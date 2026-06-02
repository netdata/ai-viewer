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

  it('keeps the longest bar value label inside the viewport (no right-edge clip)', () => {
    // VIEW_WIDTH=720, PADDING.right=16 → the right viewport edge is 704. The
    // longest bar fills the band so its end sits at the edge; a start-anchored
    // outside label would overflow. The fix flips it to an end-anchored label
    // drawn inside the bar, whose effective right edge (x, since text-anchor:end)
    // must be ≤ 704 so the text box stays within the SVG.
    const VIEW_RIGHT = 720 - 16;
    const { container } = render(<BarChart items={ITEMS} dimension="model" metric="cost" />);
    const longest = container.querySelector('text[data-value="claude-opus"]');
    expect(longest).not.toBeNull();
    expect(longest?.getAttribute('data-inside')).toBe('true');
    expect(longest?.getAttribute('text-anchor')).toBe('end');
    const x = Number(longest?.getAttribute('x'));
    expect(Number.isFinite(x)).toBe(true);
    // End-anchored: the text grows LEFT from x, so x is its right edge.
    expect(x).toBeLessThanOrEqual(VIEW_RIGHT);
    expect(x).toBeGreaterThanOrEqual(0);
  });

  it('places a short bar value label outside the bar end (start-anchored, in viewport)', () => {
    // A short bar leaves room to the right, so its label is start-anchored just
    // after the bar end and must still begin within the viewport.
    const VIEW_RIGHT = 720 - 16;
    const { container } = render(
      <BarChart items={[{ key: 'tiny', value: 1 }, { key: 'huge', value: 1000 }]} dimension="model" metric="calls" />,
    );
    const shortLabel = container.querySelector('text[data-value="tiny"]');
    expect(shortLabel?.getAttribute('data-inside')).toBe('false');
    expect(shortLabel?.getAttribute('text-anchor')).toBe('start');
    const x = Number(shortLabel?.getAttribute('x'));
    // Start-anchored: x is the LEFT edge; it must sit within the viewport.
    expect(x).toBeLessThanOrEqual(VIEW_RIGHT);
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
