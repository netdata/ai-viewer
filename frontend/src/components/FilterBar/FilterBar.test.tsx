import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { FilterBar } from './FilterBar';

// FilterBar reads its values from the URL and writes changes straight back —
// there is no internal state. A LocationProbe surfaces the current search
// string so assertions can verify the URL round-trip.

function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="loc">{loc.search}</div>;
}

function renderBar(initial: string) {
  return render(
    <MemoryRouter initialEntries={[initial]}>
      <FilterBar />
      <LocationProbe />
    </MemoryRouter>,
  );
}

describe('FilterBar', () => {
  it('renders current filters from the URL', () => {
    renderBar('/?q=nedi&agents=a,b&status=failed');
    expect(screen.getByPlaceholderText('agent name…')).toHaveValue('nedi');
    expect(screen.getByLabelText('Agents filter')).toHaveValue('a,b');
    // The "failed" checkbox is checked.
    const failed = screen.getByRole('checkbox', { name: 'failed' });
    expect(failed).toBeChecked();
    const running = screen.getByRole('checkbox', { name: 'running' });
    expect(running).not.toBeChecked();
  });

  it('typing in search updates the q param', async () => {
    const user = userEvent.setup();
    renderBar('/');
    await user.type(screen.getByPlaceholderText('agent name…'), 'abc');
    expect(screen.getByTestId('loc')).toHaveTextContent('q=abc');
  });

  it('checking a status box adds it to the URL', async () => {
    const user = userEvent.setup();
    renderBar('/');
    await user.click(screen.getByRole('checkbox', { name: 'completed' }));
    expect(screen.getByTestId('loc')).toHaveTextContent('status=completed');
  });

  it('unchecking a status box removes it', async () => {
    const user = userEvent.setup();
    renderBar('/?status=completed,failed');
    await user.click(screen.getByRole('checkbox', { name: 'completed' }));
    const loc = screen.getByTestId('loc');
    expect(loc).toHaveTextContent('status=failed');
    expect(loc.textContent).not.toContain('completed');
  });

  it('editing the agents input writes a comma-joined param', async () => {
    const user = userEvent.setup();
    renderBar('/');
    await user.type(screen.getByLabelText('Models filter'), 'm1');
    expect(screen.getByTestId('loc')).toHaveTextContent('models=m1');
  });

  it('editing the tools and sources inputs writes their params', async () => {
    const user = userEvent.setup();
    renderBar('/');
    await user.type(screen.getByLabelText('Tools filter'), 'tool1');
    expect(screen.getByTestId('loc')).toHaveTextContent('tools=tool1');
    await user.type(screen.getByLabelText('Sources filter'), 'src1');
    expect(screen.getByTestId('loc')).toHaveTextContent('sources=src1');
  });

  it('clearing the search box removes the q param', async () => {
    const user = userEvent.setup();
    renderBar('/?q=initial');
    const search = screen.getByPlaceholderText('agent name…');
    await user.clear(search);
    expect(screen.getByTestId('loc').textContent).not.toContain('q=');
  });

  it('Clear filters removes all filter params', async () => {
    const user = userEvent.setup();
    renderBar('/?q=x&agents=a&status=failed');
    await user.click(screen.getByRole('button', { name: 'Clear filters' }));
    const loc = screen.getByTestId('loc');
    expect(loc.textContent).not.toContain('q=');
    expect(loc.textContent).not.toContain('agents=');
    expect(loc.textContent).not.toContain('status=');
  });
});
