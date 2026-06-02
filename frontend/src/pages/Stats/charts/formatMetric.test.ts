import { describe, expect, it } from 'vitest';
import { formatMetricValue } from './formatMetric';

// formatMetricValue routes a chart value to the right unit per the selected
// stats metric (rest-api.md enum: cost|tokens_in|tokens_out|calls|failures|
// duration_us). Cost -> $x.xx, the count metrics -> thousands-sep integer,
// duration_us -> human duration. An unknown metric degrades to a plain number.

describe('formatMetricValue', () => {
  it('formats cost as a USD amount', () => {
    expect(formatMetricValue('cost', 1.5)).toBe('$1.50');
    expect(formatMetricValue('cost', 0)).toBe('$0.00');
  });

  it('formats token/call/failure counts as thousands-separated integers', () => {
    expect(formatMetricValue('tokens_in', 12345)).toBe('12,345');
    expect(formatMetricValue('tokens_out', 1000)).toBe('1,000');
    expect(formatMetricValue('calls', 42)).toBe('42');
    expect(formatMetricValue('failures', 7)).toBe('7');
  });

  it('formats duration_us as a human duration', () => {
    // 2_000_000 µs -> "2s" (formatDuration contract).
    expect(formatMetricValue('duration_us', 2_000_000)).toBe('2s');
  });

  it('degrades an unknown metric to a plain thousands-separated number', () => {
    expect(formatMetricValue('mystery', 1234)).toBe('1,234');
  });
});
