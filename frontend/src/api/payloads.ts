import { API_BASE, ApiError } from './client';
import type { ErrorEnvelope } from './types';

export interface PayloadHeaders {
  contentType: string;
  format: string;
  truncated: boolean;
  totalBytes: number | null;
  previewBytes?: number;
}

export interface PayloadContent {
  text: string;
  headers: PayloadHeaders;
}

export interface PayloadFetchOptions {
  full?: boolean;
  signal?: AbortSignal;
}

function payloadURL(id: number, full = false): string {
  const suffix = full ? '?full=1' : '';
  return `${API_BASE}/payloads/${encodeURIComponent(String(id))}${suffix}`;
}

function numberHeader(headers: Headers, name: string): number | null {
  const raw = headers.get(name);
  if (raw === null) {
    return null;
  }
  const parsed = Number.parseInt(raw, 10);
  return Number.isFinite(parsed) ? parsed : null;
}

function parsePayloadHeaders(headers: Headers): PayloadHeaders {
  const previewBytes = numberHeader(headers, 'X-Payload-Preview-Bytes');
  const out: PayloadHeaders = {
    contentType: headers.get('Content-Type') ?? 'text/plain; charset=utf-8',
    format: headers.get('X-Payload-Format') ?? 'text',
    truncated: headers.get('X-Payload-Truncated') === 'true',
    totalBytes: numberHeader(headers, 'X-Payload-Total-Bytes'),
  };
  if (previewBytes !== null) {
    out.previewBytes = previewBytes;
  }
  return out;
}

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

async function payloadError(res: Response): Promise<ApiError> {
  let body = '';
  try {
    body = await res.text();
    const parsed: unknown = JSON.parse(body);
    if (isErrorEnvelope(parsed)) {
      return new ApiError(
        res.status,
        parsed.error.code,
        parsed.error.message,
        parsed.error.details,
      );
    }
  } catch {
    // Text payload route errors should still be JSON envelopes, but keep the
    // fallback deterministic if a proxy or older binary returns plain text.
  }
  const suffix = body.length > 0 ? `: ${body.slice(0, 200)}` : '';
  return new ApiError(res.status, 'INTERNAL_ERROR', `HTTP ${res.status}${suffix}`);
}

export async function fetchPayloadContent(
  id: number,
  opts: PayloadFetchOptions = {},
): Promise<PayloadContent> {
  const init: RequestInit = {
    method: 'GET',
    headers: { Accept: 'text/plain' },
  };
  if (opts.signal) {
    init.signal = opts.signal;
  }
  const res = await fetch(payloadURL(id, opts.full ?? false), init);
  if (!res.ok) {
    throw await payloadError(res);
  }
  return {
    text: await res.text(),
    headers: parsePayloadHeaders(res.headers),
  };
}

export async function headPayloadContent(
  id: number,
  signal?: AbortSignal,
): Promise<PayloadHeaders> {
  const init: RequestInit = {
    method: 'HEAD',
    headers: { Accept: 'text/plain' },
  };
  if (signal) {
    init.signal = signal;
  }
  const res = await fetch(payloadURL(id), init);
  if (!res.ok) {
    throw await payloadError(res);
  }
  return parsePayloadHeaders(res.headers);
}
