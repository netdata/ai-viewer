// stepMeta (SOW-0090 chunk 8) — pure function tests. The functions are
// pure (no DOM, no time source) so we don't need jsdom.

import { describe, expect, it } from 'vitest';
import { formatElapsed, formatWallClock, shortOpId } from './stepMeta';

describe('formatElapsed', () => {
  it('formats microseconds under 1ms with µs suffix', () => {
    expect(formatElapsed(0)).toBe('+0µs');
    expect(formatElapsed(500)).toBe('+500µs');
    expect(formatElapsed(999)).toBe('+999µs');
  });

  it('formats sub-second values as ms', () => {
    expect(formatElapsed(1_000)).toBe('+1ms');
    expect(formatElapsed(1_234)).toBe('+1ms');
    expect(formatElapsed(45_000)).toBe('+45ms');
    expect(formatElapsed(999_000)).toBe('+999ms');
  });

  it('formats 1–10s with one decimal', () => {
    expect(formatElapsed(1_000_000)).toBe('+1.0s');
    expect(formatElapsed(1_234_567)).toBe('+1.2s');
    expect(formatElapsed(9_500_000)).toBe('+9.5s');
  });

  it('formats 10–60s as integer seconds', () => {
    expect(formatElapsed(10_000_000)).toBe('+10s');
    expect(formatElapsed(45_000_000)).toBe('+45s');
    expect(formatElapsed(59_999_000)).toBe('+59s');
  });

  it('formats 60s+ as m:ss', () => {
    expect(formatElapsed(60_000_000)).toBe('+1m00s');
    expect(formatElapsed(125_000_000)).toBe('+2m05s');
    expect(formatElapsed(3_600_000_000)).toBe('+60m00s');
  });

  it('clamps negative deltas to 0 (turns that overlap in micro-ts)', () => {
    expect(formatElapsed(-100)).toBe('+0µs');
    expect(formatElapsed(-5_000)).toBe('+0µs');
  });
});

describe('formatWallClock', () => {
  it('renders UTC time by default with Z suffix', () => {
    const ts = Date.UTC(2026, 5, 21, 11, 42, 18) * 1000;
    expect(formatWallClock(ts)).toBe('11:42:18Z');
  });

  it('renders local time when local=true', () => {
    // Use a fixed date and accept either UTC==local (CI) or whatever the
    // host's timezone is. The contract: local format has no Z suffix.
    const ts = Date.UTC(2026, 5, 21, 11, 42, 18) * 1000;
    const out = formatWallClock(ts, true);
    expect(out).toMatch(/^\d{2}:\d{2}:\d{2}$/);
    expect(out.endsWith('Z')).toBe(false);
  });

  it('zero-pads single-digit hours/minutes/seconds', () => {
    const ts = Date.UTC(2026, 0, 1, 3, 4, 5) * 1000;
    expect(formatWallClock(ts)).toBe('03:04:05Z');
  });
});

describe('shortOpId', () => {
  it('returns the full id when shorter than 8 chars', () => {
    expect(shortOpId('')).toBe('');
    expect(shortOpId('abc')).toBe('abc');
    expect(shortOpId('1234567')).toBe('1234567');
  });

  it('returns the first 8 chars for longer ids', () => {
    expect(shortOpId('12345678')).toBe('12345678');
    expect(shortOpId('abcdef0123456789')).toBe('abcdef01');
    expect(shortOpId('21cd490e83b974aee895cbd88d5bfbfd')).toBe('21cd490e');
  });
});
