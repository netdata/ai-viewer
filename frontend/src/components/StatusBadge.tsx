import { Loader2, CheckCircle2, AlertTriangle, CircleDashed, Pause } from 'lucide-react';
import type { SessionStatus } from '../api/types';
import { cn } from '../lib/utils';

// StatusBadge — semantic visual representation of a session's status.
// Replaces the old "running" text-pill that used the same amber color for
// every state. Each status gets its own color (mapped to the design system
// semantic tokens), icon, and accessible label/tooltip explaining what it
// means. The pulsing dot on `running` is a CSS animation that respects
// `prefers-reduced-motion`.

const STATUS_LABEL: Record<SessionStatus, string> = {
  running: 'Running',
  completed: 'Completed',
  failed: 'Failed',
  abandoned: 'Abandoned',
  interrupted: 'Interrupted',
};

const STATUS_DESCRIPTION: Record<SessionStatus, string> = {
  running: 'This session is still active.',
  completed: 'This session finished without error.',
  failed: 'This session ended with at least one failure.',
  abandoned: 'This session was closed without a clean finish.',
  interrupted: 'This session was cut off (e.g. process killed).',
};

// Maps each status to its --status-* token and its matching lucide icon.
const STATUS_META: Record<
  SessionStatus,
  { token: 'completed' | 'running' | 'failed' | 'abandoned' | 'interrupted'; Icon: typeof Loader2 }
> = {
  completed: { token: 'completed', Icon: CheckCircle2 },
  running: { token: 'running', Icon: Loader2 },
  failed: { token: 'failed', Icon: AlertTriangle },
  abandoned: { token: 'abandoned', Icon: CircleDashed },
  interrupted: { token: 'interrupted', Icon: Pause },
};

export function StatusBadge({
  status,
  className,
  showIcon = true,
}: {
  status: SessionStatus;
  className?: string;
  showIcon?: boolean;
}) {
  const meta = STATUS_META[status];
  const token = meta?.token ?? 'abandoned';
  const Icon = meta?.Icon ?? CircleDashed;
  const isRunning = status === 'running';

  return (
    <span
      data-testid={`status-badge-${status}`}
      title={STATUS_DESCRIPTION[status]}
      aria-label={`${STATUS_LABEL[status]} — ${STATUS_DESCRIPTION[status]}`}
      // The bg/border/fg use color-mix(in oklch, ...) inline so the chip stays
      // in the same hue family across light + dark without per-theme overrides.
      // pulse runs on the wrapper; reduced-motion media query disables it.
      style={
        {
          '--badge-fg': `var(--status-${token})`,
          '--badge-bg': `color-mix(in oklch, var(--status-${token}) 14%, transparent)`,
          '--badge-border': `color-mix(in oklch, var(--status-${token}) 30%, transparent)`,
        } as React.CSSProperties
      }
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium tabular-nums',
        isRunning && 'animate-pulse-subtle',
        className,
      )}
    >
      {showIcon ? (
        <Icon
          aria-hidden
          className={cn('size-3 shrink-0', isRunning && 'animate-spin-slow')}
          style={{ color: 'var(--badge-fg)' }}
        />
      ) : null}
      <span className="truncate" style={{ color: 'var(--badge-fg)' }}>
        {STATUS_LABEL[status]}
      </span>
    </span>
  );
}

// Lightweight keyframes + spin/pulse helpers. Inlined into app.css once at
// module load so they're available everywhere without bundler CSS import.
if (typeof document !== 'undefined' && !document.head.querySelector('style[data-status-badge-keyframes]')) {
  const el = document.createElement('style');
  el.setAttribute('data-status-badge-keyframes', '');
  el.textContent = `
@keyframes pulse-subtle {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.7; }
}
.animate-pulse-subtle { animation: pulse-subtle 2s ease-in-out infinite; }
@keyframes spin-slow {
  to { transform: rotate(360deg); }
}
.animate-spin-slow { animation: spin-slow 3s linear infinite; }
@media (prefers-reduced-motion: reduce) {
  .animate-pulse-subtle,
  .animate-spin-slow { animation: none !important; }
}`;
  document.head.appendChild(el);
}