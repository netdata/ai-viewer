import { describe, expect, it } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import {
  applyPatch,
  filtersToSubscription,
  readFilters,
  useFilters,
  type Filters,
} from './filters';

// Pure helpers are unit-tested directly; the hook is tested through a
// MemoryRouter so the URL round-trip (read params -> set -> URL updates) is
// real, matching frontend-architecture.md §State Management.

describe('readFilters', () => {
  it('decodes comma-joined arrays, numbers, and q', () => {
    const params = new URLSearchParams(
      'agents=nedi,neda&models=claude-opus-4-7&status=failed,running&from=100&to=200&q=hello',
    );
    const f = readFilters(params);
    expect(f.agents).toEqual(['nedi', 'neda']);
    expect(f.models).toEqual(['claude-opus-4-7']);
    expect(f.status).toEqual(['failed', 'running']);
    expect(f.from).toBe(100);
    expect(f.to).toBe(200);
    expect(f.q).toBe('hello');
  });

  it('returns empty arrays and undefined scalars for an empty query', () => {
    const f = readFilters(new URLSearchParams(''));
    expect(f.agents).toEqual([]);
    expect(f.tools).toEqual([]);
    expect(f.from).toBeUndefined();
    expect(f.q).toBeUndefined();
  });

  it('drops empty/whitespace tokens in arrays', () => {
    const f = readFilters(new URLSearchParams('agents=a,,  ,b'));
    expect(f.agents).toEqual(['a', 'b']);
  });

  it('ignores non-numeric from/to', () => {
    const f = readFilters(new URLSearchParams('from=abc'));
    expect(f.from).toBeUndefined();
  });

  it('parses a strict integer microsecond bound', () => {
    const f = readFilters(new URLSearchParams('from=1700000000000000'));
    expect(f.from).toBe(1700000000000000);
  });

  it('rejects trailing-garbage and fractional numbers (no silent truncation)', () => {
    // Backend expects a strict integer; "123abc" and "1.2" must NOT become
    // 123 / 1 as Number.parseInt would — the param is treated as absent.
    expect(readFilters(new URLSearchParams('from=123abc')).from).toBeUndefined();
    expect(readFilters(new URLSearchParams('from=1.2')).from).toBeUndefined();
  });

  it('treats an empty from param as absent', () => {
    expect(readFilters(new URLSearchParams('from=')).from).toBeUndefined();
  });

  it('rejects an unsafe-integer string', () => {
    // 2^53 is not a safe integer; treat as absent rather than lose precision.
    const f = readFilters(new URLSearchParams('from=9007199254740993'));
    expect(f.from).toBeUndefined();
  });
});

describe('applyPatch', () => {
  it('sets and clears array params', () => {
    const start = new URLSearchParams('agents=a,b');
    const set = applyPatch(start, { models: ['m1', 'm2'] });
    expect(set.get('models')).toBe('m1,m2');
    // Empty array deletes the key.
    const cleared = applyPatch(set, { agents: [] });
    expect(cleared.get('agents')).toBeNull();
    expect(cleared.get('models')).toBe('m1,m2');
  });

  it('sets and deletes scalar params', () => {
    const start = new URLSearchParams();
    const withFrom = applyPatch(start, { from: 42 });
    expect(withFrom.get('from')).toBe('42');
    const withoutFrom = applyPatch(withFrom, { from: undefined });
    expect(withoutFrom.get('from')).toBeNull();
  });

  it('deletes q when patched with empty/whitespace', () => {
    const start = new URLSearchParams('q=hi');
    expect(applyPatch(start, { q: '   ' }).get('q')).toBeNull();
    expect(applyPatch(start, { q: undefined }).get('q')).toBeNull();
  });

  it('leaves keys absent from the patch untouched', () => {
    const start = new URLSearchParams('agents=a&q=hi');
    const next = applyPatch(start, { models: ['m'] });
    expect(next.get('agents')).toBe('a');
    expect(next.get('q')).toBe('hi');
  });
});

describe('useFilters', () => {
  function wrapperFor(initial: string) {
    return function Wrapper({ children }: { children: React.ReactNode }) {
      return <MemoryRouter initialEntries={[initial]}>{children}</MemoryRouter>;
    };
  }

  it('reads filters from the URL search params', () => {
    const { result } = renderHook(() => useFilters(), {
      wrapper: wrapperFor('/?agents=nedi&status=failed'),
    });
    expect(result.current.filters.agents).toEqual(['nedi']);
    expect(result.current.filters.status).toEqual(['failed']);
  });

  it('setFilters writes back to the URL', () => {
    const { result } = renderHook(
      () => ({ filters: useFilters(), loc: useLocation() }),
      { wrapper: wrapperFor('/') },
    );
    act(() => {
      result.current.filters.setFilters({ models: ['m1', 'm2'] });
    });
    expect(result.current.loc.search).toContain('models=m1%2Cm2');
    expect(result.current.filters.filters.models).toEqual(['m1', 'm2']);
  });

  it('array params round-trip through the URL', () => {
    const { result } = renderHook(() => useFilters(), {
      wrapper: wrapperFor('/'),
    });
    const sample: Pick<Filters, 'agents' | 'tools'> = {
      agents: ['a', 'b', 'c'],
      tools: ['mcp__slack__send_message'],
    };
    act(() => {
      result.current.setFilters(sample);
    });
    expect(result.current.filters.agents).toEqual(['a', 'b', 'c']);
    expect(result.current.filters.tools).toEqual(['mcp__slack__send_message']);
  });

  it('clearFilters removes every filter param', () => {
    const { result } = renderHook(() => useFilters(), {
      wrapper: wrapperFor('/?agents=a&models=m&from=1&q=x'),
    });
    act(() => {
      result.current.clearFilters();
    });
    expect(result.current.filters.agents).toEqual([]);
    expect(result.current.filters.models).toEqual([]);
    expect(result.current.filters.from).toBeUndefined();
    expect(result.current.filters.q).toBeUndefined();
  });
});

const EMPTY: Filters = { agents: [], models: [], tools: [], status: [], sources: [] };

describe('filtersToSubscription', () => {
  it('maps an empty filter set to an empty subscription filter (no constraints)', () => {
    expect(filtersToSubscription(EMPTY)).toEqual({});
  });

  it('includes only non-empty array dimensions', () => {
    const sub = filtersToSubscription({
      ...EMPTY,
      agents: ['nedi'],
      models: [],
      status: ['failed', 'running'],
      sources: ['src-a'],
    });
    expect(sub).toEqual({
      agents: ['nedi'],
      status: ['failed', 'running'],
      sources: ['src-a'],
    });
    // Empty dimensions are omitted, never sent as a present-but-empty array.
    expect('models' in sub).toBe(false);
    expect('tools' in sub).toBe(false);
  });

  it('builds a time_range from from/to when either is set', () => {
    expect(filtersToSubscription({ ...EMPTY, from: 100 })).toEqual({
      time_range: { from: 100 },
    });
    expect(filtersToSubscription({ ...EMPTY, to: 200 })).toEqual({
      time_range: { to: 200 },
    });
    expect(filtersToSubscription({ ...EMPTY, from: 100, to: 200 })).toEqual({
      time_range: { from: 100, to: 200 },
    });
  });

  it('drops q (the SSE filter has no free-text field)', () => {
    const sub = filtersToSubscription({ ...EMPTY, q: 'nedi', agents: ['a'] });
    expect(sub).toEqual({ agents: ['a'] });
  });
});
