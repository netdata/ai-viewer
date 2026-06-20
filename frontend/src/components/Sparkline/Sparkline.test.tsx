import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Sparkline, DurationBar } from './Sparkline';

describe('Sparkline', () => {
  it('renders an empty placeholder for empty values', () => {
    const { container } = render(<Sparkline values={[]} />);
    const svg = container.querySelector('svg');
    expect(svg).not.toBeNull();
    expect(container.querySelector('polyline')).toBeNull();
  });

  it('renders a polyline for non-empty values', () => {
    const { container } = render(<Sparkline values={[1, 2, 3, 4, 5]} />);
    const polyline = container.querySelector('polyline');
    expect(polyline).not.toBeNull();
    expect(polyline?.getAttribute('points')).toMatch(/0\.0,\d+\.\d+/);
  });

  it('renders a flat line for constant values (no division by zero)', () => {
    const { container } = render(<Sparkline values={[5, 5, 5]} />);
    const polyline = container.querySelector('polyline');
    expect(polyline).not.toBeNull();
    // All points should have the same Y coordinate.
    const points = polyline?.getAttribute('points') ?? '';
    const ys = points.split(/\s+/).map((p) => p.split(',')[1]);
    const uniqueYs = new Set(ys);
    expect(uniqueYs.size).toBe(1);
  });

  it('renders the area path when area=true (default)', () => {
    const { container } = render(<Sparkline values={[1, 2, 3]} />);
    expect(container.querySelector('path')).not.toBeNull();
  });

  it('does NOT render the area path when area=false', () => {
    const { container } = render(<Sparkline values={[1, 2, 3]} area={false} />);
    expect(container.querySelector('path')).toBeNull();
  });

  it('renders the success tone class', () => {
    const { container } = render(<Sparkline values={[1, 2, 3]} tone="success" />);
    const polyline = container.querySelector('polyline');
    expect(polyline?.getAttribute('class')).toContain('stroke-status-completed');
  });

  it('renders the failed tone class', () => {
    const { container } = render(<Sparkline values={[1, 2, 3]} tone="failed" />);
    const polyline = container.querySelector('polyline');
    expect(polyline?.getAttribute('class')).toContain('stroke-status-failed');
  });

  it('renders an accessible label with the number of points', () => {
    render(<Sparkline values={[1, 2, 3, 4]} />);
    expect(screen.getByRole('img', { name: /Sparkline, 4 points/i })).toBeInTheDocument();
  });
});

describe('DurationBar', () => {
  it('renders an inline-block span with an inner span sized to %', () => {
    const { container } = render(
      <DurationBar durationUs={50_000_000} maxDurationUs={100_000_000} />,
    );
    const inner = container.querySelector('span > span') as HTMLElement;
    expect(inner).not.toBeNull();
    expect(inner.style.width).toBe('50%');
  });

  it('clamps to 100% even when duration > max', () => {
    const { container } = render(
      <DurationBar durationUs={200_000_000} maxDurationUs={100_000_000} />,
    );
    const inner = container.querySelector('span > span') as HTMLElement;
    expect(inner.style.width).toBe('100%');
  });

  it('clamps to a minimum of 2% so very short sessions stay visible', () => {
    const { container } = render(
      <DurationBar durationUs={1_000} maxDurationUs={100_000_000} />,
    );
    const inner = container.querySelector('span > span') as HTMLElement;
    expect(inner.style.width).toBe('2%');
  });

  it('renders 0% width when maxDurationUs is 0 (no division by zero)', () => {
    const { container } = render(<DurationBar durationUs={5_000_000} maxDurationUs={0} />);
    const inner = container.querySelector('span > span') as HTMLElement;
    expect(inner.style.width).toBe('0%');
  });
});
