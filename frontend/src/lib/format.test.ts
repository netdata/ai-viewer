import { describe, expect, it } from 'vitest';
import {
  cacheHitRate,
  formatBytes,
  formatCost,
  formatDuration,
  formatNumber,
  formatPct,
  formatTimestamp,
} from './format';

// Formatter unit tests. Timestamps are µs UNIX; durations are µs. Each helper
// returns an em dash for null/undefined/non-finite inputs.

describe('formatTimestamp', () => {
  it('renders a finite µs timestamp as a local string', () => {
    // 2021-01-01T00:00:00Z in µs.
    const us = Date.UTC(2021, 0, 1) * 1000;
    const out = formatTimestamp(us);
    expect(out).not.toBe('—');
    expect(out.length).toBeGreaterThan(0);
  });

  it('renders an em dash for null/undefined/NaN', () => {
    expect(formatTimestamp(null)).toBe('—');
    expect(formatTimestamp(undefined)).toBe('—');
    expect(formatTimestamp(Number.NaN)).toBe('—');
  });
});

describe('formatDuration', () => {
  it('formats microseconds', () => {
    expect(formatDuration(500)).toBe('500µs');
  });
  it('formats milliseconds', () => {
    expect(formatDuration(2_500)).toBe('3ms'); // rounded
    expect(formatDuration(850_000)).toBe('850ms');
  });
  it('formats seconds with trimmed zero', () => {
    expect(formatDuration(1_000_000)).toBe('1s');
    expect(formatDuration(1_500_000)).toBe('1.5s');
  });
  it('formats minutes and seconds', () => {
    expect(formatDuration(150_000_000)).toBe('2m 30s');
    expect(formatDuration(120_000_000)).toBe('2m');
  });
  it('formats hours and minutes', () => {
    expect(formatDuration(3_900_000_000)).toBe('1h 5m');
    expect(formatDuration(3_600_000_000)).toBe('1h');
  });
  it('clamps negatives and handles null', () => {
    expect(formatDuration(-5)).toBe('0µs');
    expect(formatDuration(null)).toBe('—');
  });
});

describe('formatBytes', () => {
  it('formats bytes, KB, MB', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(2048)).toBe('2.0 KB');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB');
  });
  it('handles null and negatives', () => {
    expect(formatBytes(null)).toBe('—');
    expect(formatBytes(-1)).toBe('0 B');
  });
});

describe('formatCost', () => {
  it('renders zero and normal costs', () => {
    expect(formatCost(0)).toBe('$0.00');
    expect(formatCost(0.42)).toBe('$0.42');
    expect(formatCost(12.5)).toBe('$12.50');
  });
  it('uses extra precision for sub-cent costs', () => {
    expect(formatCost(0.0012)).toBe('$0.0012');
  });
  it('handles null', () => {
    expect(formatCost(null)).toBe('—');
  });
});

describe('formatNumber', () => {
  it('adds thousands separators', () => {
    expect(formatNumber(12345)).toBe((12345).toLocaleString());
  });
  it('handles null', () => {
    expect(formatNumber(null)).toBe('—');
  });
});

describe('formatPct', () => {
  it('renders a ratio as a percent', () => {
    expect(formatPct(0.42)).toBe('42.0%');
    expect(formatPct(1)).toBe('100.0%');
    expect(formatPct(0)).toBe('0.0%');
  });
  it('handles null', () => {
    expect(formatPct(null)).toBe('—');
  });
});

describe('cacheHitRate', () => {
  it('computes cache_read / (fresh + cache_read + cache_write)', () => {
    // 3000 read / (1000 fresh + 3000 read + 500 write) = 3000 / 4500 = 0.6667
    expect(cacheHitRate(1000, 3000, 500)).toBeCloseTo(3000 / 4500, 10);
  });
  it('returns null when the denominator is zero (no input at all)', () => {
    expect(cacheHitRate(0, 0, 0)).toBeNull();
  });
  it('is 0 when there is fresh input but no cache reads', () => {
    expect(cacheHitRate(1000, 0, 0)).toBe(0);
  });
});
