import { Clock } from 'lucide-react';
import { formatDuration } from '../../lib/format';
import { cn } from '../../lib/utils';

// StaleBadge — SOW-0087 chunk 5 (A10).
//
// Renders a small inline badge for sessions with status=running
// whose `last_activity_ts` is older than STALE_THRESHOLD_US ago.
// "Stale" means the session's daemon hasn't produced an op in a
// while — the agent is probably hung or the parent process died.
// Surfacing this prevents the operator from mistaking a frozen
// session for an active one.

const STALE_THRESHOLD_US = 10 * 60 * 1_000_000; // 10 minutes
// 60s grace window so a session that JUST produced an op doesn't
// immediately flash "stale" on the next render tick.
const STALE_GRACE_US = 60 * 1_000_000;

export function shouldMarkStale(
  status: string,
  lastActivityUs: number | null | undefined,
  nowUs: number,
): boolean {
  if (status !== 'running') return false;
  if (lastActivityUs === null || lastActivityUs === undefined) return false;
  if (nowUs - lastActivityUs >= STALE_THRESHOLD_US + STALE_GRACE_US) return true;
  return false;
}

export function StaleBadge({
  status,
  lastActivityTs,
  nowUs,
}: {
  status: string;
  lastActivityTs: number | null | undefined;
  nowUs: number;
}) {
  if (!shouldMarkStale(status, lastActivityTs, nowUs)) return null;

  const ref = nowUs;
  const last = lastActivityTs ?? 0;
  const idleFor = ref - last;

  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-md bg-status-running/10 px-1.5 py-0.5',
        'text-[11px] font-medium text-status-running',
      )}
      role="status"
      aria-label={`Stale session — no activity for ${formatDuration(idleFor)}`}
      title={`No activity for ${formatDuration(idleFor)} — session may be hung`}
    >
      <Clock className="size-3" aria-hidden />
      stale · {formatDuration(idleFor)}
    </span>
  );
}
