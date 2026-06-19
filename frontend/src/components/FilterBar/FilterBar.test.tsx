import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { FilterBar } from './FilterBar';

// Mock useSources so FilterBar tests don't need a live API.
vi.mock('../../api/sources', () => ({
  useSources: () => ({
    data: {
      items: [
        { id: 'src1', format: 'aiagent_v3', location: '/tmp', enabled: true, parse_errors: 0, last_seq: 0 },
        { id: 'src2', format: 'codex', location: '/tmp', enabled: true, parse_errors: 0, last_seq: 0 },
      ],
    },
  }),
}));

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false } },
});

// FilterBar reads its values from the URL and writes changes straight back —
// there is no internal state. A LocationProbe surfaces the current search
// string so assertions can verify the URL round-trip.

function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="loc">{loc.search}</div>;
}

function renderBar(initial: string) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initial]}>
        <FilterBar />
        <LocationProbe />
      </MemoryRouter>
    </QueryClientProvider>,
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

  it('editing the tools input writes its param', async () => {
    const user = userEvent.setup();
    renderBar('/');
    await user.type(screen.getByLabelText('Tools filter'), 'tool1');
    expect(screen.getByTestId('loc')).toHaveTextContent('tools=tool1');
  });

  it('clicking a source chip toggles the sources param', async () => {
    const user = userEvent.setup();
    renderBar('/');
    const chip = screen.getByText('aiagent_v3');
    await user.click(chip);
    expect(screen.getByTestId('loc').textContent).toContain('sources=');
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
    renderBar('/?q=x&agents=a&status=failed&range=7d&from=123');
    await user.click(screen.getByRole('button', { name: 'Clear filters' }));
    const loc = screen.getByTestId('loc').textContent ?? '';
    expect(loc).not.toContain('q=');
    expect(loc).not.toContain('agents=');
    expect(loc).not.toContain('status=');
    expect(loc).not.toContain('from=');
    // The preset mirror must also be cleared so the select does not display a
    // preset that no longer applies (SOW-0067).
    expect(loc).not.toContain('range=');
  });

  it('selecting a time-range preset writes a from bound + range mirror (SOW-0067)', async () => {
    const user = userEvent.setup();
    renderBar('/');
    await user.selectOptions(screen.getByLabelText(/time range preset/i), '7d');
    const loc = screen.getByTestId('loc').textContent ?? '';
    expect(loc).toContain('range=7d');
    expect(loc).toContain('from=');
    // 'to' stays open so live data keeps flowing.
    expect(loc).not.toContain('to=');
  });

  it('All time clears the from bound and the range mirror', async () => {
    const user = userEvent.setup();
    renderBar('/?range=7d&from=123');
    await user.selectOptions(screen.getByLabelText(/time range preset/i), 'all');
    const loc = screen.getByTestId('loc').textContent ?? '';
    expect(loc).not.toContain('from=');
    expect(loc).not.toContain('range=');
  });
});
