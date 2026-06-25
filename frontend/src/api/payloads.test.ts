import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  fetchPayloadContent,
  headPayloadContent,
  type PayloadContent,
  type PayloadHeaders,
} from './payloads';
import { ApiError } from './client';

function fakePayloadResponse(body: string, headers: Record<string, string>): Response {
  return {
    ok: true,
    status: 200,
    statusText: 'ok',
    headers: {
      get: (name: string): string | null => {
        const key = Object.keys(headers).find(
          (h) => h.toLowerCase() === name.toLowerCase(),
        );
        return key === undefined ? null : headers[key] ?? null;
      },
    },
    text: () => Promise.resolve(body),
    json: () => Promise.reject(new Error('payload route is text, not JSON')),
  } as unknown as Response;
}

function stubPayloadFetch(body = 'payload text'): ReturnType<typeof vi.fn> {
  const mock = vi.fn().mockResolvedValue(
    fakePayloadResponse(body, {
      'Content-Type': 'text/plain; charset=utf-8',
      'X-Payload-Format': 'json',
      'X-Payload-Truncated': 'false',
      'X-Payload-Total-Bytes': '120',
      'X-Payload-Preview-Bytes': '12',
    }),
  );
  vi.stubGlobal('fetch', mock);
  return mock;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('payload byte-streaming API client', () => {
  it('fetches text and parses payload metadata headers', async () => {
    const fetchMock = stubPayloadFetch('payload text');
    const content: PayloadContent = await fetchPayloadContent(42);

    expect(fetchMock).toHaveBeenCalledWith('/api/payloads/42', expect.any(Object));
    expect(content.text).toBe('payload text');
    expect(content.headers).toEqual<PayloadHeaders>({
      contentType: 'text/plain; charset=utf-8',
      format: 'json',
      truncated: false,
      totalBytes: 120,
      previewBytes: 12,
    });
  });

  it('passes full=1 only for full payload requests', async () => {
    const fetchMock = stubPayloadFetch();
    await fetchPayloadContent(42, { full: true });
    expect(fetchMock).toHaveBeenCalledWith('/api/payloads/42?full=1', expect.any(Object));
  });

  it('supports HEAD metadata without reading a text body', async () => {
    const fetchMock = stubPayloadFetch('');
    const headers = await headPayloadContent(42);
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;

    expect(init.method).toBe('HEAD');
    expect(headers.totalBytes).toBe(120);
  });

  it('passes abort signals through GET and HEAD requests', async () => {
    const fetchMock = stubPayloadFetch();
    const controller = new AbortController();

    await fetchPayloadContent(42, { signal: controller.signal });
    await headPayloadContent(43, controller.signal);

    const getInit = fetchMock.mock.calls[0]?.[1] as RequestInit;
    const headInit = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(getInit.signal).toBe(controller.signal);
    expect(headInit.signal).toBe(controller.signal);
  });

  it('propagates abort failures without wrapping or retrying', async () => {
    const abortError = new DOMException('The operation was aborted.', 'AbortError');
    const fetchMock = vi.fn().mockRejectedValue(abortError);
    vi.stubGlobal('fetch', fetchMock);

    await expect(fetchPayloadContent(42, { signal: new AbortController().signal })).rejects.toBe(
      abortError,
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('decodes structured error envelopes from non-OK responses', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          error: {
            code: 'NOT_FOUND',
            message: 'payload not found',
            details: { id: 404 },
          },
        }),
        { status: 404, headers: { 'Content-Type': 'application/json' } },
      ),
    );
    vi.stubGlobal('fetch', fetchMock);

    const request = fetchPayloadContent(404);
    await expect(request).rejects.toBeInstanceOf(ApiError);
    await expect(request).rejects.toMatchObject({
      name: 'ApiError',
      status: 404,
      code: 'NOT_FOUND',
      message: 'payload not found',
      details: { id: 404 },
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('falls back to a deterministic internal error for plain-text failures', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response('proxy failure', {
        status: 500,
        headers: { 'Content-Type': 'text/plain; charset=utf-8' },
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    const request = headPayloadContent(42);
    await expect(request).rejects.toBeInstanceOf(ApiError);
    await expect(request).rejects.toMatchObject({
      name: 'ApiError',
      status: 500,
      code: 'INTERNAL_ERROR',
      message: 'HTTP 500: proxy failure',
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
