import styles from './LoadMore.module.css';

// Keyset "Load more" control (ui-pages.md §Phase-1 Implemented Behavior). Renders
// nothing when there is no next page; while a fetch is in flight it disables and
// shows a busy label. Presentational — the caller owns the page state (TanStack
// useInfiniteQuery's hasNextPage / isFetchingNextPage / fetchNextPage).

export interface LoadMoreProps {
  hasNextPage: boolean;
  isFetching: boolean;
  onLoadMore: () => void;
  label?: string;
}

export function LoadMore({
  hasNextPage,
  isFetching,
  onLoadMore,
  label = 'Load more',
}: LoadMoreProps) {
  if (!hasNextPage) {
    return null;
  }
  return (
    <div className={styles.wrap}>
      <button
        type="button"
        className={styles.button}
        disabled={isFetching}
        onClick={onLoadMore}
      >
        {isFetching ? 'Loading…' : label}
      </button>
    </div>
  );
}
