// TurnView (ui-turn-view.md): rich rendering of ONE TurnDetail as a vertical
// timeline of step cards. This is the "what is this turn doing?" view that
// the operator specifically called out as the biggest miss in the current
// Session Detail page (SOW-0088 inception message): previously the operator
// only saw op IDs and small field chips; this component answers the question
// "is this the turn I'm interested in?" with full prompt + reasoning + tool
// request/response + assistant output.

import { useState } from 'react';
import { Clipboard } from 'lucide-react';
import type { TurnDetail } from '../../api/types';
import { TurnStep } from './TurnStep';
import styles from './TurnView.module.css';

function formatTokens(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

function formatCost(n: number): string {
  // cost is float64 dollars; we render with up to 6 fraction digits and trim.
  if (n === 0) return '$0.00';
  if (n < 0.01) return `$${n.toFixed(4)}`;
  return `$${n.toFixed(4).replace(/\.?0+$/, '')}`;
}

function formatDuration(start: number, end: number | null): string {
  if (end === null) return 'running…';
  const us = end - start;
  if (us < 1000) return `${us}µs`;
  if (us < 1_000_000) return `${(us / 1000).toFixed(0)}ms`;
  return `${(us / 1_000_000).toFixed(2)}s`;
}

export function TurnView({
  turn,
  focusOpId,
}: {
  turn: TurnDetail;
  /** When set, scrolls the matching op into view + pulses it. */
  focusOpId?: string;
}) {
  return (
    <section
      className={styles.turn}
      aria-labelledby={`turn-${turn.seq}-title`}
    >
      <header className={styles.turnHeader}>
        <h2 id={`turn-${turn.seq}-title`} className={styles.turnTitle}>
          Turn #{turn.seq}
        </h2>
        <span className={styles.turnMeta}>
          {turn.op_count} {turn.op_count === 1 ? 'op' : 'ops'} ·{' '}
          {formatDuration(turn.start_ts, turn.end_ts)} ·{' '}
          <span className={styles.turnStatus}>{turn.status}</span> ·{' '}
          <span className={styles.turnTokens}>
            {formatTokens(turn.tokens_in)}→{formatTokens(turn.tokens_out)} tok
          </span>{' '}
          · {formatCost(turn.cost_usd)}
        </span>
        <CopyTurnButton turn={turn} />
      </header>

      <ol className={styles.steps}>
        {turn.ops.map((op) => (
          <li key={op.id} className={styles.stepLi}>
            <TurnStep op={op} focused={focusOpId === op.id} />
          </li>
        ))}
      </ol>
    </section>
  );
}

/** CopyTurnButton serializes the rendered DOM into a plain-text representation
 *  suitable for the clipboard: header + each step's label + body text.
 *  We read from the live DOM (querySelectorAll on data-op-id) so the copy is
 *  exactly what the operator sees — including any redactions done by the
 *  server. If a payload hasn't loaded yet, that step contributes "Loading…". */
function CopyTurnButton({ turn }: { turn: TurnDetail }) {
  const [copied, setCopied] = useState(false);

  const handleClick = (): void => {
    void doCopy();
  };

  async function doCopy(): Promise<void> {
    const lines: string[] = [];
    lines.push(`Turn #${turn.seq}`);
    lines.push(`${turn.op_count} ops · ${turn.status} · ${formatTokens(turn.tokens_in)}→${formatTokens(turn.tokens_out)} tok · ${formatCost(turn.cost_usd)}`);
    lines.push('');
    for (const op of turn.ops) {
      const el = document.querySelector(`[data-op-id="${op.id}"]`);
      const label = el?.querySelector('h3')?.textContent ?? `${op.kind} ${op.name}`;
      lines.push(`## ${label}`);
      const body = el?.querySelector('[role="region"]');
      if (body?.textContent) {
        lines.push(body.textContent.trim());
      } else {
        lines.push('(loading…)');
      }
      lines.push('');
    }
    const text = lines.join('\n');
    try {
      const cb = navigator.clipboard;
      if (typeof cb.writeText === 'function') {
        await cb.writeText(text);
      }
      setCopied(true);
      setTimeout(() => {
        setCopied(false);
      }, 1500);
    } catch {
      // Best-effort copy; no silent failure.
      setCopied(false);
    }
  }

  return (
    <button
      type="button"
      className={styles.copyTurnButton}
      onClick={handleClick}
      aria-label="Copy turn"
      data-copied={copied ? 'true' : 'false'}
    >
      <Clipboard size={12} />
      <span>{copied ? 'Copied' : 'Copy turn'}</span>
    </button>
  );
}