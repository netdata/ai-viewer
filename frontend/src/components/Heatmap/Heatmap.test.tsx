import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Heatmap } from './Heatmap';

describe('Heatmap', () => {
  it('renders a 7×24 grid of cells', () => {
    render(<Heatmap counts={{}} />);
    // 7 day labels visible
    expect(screen.getByText('Sun')).toBeInTheDocument();
    expect(screen.getByText('Mon')).toBeInTheDocument();
    expect(screen.getByText('Tue')).toBeInTheDocument();
    expect(screen.getByText('Wed')).toBeInTheDocument();
    expect(screen.getByText('Thu')).toBeInTheDocument();
    expect(screen.getByText('Fri')).toBeInTheDocument();
    expect(screen.getByText('Sat')).toBeInTheDocument();
    // Hour labels show at every 6th hour
    expect(screen.getByText('00')).toBeInTheDocument();
    expect(screen.getByText('06')).toBeInTheDocument();
    expect(screen.getByText('12')).toBeInTheDocument();
    expect(screen.getByText('18')).toBeInTheDocument();
  });

  it('renders cells with bg-status-failed tone when count > 0 (failed tone)', () => {
    const { container } = render(<Heatmap counts={{ '3:14': 5 }} />);
    // Find the cell at Wed 14:00 — should have bg-status-failed
    const cell = container.querySelector('[title*="Wed 14:00"]') as HTMLElement | null;
    expect(cell).not.toBeNull();
    expect(cell?.className).toContain('bg-status-failed');
  });

  it('renders cells with bg-muted/30 when count is 0', () => {
    const { container } = render(<Heatmap counts={{ '0:0': 0 }} />);
    const cell = container.querySelector('[title*="Sun 00:00"]') as HTMLElement | null;
    expect(cell).not.toBeNull();
    expect(cell?.className).toContain('bg-muted/30');
  });

  it('uses bg-status-completed when tone="completed"', () => {
    const { container } = render(<Heatmap counts={{ '1:9': 1 }} tone="completed" />);
    const cell = container.querySelector('[title*="Mon 09:00"]') as HTMLElement | null;
    expect(cell?.className).toContain('bg-status-completed');
  });

  it('includes the count in the title (a11y)', () => {
    const { container } = render(<Heatmap counts={{ '2:8': 7 }} />);
    const cell = container.querySelector('[title*="Tue 08:00"]') as HTMLElement | null;
    expect(cell?.getAttribute('title')).toMatch(/7 events/);
  });

  it('does not show hour labels when showHourLabels is false', () => {
    render(<Heatmap counts={{}} showHourLabels={false} />);
    expect(screen.queryByText('00')).not.toBeInTheDocument();
  });
});
