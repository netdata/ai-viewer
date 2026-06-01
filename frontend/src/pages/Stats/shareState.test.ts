import { describe, expect, it } from 'vitest';
import {
  STAT_CONTROL_DEFAULTS,
  STAT_PARAM_KEYS,
  applyStatPatch,
  readStatControls,
} from './shareState';

// shareState is the pure URL<->control codec for /stats (mirrors filters.ts).
// These cover the round-trip, the clamp-on-invalid contract, and the merge that
// lets the chart-control params coexist with the global filter params.

describe('readStatControls', () => {
  it('returns the defaults when no control params are present', () => {
    const c = readStatControls(new URLSearchParams(''));
    expect(c).toEqual(STAT_CONTROL_DEFAULTS);
  });

  it('reads each control param into its typed value', () => {
    const params = new URLSearchParams(
      `${STAT_PARAM_KEYS.trendMetric}=tokens_in&${STAT_PARAM_KEYS.bucket}=hourly` +
        `&${STAT_PARAM_KEYS.topDimension}=tool&${STAT_PARAM_KEYS.topMetric}=failures`,
    );
    expect(readStatControls(params)).toEqual({
      trendMetric: 'tokens_in',
      bucket: 'hourly',
      topDimension: 'tool',
      topMetric: 'failures',
    });
  });

  it('falls back to the default for an unknown/invalid value (no throw)', () => {
    const params = new URLSearchParams(
      `${STAT_PARAM_KEYS.trendMetric}=bogus&${STAT_PARAM_KEYS.bucket}=weekly` +
        `&${STAT_PARAM_KEYS.topDimension}=nonsense&${STAT_PARAM_KEYS.topMetric}=`,
    );
    expect(() => readStatControls(params)).not.toThrow();
    expect(readStatControls(params)).toEqual(STAT_CONTROL_DEFAULTS);
  });

  it('clamps only the invalid control, keeping the valid ones', () => {
    const params = new URLSearchParams(
      `${STAT_PARAM_KEYS.trendMetric}=tokens_out&${STAT_PARAM_KEYS.bucket}=invalid`,
    );
    const c = readStatControls(params);
    expect(c.trendMetric).toBe('tokens_out'); // valid → kept
    expect(c.bucket).toBe(STAT_CONTROL_DEFAULTS.bucket); // invalid → default
  });
});

describe('applyStatPatch', () => {
  it('writes a control param onto a copy without mutating the input', () => {
    const current = new URLSearchParams('');
    const next = applyStatPatch(current, { trendMetric: 'failures' });
    expect(next.get(STAT_PARAM_KEYS.trendMetric)).toBe('failures');
    // input is untouched (functional, not in-place).
    expect(current.get(STAT_PARAM_KEYS.trendMetric)).toBeNull();
  });

  it('PRESERVES existing global filter params when patching a control', () => {
    const current = new URLSearchParams('agents=a,b&from=100&q=hello');
    const next = applyStatPatch(current, { bucket: 'hourly' });
    expect(next.get(STAT_PARAM_KEYS.bucket)).toBe('hourly');
    expect(next.get('agents')).toBe('a,b');
    expect(next.get('from')).toBe('100');
    expect(next.get('q')).toBe('hello');
  });

  it('PRESERVES an existing control param when patching a different control', () => {
    const current = new URLSearchParams(`${STAT_PARAM_KEYS.trendMetric}=tokens_in`);
    const next = applyStatPatch(current, { topDimension: 'agent' });
    expect(next.get(STAT_PARAM_KEYS.trendMetric)).toBe('tokens_in'); // untouched
    expect(next.get(STAT_PARAM_KEYS.topDimension)).toBe('agent'); // patched
  });

  it('round-trips: read(applyStatPatch(...)) yields the patched controls', () => {
    const next = applyStatPatch(new URLSearchParams(''), {
      trendMetric: 'duration_us',
      bucket: 'hourly',
      topDimension: 'cwd',
      topMetric: 'tokens_out',
    });
    expect(readStatControls(next)).toEqual({
      trendMetric: 'duration_us',
      bucket: 'hourly',
      topDimension: 'cwd',
      topMetric: 'tokens_out',
    });
  });
});
