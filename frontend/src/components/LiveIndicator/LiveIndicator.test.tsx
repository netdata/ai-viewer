import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { LiveIndicator } from './LiveIndicator';
import type { ConnectionStatus } from '../../api/sse';

// LiveIndicator renders a colored dot + text label for each SSE connection
// status. The dot must never be the only signal (accessible aria-label).

function renderIndicator(status: ConnectionStatus) {
  return render(<LiveIndicator status={status} />);
}

describe('LiveIndicator', () => {
  it('renders the "Live" label + a dot for the open status', () => {
    renderIndicator('open');
    expect(screen.getByText('Live')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveAttribute(
      'aria-label',
      expect.stringContaining('Live'),
    );
  });

  it('renders "Connecting…" for the connecting status', () => {
    renderIndicator('connecting');
    expect(screen.getByText('Connecting…')).toBeInTheDocument();
  });

  it('renders "Reconnecting…" for the reconnecting status', () => {
    renderIndicator('reconnecting');
    expect(screen.getByText('Reconnecting…')).toBeInTheDocument();
  });

  it('renders "Disconnected" for the closed status', () => {
    renderIndicator('closed');
    expect(screen.getByText('Disconnected')).toBeInTheDocument();
  });

  it('renders a status role with an accessible label for every status', () => {
    const statuses: ConnectionStatus[] = ['open', 'connecting', 'reconnecting', 'closed'];
    for (const status of statuses) {
      const { unmount } = renderIndicator(status);
      const el = screen.getByRole('status');
      expect(el.getAttribute('aria-label')).toBeTruthy();
      unmount();
    }
  });
});
