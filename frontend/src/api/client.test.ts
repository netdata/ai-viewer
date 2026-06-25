import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  ApiError,
  buildIncludeQuery,
  buildQuery,
  del,
  get,
  head,
  post,
  request,
} from './client';

// client.ts is the typed fetch wrapper. These tests mock global fetch to verify
// success decoding, the structured error-envelope mapping, the 204 path, the
// HEAD / empty-body (Content-Length: 0) path, and the query-string builder.

interface FakeResponseInit {
  status: number;
  statusText?: string;
  /** json() resolver; reject to simulate a non-JSON body. */
  json: () => Promise<unknown>;
  /** Optional response headers exposed via headers.get(name). */
  headers?: Record<string, string>;
}

/** fakeResponse builds the minimal Response surface client.ts touches. The one
 *  cast is centralized here so individual tests need no assertions. */
function fakeResponse(init: FakeResponseInit): Response {
  const headerMap = init.headers ?? {};
  return {
    ok: init.status >= 200 && init.status < 300,
    status: init.status,
    statusText: init.statusText ?? 'mock',
    headers: {
      get: (name: string): string | null => {
        const key = Object.keys(headerMap).find(
          (k) => k.toLowerCase() === name.toLowerCase(),
        );
        return key !== undefined ? (headerMap[key] ?? null) : null;
      },
    },
    json: init.json,
  } as unknown as Response;
}

/** stubFetch installs a fetch mock resolving to res; returns the mock fn. */
function stubFetch(res: Response): ReturnType<typeof vi.fn> {
  const mock = vi.fn().mockResolvedValue(res);
  vi.stubGlobal('fetch', mock);
  return mock;
}

function mockFetchJson(status: number, body: unknown): void {
  stubFetch(fakeResponse({ status, json: () => Promise.resolve(body) }));
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('request/get', () => {
  it('parses a success body', async () => {
    mockFetchJson(200, { items: [{ id: 'x' }], next_cursor: 'c' });
    const data = await get<{ items: { id: string }[]; next_cursor?: string }>(
      '/sessions',
    );
    expect(data.items[0]?.id).toBe('x');
    expect(data.next_cursor).toBe('c');
  });

  it('targets the /api base with a relative URL', async () => {
    const fetchMock = stubFetch(
      fakeResponse({ status: 200, json: () => Promise.resolve({}) }),
    );
    await get('/health');
    expect(fetchMock).toHaveBeenCalledWith('/api/health', expect.any(Object));
  });

  it('maps the error envelope to a typed ApiError', async () => {
    mockFetchJson(400, {
      error: {
        code: 'BAD_REQUEST',
        message: 'bad filter',
        details: { field: 'agents' },
      },
    });
    await expect(get('/sessions?agents=')).rejects.toMatchObject({
      name: 'ApiError',
      status: 400,
      code: 'BAD_REQUEST',
      message: 'bad filter',
      details: { field: 'agents' },
    });
  });

  it('falls back to INTERNAL_ERROR when the error body is not an envelope', async () => {
    mockFetchJson(500, { something: 'else' });
    const err = await get('/x').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe('INTERNAL_ERROR');
    expect((err as ApiError).status).toBe(500);
  });

  it('falls back when the error body is not valid JSON', async () => {
    stubFetch(
      fakeResponse({
        status: 503,
        statusText: 'unavailable',
        json: () => Promise.reject(new Error('not json')),
      }),
    );
    const err = await get('/x').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(503);
    expect((err as ApiError).message).toBe('unavailable');
  });

  it('resolves 204 to undefined (del)', async () => {
    stubFetch(
      fakeResponse({
        status: 204,
        statusText: 'no content',
        json: () => Promise.reject(new Error('no body')),
      }),
    );
    await expect(del('/subscriptions/abc')).resolves.toBeUndefined();
  });

  it('resolves a HEAD request to undefined without parsing JSON', async () => {
    const jsonSpy = vi.fn(() => Promise.reject(new Error('HEAD has no body')));
    const fetchMock = stubFetch(
      fakeResponse({ status: 200, statusText: 'ok', json: jsonSpy }),
    );
    await expect(head('/health')).resolves.toBeUndefined();
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.method).toBe('HEAD');
    // Never attempted to read a body.
    expect(jsonSpy).not.toHaveBeenCalled();
  });

  it('resolves a 200 with Content-Length: 0 to undefined (no JSON parse)', async () => {
    const jsonSpy = vi.fn(() => Promise.reject(new Error('empty body')));
    stubFetch(
      fakeResponse({
        status: 200,
        json: jsonSpy,
        headers: { 'Content-Length': '0' },
      }),
    );
    await expect(get('/sources')).resolves.toBeUndefined();
    expect(jsonSpy).not.toHaveBeenCalled();
  });

  it('still parses JSON for a normal GET with a body', async () => {
    stubFetch(
      fakeResponse({
        status: 200,
        json: () => Promise.resolve({ ok: true }),
        headers: { 'Content-Length': '12' },
      }),
    );
    await expect(get<{ ok: boolean }>('/health')).resolves.toEqual({ ok: true });
  });

  it('request accepts an explicit HEAD method', async () => {
    const fetchMock = stubFetch(
      fakeResponse({ status: 200, json: () => Promise.reject(new Error('x')) }),
    );
    await expect(request('/events', { method: 'HEAD' })).resolves.toBeUndefined();
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.method).toBe('HEAD');
  });

  it('post sends a JSON body and Content-Type', async () => {
    const fetchMock = stubFetch(
      fakeResponse({ status: 200, json: () => Promise.resolve({ id: 'sub-1' }) }),
    );
    const out = await post<{ id: string }>('/subscriptions', { filter: {} });
    expect(out.id).toBe('sub-1');
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.method).toBe('POST');
    expect(init.body).toBe(JSON.stringify({ filter: {} }));
    expect((init.headers as Record<string, string>)['Content-Type']).toContain(
      'application/json',
    );
  });
});

describe('buildQuery', () => {
  it('joins arrays with commas and omits empties', () => {
    expect(
      buildQuery({ agents: ['a', 'b'], models: [], q: 'x', from: 10 }),
    ).toBe('?agents=a%2Cb&q=x&from=10');
  });

  it('returns an empty string when nothing is set', () => {
    expect(buildQuery({ agents: [], q: undefined })).toBe('');
  });

  it('stringifies number scalars', () => {
    expect(buildQuery({ from: 0 })).toBe('?from=0');
  });
});

describe('buildIncludeQuery', () => {
  it('deduplicates include tokens while preserving canonical order', () => {
    expect(buildIncludeQuery(['proof', 'payload_refs', 'proof'])).toBe(
      '?include=payload_refs%2Cproof',
    );
  });

  it('omits include when no token is set', () => {
    expect(buildIncludeQuery([])).toBe('');
  });
});
