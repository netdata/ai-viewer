import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchOpPayloadRefs, fetchSessionTrace, fetchTurnPayloadRefs } from './sessions';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('session API clients', () => {
  it('builds trace include tokens for payload refs and proof', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ root_id: 's1', ops: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    await fetchSessionTrace('s1', { includePayloadRefs: true, includeProof: true });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/sessions/s1/trace?include=payload_refs%2Cproof',
      expect.any(Object),
    );
  });

  it('builds proof include tokens for lazy op payload refs', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ refs: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    await fetchOpPayloadRefs('s1', 'op1', { includeProof: true });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/sessions/s1/payload_refs?op=op1&include=proof',
      expect.any(Object),
    );
  });

  it('builds proof include tokens for lazy turn payload refs', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ refs: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    await fetchTurnPayloadRefs('s1', 'turn1', { includeProof: true });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/sessions/s1/payload_refs?turn=turn1&include=proof',
      expect.any(Object),
    );
  });
});
