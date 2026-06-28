// TurnView (ui-turn-view.md): rich rendering of ONE TurnDetail as a vertical
// timeline of step cards. This is the "what is this turn doing?" view that
// the operator specifically called out as the biggest miss in the current
// Session Detail page (SOW-0088 inception message): previously the operator
// only saw op IDs and small field chips; this component answers the question
// "is this the turn I'm interested in?" with full prompt + reasoning + tool
// request/response + assistant output.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Clipboard } from 'lucide-react';
import type { TurnDetail } from '../../api/types';
import { TurnStep } from './TurnStep';
import { StepFilter, type StepKindFilter, matchStepFilter } from './StepFilter';
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
  initialStepKindFilter,
  onStepKindFilterChange,
}: {
  turn: TurnDetail;
  /** When set, scrolls the matching op into view + pulses it. */
  focusOpId?: string;
  /** Initial filter value (typically from the URL `?stepKindFilter=`).
   *  Defaults to 'all' when omitted. */
  initialStepKindFilter?: StepKindFilter;
  /** Optional change callback so the parent can write the new filter back
   *  to the URL. If omitted, the filter is purely local state. */
  onStepKindFilterChange?: (next: StepKindFilter) => void;
}) {
  const [activeFilter, setActiveFilter] = useState<StepKindFilter>(initialStepKindFilter ?? 'all');

  const handleFilterChange = useCallback(
    (next: StepKindFilter): void => {
      setActiveFilter(next);
      onStepKindFilterChange?.(next);
    },
    [onStepKindFilterChange],
  );

  // Counts per kind so the filter pills can render 'Tool (4)' etc. Recomputed
  // when the turn changes; cheap (≤ ~50 ops per turn in practice).
  const counts = useMemo<Record<StepKindFilter, number>>(() => {
    const inc = (r: Record<StepKindFilter, number>, k: StepKindFilter): void => {
      r[k] += 1;
    };
    const result: Record<StepKindFilter, number> = {
      all: turn.ops.length,
      user: 0,
      reasoning: 0,
      assistant: 0,
      tool: 0,
      session: 0,
      compaction: 0,
      internal: 0,
      llm: 0,
      generic: 0,
      system: 0,
    };
    for (const op of turn.ops) {
      if (op.kind === 'internal' && op.name === 'user_input') {
        inc(result, 'user');
        inc(result, 'internal');
      } else if (op.kind === 'reasoning') {
        inc(result, 'reasoning');
      } else if (op.kind === 'llm' && op.name === 'message') {
        inc(result, 'assistant');
        inc(result, 'llm');
      } else if (op.kind === 'tool') {
        inc(result, 'tool');
      } else if (op.kind === 'session') {
        inc(result, 'session');
      } else if (op.kind === 'compaction') {
        inc(result, 'compaction');
      } else if (op.kind === 'system') {
        inc(result, 'system');
      } else {
        inc(result, 'generic');
      }
    }
    return result;
  }, [turn.ops]);

  // Filtered list. The step-index labels stay tied to the ORIGINAL
  // position in the turn (1-based + total), not the filtered position, so
  // 'step 4/31' is a stable reference even after toggling the filter.
  const visibleOps = useMemo(
    () =>
      turn.ops.filter((op) => matchStepFilter(activeFilter, op.kind, op.name)),
    [turn.ops, activeFilter],
  );

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

      <StepFilter active={activeFilter} counts={counts} onChange={handleFilterChange} />

      {visibleOps.length === 0 ? (
        <p className={styles.emptyFilter} role="status">
          No steps of this kind in this turn.
        </p>
      ) : (
        <ol className={styles.steps}>
          {turn.ops.map((op) => {
            if (!matchStepFilter(activeFilter, op.kind, op.name)) return null;
            const originalIndex = turn.ops.indexOf(op);
            return (
              <li key={op.id} className={styles.stepLi}>
                <TurnStep
                  op={op}
                  focused={focusOpId === op.id}
                  turnStartTs={turn.start_ts}
                  stepIndex={originalIndex + 1}
                  stepTotal={turn.ops.length}
                />
              </li>
            );
          })}
        </ol>
      )}
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
  const copiedResetTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (copiedResetTimer.current !== null) {
        clearTimeout(copiedResetTimer.current);
      }
    };
  }, []);

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
      if (copiedResetTimer.current !== null) {
        clearTimeout(copiedResetTimer.current);
      }
      setCopied(true);
      copiedResetTimer.current = setTimeout(() => {
        copiedResetTimer.current = null;
        setCopied(false);
      }, 1500);
    } catch {
      // Best-effort copy; no silent failure.
      if (copiedResetTimer.current !== null) {
        clearTimeout(copiedResetTimer.current);
        copiedResetTimer.current = null;
      }
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
