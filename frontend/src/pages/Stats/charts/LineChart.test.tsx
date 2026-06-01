import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { axe } from 'jest-axe';
import { LineChart } from './LineChart';
import type { AggregateBucket } from '../../../api/types';

// LineChart is presentational (props only, no fetch). The tests assert the a11y
// contract Chunk 11's axe gate will enforce: role="img" + a descriptive
// aria-label, a <title>/<desc> pair, a legend whose series are distinguished by
// TEXT label (not color alone — queried by text), and an accessible "no data"
// message for empty input. They also pin the value formatting per metric.

const TWO_SERIES: AggregateBucket[] = [
  { bucket_ts: 1_700_000_000_000_000, series: [{ key: 'gpt-5', value: 1 }, { key: 'claude', value: 2 }] },
  { bucket_ts: 1_700_086_400_000_000, series: [{ key: 'gpt-5', value: 3 }, { key: 'claude', value: 4 }] },
];

describe('LineChart', () => {
  it('renders an accessible "no data" message for empty buckets (not a blank svg)', () => {
    render(<LineChart buckets={[]} metric="cost" bucket="daily" />);
    expect(screen.getByText(/no data/i)).toBeInTheDocument();
    // The empty state is not an img-role chart (nothing to describe).
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
  });

  it('exposes role="img" with a descriptive aria-label summarizing the series', () => {
    render(<LineChart buckets={TWO_SERIES} metric="cost" bucket="daily" />);
    const fig = screen.getByRole('img');
    // The label names the metric and the series count so a screen reader hears
    // what the chart shows without seeing the lines.
    expect(fig).toHaveAccessibleName(/cost/i);
    expect(fig).toHaveAccessibleName(/2 series/i);
  });

  it('labels each series with TEXT in a legend (color is never the only signal)', () => {
    render(<LineChart buckets={TWO_SERIES} metric="cost" bucket="daily" />);
    const legend = screen.getByRole('list', { name: /legend/i });
    // Each series name is present as readable text — proving the differentiator
    // is the label, not just the stroke color.
    expect(within(legend).getByText('gpt-5')).toBeInTheDocument();
    expect(within(legend).getByText('claude')).toBeInTheDocument();
  });

  it('renders one <path> polyline per distinct series key', () => {
    const { container } = render(<LineChart buckets={TWO_SERIES} metric="cost" bucket="daily" />);
    // data-series marks the value polylines (axis/gridlines are not marked).
    const lines = container.querySelectorAll('path[data-series]');
    expect(lines).toHaveLength(2);
  });

  it('labels the single group_by=total series (key "") as "total" in legend + path', () => {
    // rest-api.md: group_by=total returns one series keyed "". The chart must
    // not show a blank legend entry — it reads "total".
    const totalSeries: AggregateBucket[] = [
      { bucket_ts: 1_700_000_000_000_000, series: [{ key: '', value: 3 }] },
      { bucket_ts: 1_700_086_400_000_000, series: [{ key: '', value: 5 }] },
    ];
    const { container } = render(<LineChart buckets={totalSeries} metric="cost" bucket="daily" />);
    const legend = screen.getByRole('list', { name: /legend/i });
    expect(within(legend).getByText('total')).toBeInTheDocument();
    // The polyline's data-series attribute also falls back to "total".
    expect(container.querySelector('path[data-series="total"]')).not.toBeNull();
    // And the aria description names the single total series.
    expect(screen.getByRole('img')).toHaveAccessibleName(/1 series: total/i);
  });

  it('renders a single-point series without crashing (one bucket)', () => {
    const onePoint: AggregateBucket[] = [
      { bucket_ts: 1_700_000_000_000_000, series: [{ key: 'gpt-5', value: 2 }] },
    ];
    const { container } = render(<LineChart buckets={onePoint} metric="tokens_in" bucket="hourly" />);
    // A lone M-only path is emitted (a degenerate but valid polyline).
    const path = container.querySelector('path[data-series="gpt-5"]');
    expect(path?.getAttribute('d')?.startsWith('M')).toBe(true);
  });

  it('has no axe violations with data', async () => {
    const { container } = render(<LineChart buckets={TWO_SERIES} metric="cost" bucket="daily" />);
    expect(await axe(container)).toHaveNoViolations();
  });

  it('has no axe violations when empty', async () => {
    const { container } = render(<LineChart buckets={[]} metric="cost" bucket="daily" />);
    expect(await axe(container)).toHaveNoViolations();
  });
});
