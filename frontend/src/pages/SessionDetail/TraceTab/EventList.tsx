import { useRef, useState } from 'react';
import type { UIEvent } from 'react';
import type { TraceNode } from '../../../viz/trace';
import { isInstantOp, windowRange } from '../../../viz/trace';
import { colorForAgent, colorForOpKind } from '../../../viz/color';
import { formatDuration, formatTimestamp } from '../../../lib/format';
import styles from './TraceTab.module.css';

// The always-available event list (ui-pages.md §/sessions/:id #3 "Event list"):
// a scrollable, virtualized list of every op in pre-order (ts, kind, name,
// duration, status), click-to-detail. Windowing is a simple uniform-height
// scheme (viz/trace.windowRange): only the visible slice (+ overscan) is
// mounted, with top/bottom spacer rows preserving the scrollbar geometry, so a
// 10k-op session stays responsive.

const ROW_HEIGHT = 28;
const OVERSCAN = 8;
const VIEWPORT_HEIGHT = 420;

export interface EventListProps {
  nodes: TraceNode[];
  onSelect: (node: TraceNode) => void;
  selectedId: string | null;
}

export function EventList({ nodes, onSelect, selectedId }: EventListProps) {
  const [scrollTop, setScrollTop] = useState(0);
  const scrollerRef = useRef<HTMLDivElement>(null);

  const { start, end } = windowRange(
    nodes.length,
    ROW_HEIGHT,
    scrollTop,
    VIEWPORT_HEIGHT,
    OVERSCAN,
  );
  const slice = nodes.slice(start, end);
  const topPad = start * ROW_HEIGHT;
  const bottomPad = Math.max(0, (nodes.length - end) * ROW_HEIGHT);

  const onScroll = (e: UIEvent<HTMLDivElement>): void => {
    setScrollTop(e.currentTarget.scrollTop);
  };

  return (
    <div
      ref={scrollerRef}
      className={styles.eventScroller}
      style={{ maxHeight: VIEWPORT_HEIGHT }}
      onScroll={onScroll}
      tabIndex={0}
      role="region"
      aria-label="Event list scroll area"
    >
      <table className={styles.eventTable} aria-label="Event list">
        <thead>
          <tr>
            <th className={styles.colTime}>Time</th>
            <th className={styles.colKind}>Kind</th>
            <th className={styles.colName}>Name</th>
            <th className={styles.colAgent}>Sub-agent</th>
            <th className={styles.colNum}>Duration</th>
            <th className={styles.colStatus}>Status</th>
          </tr>
        </thead>
        <tbody>
          {topPad > 0 ? (
            <tr aria-hidden="true" style={{ height: topPad }}>
              <td colSpan={6} />
            </tr>
          ) : null}
          {slice.map((node) => {
            const { op } = node;
            const failed = op.error_class !== null;
            const agent = node.sessionAgent ?? '';
            return (
              <tr
                key={op.id}
                className={op.id === selectedId ? styles.eventRowSelected : undefined}
                style={{ height: ROW_HEIGHT }}
              >
                <td className={styles.colTime}>{formatTimestamp(op.start_ts)}</td>
                <td className={styles.colKind}>
                  <span
                    className={styles.kindDot}
                    style={{ background: colorForOpKind(op.kind) }}
                    aria-hidden="true"
                  />
                  {op.kind}
                </td>
                <td className={styles.colName}>
                  <button
                    type="button"
                    className={styles.eventNameButton}
                    onClick={() => {
                      onSelect(node);
                    }}
                  >
                    {op.name || op.id}
                  </button>
                </td>
                {/* Sub-agent indicator (SOW-0070): a colored swatch + the agent
                    name, keyed by the owning session's agent. Empty for the
                    single-session trace (no sessionAgent on the node). */}
                <td className={styles.colAgent}>
                  {agent ? (
                    <>
                      <span
                        className={styles.agentDot}
                        style={{ background: colorForAgent(agent) }}
                        aria-hidden="true"
                      />
                      {agent}
                    </>
                  ) : (
                    '—'
                  )}
                </td>
                {/* Point-event ops (no measured span) show an em-dash, not
                    "0µs" — the source recorded no call duration (ui-pages.md
                    §Trace, P2#3). Measured ops keep their real duration. */}
                <td className={styles.colNum}>
                  {isInstantOp(op) ? '—' : formatDuration(op.duration_us)}
                </td>
                <td className={failed ? styles.statusFailed : styles.colStatus}>{op.status}</td>
              </tr>
            );
          })}
          {bottomPad > 0 ? (
            <tr aria-hidden="true" style={{ height: bottomPad }}>
              <td colSpan={6} />
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  );
}
