import type { ConnectionStatus } from '../../api/sse';
import { cn } from '../../lib/utils';

// Live indicator: a small pulsing dot reflecting the SSE connection state
// (SOW-0018). Connected = steady green pulse; connecting/reconnecting =
// amber; closed = red. The dot is never the only signal — an accessible
// text label reads the state (ui-pages.md §Accessibility: "color is never
// the only signal").
//
// `compact` (SOW-0073) hides the text label so the indicator can sit in a
// tighter chrome context (e.g. the new topbar). The status is still
// announced via aria-label.

const STATUS_LABEL: Record<ConnectionStatus, string> = {
  open: 'Live',
  connecting: 'Connecting…',
  reconnecting: 'Reconnecting…',
  closed: 'Disconnected',
};

const DOT_CLASS: Record<ConnectionStatus, string> = {
  open: 'bg-status-completed',
  connecting: 'bg-status-running',
  reconnecting: 'bg-status-running',
  closed: 'bg-status-failed',
};

const DOT_ANIM: Record<ConnectionStatus, string> = {
  open: 'animate-[pulse-live_2s_ease-in-out_infinite]',
  connecting: 'animate-[pulse-live_1s_ease-in-out_infinite]',
  reconnecting: 'animate-[pulse-live_0.5s_ease-in-out_infinite]',
  closed: '',
};

export function LiveIndicator({
  status,
  compact = false,
}: {
  status: ConnectionStatus;
  compact?: boolean;
}) {
  return (
    <div
      className={cn(
        'inline-flex items-center gap-1.5 text-xs text-muted-foreground',
      )}
      role="status"
      aria-label={`SSE status: ${STATUS_LABEL[status]}`}
    >
      <span
        className={cn(
          'inline-block size-2 shrink-0 rounded-full',
          DOT_CLASS[status],
          DOT_ANIM[status],
        )}
        aria-hidden
      />
      {!compact ? <span className="whitespace-nowrap">{STATUS_LABEL[status]}</span> : null}
    </div>
  );
}

// Keyframes for the dot pulse. Inlined once at module load so it works
// without touching app.css.
if (
  typeof document !== 'undefined' &&
  !document.head.querySelector('style[data-live-indicator-keyframes]')
) {
  const el = document.createElement('style');
  el.setAttribute('data-live-indicator-keyframes', '');
  el.textContent = `
@keyframes pulse-live {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}
@media (prefers-reduced-motion: reduce) {
  .animate-\\[pulse-live_2s_ease-in-out_infinite\\],
  .animate-\\[pulse-live_1s_ease-in-out_infinite\\],
  .animate-\\[pulse-live_0\\.5s_ease-in-out_infinite\\] {
    animation: none !important;
  }
}`;
  document.head.appendChild(el);
}