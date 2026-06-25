import type { ErrorEnvelope, ErrorCode } from './types';

// Typed fetch wrapper. The SPA is served same-origin by the Go binary, so the
// base is the relative `/api` prefix — never an absolute host (no operator
// path or hostname is ever baked in). On a non-2xx response the server returns
// the structured envelope `{ error: { code, message, details } }`
// (rest-api.md §Conventions); this wrapper decodes it into a typed ApiError so
// callers (and TanStack Query) get a stable error surface.

/** Base path for every API call. Relative → same-origin as the served SPA. */
export const API_BASE = '/api';

/**
 * ApiError carries the structured server error. `code` is the stable
 * machine-readable string; `status` is the HTTP status. `details` is the
 * optional context bag the server attaches.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ErrorCode;
  readonly details: Record<string, unknown> | undefined;

  constructor(
    status: number,
    code: ErrorCode,
    message: string,
    details?: Record<string, unknown>,
  ) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

/** Options accepted by the request helpers. */
export interface RequestOptions {
  signal?: AbortSignal;
  method?: 'GET' | 'POST' | 'DELETE' | 'HEAD';
  /** JSON-serializable request body (POST). */
  body?: unknown;
}

/** isErrorEnvelope narrows an unknown parsed body to the error envelope. */
function isErrorEnvelope(value: unknown): value is ErrorEnvelope {
  if (typeof value !== 'object' || value === null) {
    return false;
  }
  const err = (value as { error?: unknown }).error;
  return (
    typeof err === 'object' &&
    err !== null &&
    typeof (err as { code?: unknown }).code === 'string' &&
    typeof (err as { message?: unknown }).message === 'string'
  );
}

/** parseErrorBody turns a non-2xx response into a typed ApiError. */
async function parseErrorBody(res: Response): Promise<ApiError> {
  let parsed: unknown;
  try {
    parsed = await res.json();
  } catch {
    return new ApiError(res.status, 'INTERNAL_ERROR', res.statusText || 'request failed');
  }
  if (isErrorEnvelope(parsed)) {
    const { code, message, details } = parsed.error;
    return new ApiError(res.status, code, message, details);
  }
  return new ApiError(res.status, 'INTERNAL_ERROR', res.statusText || 'request failed');
}

/**
 * isBodiless reports whether a successful response carries no body to parse:
 * a HEAD request (bodiless by definition, RFC 9110 §9.3.2), a 204 No Content,
 * or any 2xx whose Content-Length is explicitly 0. Parsing JSON from an empty
 * body throws, so these resolve to undefined instead.
 */
function isBodiless(method: string, res: Response): boolean {
  return (
    method === 'HEAD' ||
    res.status === 204 ||
    res.headers.get('Content-Length') === '0'
  );
}

/**
 * request issues an HTTP request to the API and returns the parsed JSON body
 * typed as T. Throws ApiError on any non-2xx response (decoding the envelope),
 * and rethrows AbortError unchanged so TanStack Query can treat cancellations
 * distinctly. Bodiless successes (HEAD, 204 No Content, or a 2xx with
 * Content-Length: 0) resolve to undefined cast to T without a JSON parse.
 */
export async function request<T>(
  path: string,
  opts: RequestOptions = {},
): Promise<T> {
  const url = `${API_BASE}${path}`;
  const method = opts.method ?? 'GET';
  const init: RequestInit = {
    method,
    headers: { Accept: 'application/json' },
  };
  if (opts.signal) {
    init.signal = opts.signal;
  }
  if (opts.body !== undefined) {
    init.headers = { ...init.headers, 'Content-Type': 'application/json' };
    init.body = JSON.stringify(opts.body);
  }

  const res = await fetch(url, init);
  if (!res.ok) {
    throw await parseErrorBody(res);
  }
  if (isBodiless(method, res)) {
    return undefined as T;
  }
  return (await res.json()) as T;
}

/** get is the GET convenience over request. */
export function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  return signal ? request<T>(path, { signal }) : request<T>(path);
}

/** post issues a POST with a JSON body. */
export function post<T>(
  path: string,
  body: unknown,
  signal?: AbortSignal,
): Promise<T> {
  return request<T>(
    path,
    signal ? { method: 'POST', body, signal } : { method: 'POST', body },
  );
}

/** del issues a DELETE; resolves to void (the API returns 204). */
export function del(path: string, signal?: AbortSignal): Promise<void> {
  return request<undefined>(
    path,
    signal ? { method: 'DELETE', signal } : { method: 'DELETE' },
  );
}

/**
 * head issues a HEAD; resolves to void. Used for cheap liveness checks against
 * /api/health, /api/sources, and /api/events (rest-api.md), which return
 * headers with an empty body.
 */
export function head(path: string, signal?: AbortSignal): Promise<void> {
  return request<undefined>(
    path,
    signal ? { method: 'HEAD', signal } : { method: 'HEAD' },
  );
}

/**
 * buildQuery serializes filter params for the list/stats endpoints. Array
 * dimensions are comma-joined (the Go endpoints accept that form); empty
 * arrays and undefined scalars are omitted (absent key = no constraint). Used
 * by the endpoint helpers so query construction is consistent and testable.
 */
export function buildQuery(
  params: Record<string, string | number | string[] | undefined>,
): string {
  const sp = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined) {
      continue;
    }
    if (Array.isArray(value)) {
      if (value.length > 0) {
        sp.set(key, value.join(','));
      }
    } else {
      sp.set(key, String(value));
    }
  }
  const qs = sp.toString();
  return qs ? `?${qs}` : '';
}

export type IncludeToken = 'payload_refs' | 'proof' | 'cursors';

const INCLUDE_ORDER: IncludeToken[] = ['payload_refs', 'proof', 'cursors'];

export function buildIncludeQuery(tokens: IncludeToken[]): string {
  const selected = new Set(tokens);
  const ordered = INCLUDE_ORDER.filter((token) => selected.has(token));
  if (ordered.length === 0) {
    return '';
  }
  const sp = new URLSearchParams();
  sp.set('include', ordered.join(','));
  return `?${sp.toString()}`;
}
