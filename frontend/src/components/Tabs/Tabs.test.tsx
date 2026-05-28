import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Tabs, type TabSpec } from './Tabs';

// Tabs is a controlled, accessible tablist. It marks the active tab
// aria-selected, calls onSelect with the clicked/keyboard-selected key, and
// implements the WAI-ARIA tabs pattern (roving tabindex + arrow/Home/End).

const TABS = [
  { key: 'overview', label: 'Overview' },
  { key: 'logs', label: 'Logs' },
] as const;

const THREE: ReadonlyArray<TabSpec<'a' | 'b' | 'c'>> = [
  { key: 'a', label: 'A' },
  { key: 'b', label: 'B' },
  { key: 'c', label: 'C' },
];

/** Controlled harness so keyboard navigation re-renders with the new active. */
function Harness({ initial = 'a' }: { initial?: 'a' | 'b' | 'c' }) {
  const [active, setActive] = useState<'a' | 'b' | 'c'>(initial);
  return <Tabs tabs={THREE} active={active} onSelect={setActive} ariaLabel="Views" />;
}

describe('Tabs', () => {
  it('renders a tablist with one tab per spec', () => {
    render(<Tabs tabs={TABS} active="overview" onSelect={() => undefined} ariaLabel="Views" />);
    const list = screen.getByRole('tablist', { name: 'Views' });
    expect(list).toBeInTheDocument();
    expect(screen.getAllByRole('tab')).toHaveLength(2);
  });

  it('marks the active tab aria-selected', () => {
    render(<Tabs tabs={TABS} active="logs" onSelect={() => undefined} ariaLabel="Views" />);
    expect(screen.getByRole('tab', { name: 'Logs' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'false',
    );
  });

  it('wires aria-controls to the panel id', () => {
    render(<Tabs tabs={TABS} active="overview" onSelect={() => undefined} ariaLabel="Views" />);
    expect(screen.getByRole('tab', { name: 'Logs' })).toHaveAttribute(
      'aria-controls',
      'tabpanel-logs',
    );
  });

  it('calls onSelect with the clicked key', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Tabs tabs={TABS} active="overview" onSelect={onSelect} ariaLabel="Views" />);
    await user.click(screen.getByRole('tab', { name: 'Logs' }));
    expect(onSelect).toHaveBeenCalledWith('logs');
  });

  it('applies roving tabindex (selected tab 0, others -1)', () => {
    render(<Tabs tabs={TABS} active="logs" onSelect={() => undefined} ariaLabel="Views" />);
    expect(screen.getByRole('tab', { name: 'Logs' })).toHaveAttribute('tabindex', '0');
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute('tabindex', '-1');
  });

  it('ArrowRight moves selection to the next tab and focuses it', async () => {
    const user = userEvent.setup();
    render(<Harness initial="a" />);
    screen.getByRole('tab', { name: 'A' }).focus();
    await user.keyboard('{ArrowRight}');
    const b = screen.getByRole('tab', { name: 'B' });
    expect(b).toHaveAttribute('aria-selected', 'true');
    expect(b).toHaveFocus();
  });

  it('ArrowLeft from the first tab wraps to the last', async () => {
    const user = userEvent.setup();
    render(<Harness initial="a" />);
    screen.getByRole('tab', { name: 'A' }).focus();
    await user.keyboard('{ArrowLeft}');
    expect(screen.getByRole('tab', { name: 'C' })).toHaveAttribute('aria-selected', 'true');
  });

  it('ArrowRight from the last tab wraps to the first', async () => {
    const user = userEvent.setup();
    render(<Harness initial="c" />);
    screen.getByRole('tab', { name: 'C' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(screen.getByRole('tab', { name: 'A' })).toHaveAttribute('aria-selected', 'true');
  });

  it('Home selects the first tab and End selects the last', async () => {
    const user = userEvent.setup();
    render(<Harness initial="b" />);
    screen.getByRole('tab', { name: 'B' }).focus();
    await user.keyboard('{End}');
    expect(screen.getByRole('tab', { name: 'C' })).toHaveAttribute('aria-selected', 'true');
    screen.getByRole('tab', { name: 'C' }).focus();
    await user.keyboard('{Home}');
    expect(screen.getByRole('tab', { name: 'A' })).toHaveAttribute('aria-selected', 'true');
  });

  it('ignores non-navigation keys (no selection change)', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Tabs tabs={TABS} active="overview" onSelect={onSelect} ariaLabel="Views" />);
    screen.getByRole('tab', { name: 'Overview' }).focus();
    await user.keyboard('{Enter}');
    await user.keyboard('a');
    // Only click/space activates via the native button; arrow handler ignores these.
    expect(onSelect).not.toHaveBeenCalledWith('logs');
  });

  it('ArrowRight is a no-op when the active key is not in the tabs', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    // active="missing" → activeIndex < 0 guard; arrow keys must not select.
    render(
      <Tabs
        tabs={THREE}
        active={'missing' as 'a' | 'b' | 'c'}
        onSelect={onSelect}
        ariaLabel="Views"
      />,
    );
    screen.getByRole('tablist').focus();
    await user.keyboard('{ArrowRight}');
    expect(onSelect).not.toHaveBeenCalled();
  });
});
