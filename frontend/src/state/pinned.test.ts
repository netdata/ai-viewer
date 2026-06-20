import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { usePinned } from './pinned';

const STORAGE_KEY = 'ai-viewer.pinned-sessions.v1';

describe('usePinned', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });
  afterEach(() => {
    window.localStorage.clear();
  });

  it('returns an empty list when localStorage is empty', () => {
    const { result } = renderHook(() => usePinned());
    expect(result.current.pinned).toEqual([]);
  });

  it('toggles a pin on and off', () => {
    const { result } = renderHook(() => usePinned());
    act(() => { result.current.toggle('s1'); });
    expect(result.current.pinned).toEqual(['s1']);
    expect(window.localStorage.getItem(STORAGE_KEY)).toBe(JSON.stringify(['s1']));
    act(() => { result.current.toggle('s1'); });
    expect(result.current.pinned).toEqual([]);
  });

  it('newest pin is first', () => {
    const { result } = renderHook(() => usePinned());
    act(() => { result.current.toggle('s1'); });
    act(() => { result.current.toggle('s2'); });
    act(() => { result.current.toggle('s3'); });
    expect(result.current.pinned).toEqual(['s3', 's2', 's1']);
  });

  it('caps at 10 pins (oldest evicted)', () => {
    const { result } = renderHook(() => usePinned());
    for (let i = 0; i < 12; i += 1) {
      act(() => { result.current.toggle(`s${i}`); });
    }
    expect(result.current.pinned.length).toBe(10);
    expect(result.current.pinned[0]).toBe('s11'); // most recent
    expect(result.current.pinned.at(-1)).toBe('s2'); // s0 + s1 evicted
    expect(result.current.pinned).not.toContain('s0');
    expect(result.current.pinned).not.toContain('s1');
  });

  it('reads existing pins from localStorage on mount', () => {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(['existing-1', 'existing-2']));
    const { result } = renderHook(() => usePinned());
    expect(result.current.pinned).toEqual(['existing-1', 'existing-2']);
  });

  it('isPinned reflects current state', () => {
    const { result } = renderHook(() => usePinned());
    expect(result.current.isPinned('s1')).toBe(false);
    act(() => { result.current.toggle('s1'); });
    expect(result.current.isPinned('s1')).toBe(true);
  });

  it('remove drops a specific id without affecting others', () => {
    const { result } = renderHook(() => usePinned());
    act(() => { result.current.toggle('s1'); });
    act(() => { result.current.toggle('s2'); });
    act(() => { result.current.remove('s1'); });
    expect(result.current.pinned).toEqual(['s2']);
  });

  it('clear empties the list', () => {
    const { result } = renderHook(() => usePinned());
    act(() => { result.current.toggle('s1'); });
    act(() => { result.current.toggle('s2'); });
    act(() => { result.current.clear(); });
    expect(result.current.pinned).toEqual([]);
    expect(window.localStorage.getItem(STORAGE_KEY)).toBe('[]');
  });

  it('ignores malformed localStorage values', () => {
    window.localStorage.setItem(STORAGE_KEY, '{not-valid-json');
    const { result } = renderHook(() => usePinned());
    expect(result.current.pinned).toEqual([]);
  });

  it('ignores non-array localStorage values', () => {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify({ not: 'an array' }));
    const { result } = renderHook(() => usePinned());
    expect(result.current.pinned).toEqual([]);
  });
});
