import { describe, expect, it } from 'vitest';
import type { AggregateBucket } from '../../api/types';
import { capAndRoll, capAndRollRate, rateBuckets, sortRows } from './Stats';
import type { BreakdownRow } from './Stats';

// Pure-helper tests for the SOW-0067 trend rollup + comparison sort. These pin
// the round-1/round-2 correctness fixes that are awkward to assert through the
// rendered chart: the rate-aware 'other' (weighted, not sum-of-ratios) and the
// nulls-last sort.

function bucket(ts: number, series: Array<{ key: string; value: number }>): AggregateBucket {
  return { bucket_ts: ts, series };
}

describe('rateBuckets', () => {
  it('divides failures by calls per (bucket, key) and yields 0 when calls=0', () => {
    const failures = [bucket(1, [{ key: 'a', value: 5 }, { key: 'b', value: 3 }])];
    const calls = [bucket(1, [{ key: 'a', value: 10 }, { key: 'b', value: 0 }])];
    const out = rateBuckets(failures, calls);
    expect(out[0]?.series).toEqual([
      { key: 'a', value: 0.5 },
      { key: 'b', value: 0 }, // never NaN even with zero calls
    ]);
  });
});

describe('capAndRoll (absolute metrics)', () => {
  it('rolls dropped series into "other" by SUM (correct for absolute values)', () => {
    // 3 series, limit 2 → the smallest is rolled into 'other'.
    const buckets = [
      bucket(1, [
        { key: 'big', value: 100 },
        { key: 'mid', value: 50 },
        { key: 'small', value: 10 },
      ]),
    ];
    const out = capAndRoll(buckets, 2);
    expect(out.truncated).toBe(1);
    const keys = out.buckets[0]?.series.map((s) => s.key) ?? [];
    expect(keys).toContain('big');
    expect(keys).toContain('mid');
    expect(keys).toContain('other');
    expect(out.buckets[0]?.series.find((s) => s.key === 'other')?.value).toBe(10);
  });

  it('leaves group_by=total (key "") untouched', () => {
    const buckets = [bucket(1, [{ key: '', value: 42 }])];
    const out = capAndRoll(buckets, 8);
    expect(out.truncated).toBe(0);
    expect(out.buckets[0]?.series).toEqual([{ key: '', value: 42 }]);
  });
});

describe('capAndRollRate (failure_rate — round-2 correctness fix)', () => {
  it('computes "other" as Σfailures/Σcalls (weighted), NOT Σ(ratios)', () => {
    // Two dropped series in one bucket: A 10 failures/100 calls (10%),
    // B 1 failure/1000 calls (0.1%). A naive sum-of-ratios gives 0.101; the
    // correct weighted rate is 11/1100 = 0.01.
    const limit = 1; // keep only 'keep', roll A and B into 'other'
    const failures = [
      bucket(1, [
        { key: 'keep', value: 5 },
        { key: 'A', value: 10 },
        { key: 'B', value: 1 },
      ]),
    ];
    const calls = [
      bucket(1, [
        { key: 'keep', value: 2000 }, // highest call volume → kept
        { key: 'A', value: 100 },
        { key: 'B', value: 1000 },
      ]),
    ];
    const out = capAndRollRate(failures, calls, limit);
    expect(out.truncated).toBe(2);
    const other = out.buckets[0]?.series.find((s) => s.key === 'other');
    expect(other?.value).toBeCloseTo(11 / 1100, 6); // weighted, not 0.101
  });

  it('keeps the highest-call-volume series (ranks by the rate denominator)', () => {
    const failures = [
      bucket(1, [
        { key: 'rare-but-faily', value: 9 },
        { key: 'common', value: 1 },
      ]),
    ];
    const calls = [
      bucket(1, [
        { key: 'rare-but-faily', value: 10 },
        { key: 'common', value: 1000 },
      ]),
    ];
    const out = capAndRollRate(failures, calls, 1);
    const keys = out.buckets[0]?.series.map((s) => s.key) ?? [];
    expect(keys).toContain('common'); // kept (highest call volume)
    expect(keys).toContain('other'); // rare-but-faily rolled in
  });

  it('leaves group_by=total untouched (single "" series)', () => {
    const failures = [bucket(1, [{ key: '', value: 3 }])];
    const calls = [bucket(1, [{ key: '', value: 10 }])];
    const out = capAndRollRate(failures, calls, 8);
    expect(out.truncated).toBe(0);
    expect(out.buckets[0]?.series).toEqual([{ key: '', value: 0.3 }]);
  });
});

describe('sortRows (nulls-last)', () => {
  const row = (over: Partial<BreakdownRow>): BreakdownRow => ({
    label: over.label ?? 'x',
    sublabel: undefined,
    count: 0,
    countLabel: 'Sessions',
    failures: 0,
    failureRate: 0,
    cost: undefined,
    tokensIn: undefined,
    tokensOut: undefined,
    cacheRead: undefined,
    cacheHit: undefined,
    duration: undefined,
    drill: undefined,
    ...over,
  });

  it('sorts N/A (undefined) cells LAST in both ascending and descending', () => {
    const rows = [
      row({ label: 'has', cost: 5 }),
      row({ label: 'missing', cost: undefined }),
      row({ label: 'has2', cost: 1 }),
    ];
    const asc = sortRows(rows, 'cost', 'asc');
    expect(asc.map((r) => r.label)).toEqual(['has2', 'has', 'missing']);
    const desc = sortRows(rows, 'cost', 'desc');
    expect(desc.map((r) => r.label)).toEqual(['has', 'has2', 'missing']);
  });
});
