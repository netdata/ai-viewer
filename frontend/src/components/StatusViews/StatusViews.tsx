import type { ReactNode } from 'react';
import { ApiError } from '../../api/client';
import styles from './StatusViews.module.css';

// Small status primitives shared by every page: a loading placeholder, an error
// panel, and an empty-result panel (ui-pages.md §Phase-1 Implemented Behavior).
// ErrorState surfaces the ApiError message — never a silent failure (AGENTS.md).

/** LoadingState is the in-flight placeholder. role=status so SRs announce it. */
export function LoadingState({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className={styles.loading} role="status" aria-live="polite">
      {label}
    </div>
  );
}

/** errorMessage extracts a human string from an unknown query error. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  if (err instanceof Error && err.message) {
    return err.message;
  }
  return 'Something went wrong.';
}

/**
 * ErrorState renders a query error. The ApiError.message (decoded from the
 * server's error envelope) is shown verbatim so the operator sees the real
 * cause; an optional title frames it.
 */
export function ErrorState({
  error,
  title = 'Failed to load',
}: {
  error: unknown;
  title?: string;
}) {
  return (
    <div className={styles.error} role="alert">
      <strong className={styles.errorTitle}>{title}</strong>
      <span className={styles.errorMessage}>{errorMessage(error)}</span>
    </div>
  );
}

/** EmptyState renders when a successful query returns no rows. */
export function EmptyState({ children }: { children?: ReactNode }) {
  return (
    <div className={styles.empty} role="status">
      {children ?? 'Nothing to show.'}
    </div>
  );
}
