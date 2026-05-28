import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { LoadMore } from './LoadMore';

// LoadMore is the keyset pagination control. It renders nothing when there is no
// next page, calls onLoadMore on click, and disables + shows a busy label while
// a fetch is in flight.

describe('LoadMore', () => {
  it('renders nothing when there is no next page', () => {
    const { container } = render(
      <LoadMore hasNextPage={false} isFetching={false} onLoadMore={() => undefined} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it('renders an enabled button when a next page exists', () => {
    render(<LoadMore hasNextPage isFetching={false} onLoadMore={() => undefined} />);
    const btn = screen.getByRole('button', { name: 'Load more' });
    expect(btn).toBeEnabled();
  });

  it('calls onLoadMore when clicked', async () => {
    const user = userEvent.setup();
    const onLoadMore = vi.fn();
    render(<LoadMore hasNextPage isFetching={false} onLoadMore={onLoadMore} />);
    await user.click(screen.getByRole('button'));
    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });

  it('disables and shows a busy label while fetching', () => {
    render(<LoadMore hasNextPage isFetching onLoadMore={() => undefined} />);
    const btn = screen.getByRole('button');
    expect(btn).toBeDisabled();
    expect(btn).toHaveTextContent('Loading…');
  });

  it('honors a custom label', () => {
    render(
      <LoadMore hasNextPage isFetching={false} onLoadMore={() => undefined} label="More logs" />,
    );
    expect(screen.getByRole('button', { name: 'More logs' })).toBeInTheDocument();
  });
});
