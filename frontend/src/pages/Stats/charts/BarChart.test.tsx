import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { axe } from 'jest-axe';
import { BarChart } from './BarChart';
import type { TopItem } from '../../../api/types';

// BarChart is presentational (props only). It draws horizontal bars for a
// pre-sorted top-N list. The tests assert the same a11y contract as LineChart:
// role="img" + aria-label, each bar labelled with its key + value as TEXT (not
// color-only — queried by text), and an accessible "no data" message for empty
// input. Value formatting is pinned per metric.

const ITEMS: TopItem[] = [
  { key: 'claude-opus', value: 5 },
  { key: 'gpt-5', value: 2 },
];

describe('BarChart', () => {
  it('renders an accessible "no data" message for empty items', () => {
    render(<BarChart items={[]} dimension="model" metric="cost" />);
    expect(screen.getByText(/no data/i)).toBeInTheDocument();
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
  });

  it('exposes role="img" with a descriptive aria-label (dimension + metric)', () => {
    render(<BarChart items={ITEMS} dimension="model" metric="cost" />);
    const fig = screen.getByRole('img');
    expect(fig).toHaveAccessibleName(/model/i);
    expect(fig).toHaveAccessibleName(/cost/i);
  });

  it('labels each bar with its key AND formatted value as TEXT (never color-only)', () => {
    render(<BarChart items={ITEMS} dimension="model" metric="cost" />);
    // The category key is readable text…
    expect(screen.getByText('claude-opus')).toBeInTheDocument();
    expect(screen.getByText('gpt-5')).toBeInTheDocument();
    // …and the value is formatted per metric (cost -> USD) as readable text.
    expect(screen.getByText('$5.00')).toBeInTheDocument();
    expect(screen.getByText('$2.00')).toBeInTheDocument();
  });

  it('formats values per the calls metric as integers', () => {
    render(
      <BarChart
        items={[{ key: 'shell.Bash', value: 1234 }]}
        dimension="tool"
        metric="calls"
      />,
    );
    expect(screen.getByText('1,234')).toBeInTheDocument();
  });

  it('renders one <rect> bar per item', () => {
    const { container } = render(<BarChart items={ITEMS} dimension="model" metric="cost" />);
    const bars = container.querySelectorAll('rect[data-bar]');
    expect(bars).toHaveLength(2);
  });

  it('has no axe violations with data', async () => {
    const { container } = render(<BarChart items={ITEMS} dimension="model" metric="cost" />);
    expect(await axe(container)).toHaveNoViolations();
  });

  it('has no axe violations when empty', async () => {
    const { container } = render(<BarChart items={[]} dimension="model" metric="cost" />);
    expect(await axe(container)).toHaveNoViolations();
  });
});
