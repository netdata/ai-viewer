import { useCallback, useEffect, useId, useRef } from 'react';
import type { KeyboardEvent, MouseEvent } from 'react';
import type { OpDetail, PayloadRef, TopologyNode } from '../../api/types';
import {
  formatBytes,
  formatCost,
  formatDuration,
  formatNumber,
  formatPct,
  formatTimestamp,
} from '../../lib/format';
import { isInstantOp } from '../../viz/trace';
import styles from './SpanDetailDrawer.module.css';

// Shared right-side span detail drawer (ui-pages.md §Span detail drawer). NOT a
// modal — the visualization behind stays visible (aria-modal="false"), but the
// drawer is still focus-trapped while open for keyboard a11y, closes on Esc /
// outside-click, and returns focus to the element that opened it.
//
// SOURCE-AWARE (SOW-0006 decision, from real-data review): the drawer NEVER
// fabricates unavailable fields as zero. It is opened from three views with three
// different amounts of data, so the prop is a discriminated `SpanDetail`:
//   - 'op'   (Trace tab)    → the full op row + its payload_refs. The Trace tab is
//     the only source that carries op metrics (model/tokens/cost/context/payloads).
//   - 'span' (Timeline tab) → only the lane/span fields (kind/name/status/start/
//     end/derived duration). A timeline span has NO op metrics, so the drawer
//     shows none — and directs the operator to the Trace tab — rather than
//     printing "$0.00 / 0 tokens / No payloads" as if they were measured.
//   - 'node' (per-session Topology tab) → an aggregate ACTOR (agent/tool), not an
//     op: its kind/label, failure %, and the value of the currently-selected size
//     metric. Op-only fields are omitted, not zeroed.
//
// The payload byte-preview route (GET /api/payloads/:ref) is deferred to SOW-0033
// (it reads source-file byte ranges, a security surface); until it lands the
// 'op' drawer shows each ref's metadata and a DISABLED "preview coming soon"
// control, structured so the byte-preview wires in trivially later.

/**
 * SpanDetail is the source-aware drawer payload. The discriminant `kind` selects
 * which fields are real for the view that opened the drawer; the drawer renders
 * only those, so an unavailable field is never shown as a fabricated zero.
 */
export type SpanDetail =
  | { kind: 'op'; op: OpDetail }
  | {
      kind: 'span';
      span: {
        id: string;
        kind: string;
        name: string;
        start_ts: number;
        end_ts: number | null;
        duration_us: number | null;
        status: string;
      };
    }
  | { kind: 'node'; node: TopologyNode; metricLabel: string; metricValue: number };

/**
 * formatNodeMetric renders a topology node's size-metric value honestly for its
 * metric: cost → USD, duration → µs duration, ctx_pct → percent, tokens/calls (and
 * any future count metric) → a plain count. The metric is recovered from the label
 * the tab passed (the tab's METRICS list owns the label↔key mapping), so the
 * drawer needs no extra wiring when a metric is added.
 */
function formatNodeMetric(label: string, value: number): string {
  switch (label.toLowerCase()) {
    case 'cost':
      return formatCost(value);
    case 'duration':
      return formatDuration(value);
    case 'context %':
      return formatPct(value);
    default:
      // tokens / calls / any future count-shaped metric.
      return formatNumber(value);
  }
}

export interface SpanDetailDrawerProps {
  /** The source-aware detail to show, or null when the drawer is closed. */
  detail: SpanDetail | null;
  onClose: () => void;
}

const FOCUSABLE =
  'a[href], button:not([disabled]), textarea, input, select, [tabindex]:not([tabindex="-1"])';

export function SpanDetailDrawer({ detail, onClose }: SpanDetailDrawerProps) {
  const titleId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  // The element focused before the drawer opened, restored on close.
  const restoreRef = useRef<Element | null>(null);

  const open = detail !== null;

  // Capture the trigger and move focus into the panel on open; restore on close.
  useEffect(() => {
    if (!open) {
      return;
    }
    restoreRef.current = document.activeElement;
    // Focus the close button as the initial focus target.
    closeRef.current?.focus();
    const toRestore = restoreRef.current;
    return () => {
      if (toRestore instanceof HTMLElement) {
        toRestore.focus();
      }
    };
  }, [open]);

  // Esc closes (captured at the panel; the drawer is a leaf so this is enough).
  const onKeyDown = useCallback(
    (e: KeyboardEvent<HTMLDivElement>): void => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== 'Tab') {
        return;
      }
      // Focus trap: keep Tab / Shift+Tab within the panel.
      const panel = panelRef.current;
      if (!panel) {
        return;
      }
      const focusables = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (el) => el.offsetParent !== null || el === document.activeElement,
      );
      if (focusables.length === 0) {
        e.preventDefault();
        return;
      }
      const first = focusables[0] as HTMLElement;
      const last = focusables[focusables.length - 1] as HTMLElement;
      const active = document.activeElement;
      if (e.shiftKey && active === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      } else if (active !== null && !panel.contains(active)) {
        // Focus somehow escaped — pull it back to the first focusable.
        e.preventDefault();
        first.focus();
      }
    },
    [onClose],
  );

  const onOverlayMouseDown = (e: MouseEvent<HTMLDivElement>): void => {
    // Only an actual backdrop click (not a click that bubbled from the panel)
    // closes the drawer.
    if (e.target === e.currentTarget) {
      onClose();
    }
  };

  if (!open) {
    return null;
  }

  const header = headerOf(detail);

  return (
    // The backdrop onMouseDown is a pointer-only "click-outside-to-dismiss"
    // convenience; keyboard users dismiss via Escape (handled on the dialog
    // below). The dialog has a complete keyboard path (Escape + Tab/Shift+Tab
    // focus trap + a real close button), so the backdrop needs no key handler.
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions
    <div
      className={styles.overlay}
      data-testid="drawer-overlay"
      onMouseDown={onOverlayMouseDown}
    >
      {/* onKeyDown here is the dialog's Escape-to-close + focus-trap handler —
          the canonical modal keyboard contract. jsx-a11y classifies the
          `dialog` role as non-interactive, so its no-noninteractive-element-
          interactions rule false-positives on this required handler. */}
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */}
      <div
        ref={panelRef}
        className={styles.panel}
        role="dialog"
        aria-modal="false"
        aria-labelledby={titleId}
        onKeyDown={onKeyDown}
      >
        <header className={styles.header}>
          <div className={styles.titleWrap}>
            <span className={styles.kind} data-kind={header.kind}>
              {header.kind}
            </span>
            <h2 id={titleId} className={styles.title}>
              {header.title}
            </h2>
          </div>
          <button
            ref={closeRef}
            type="button"
            className={styles.close}
            onClick={onClose}
            aria-label="Close span details"
          >
            ✕
          </button>
        </header>

        {detail.kind === 'op' ? (
          <OpBody op={detail.op} titleId={titleId} />
        ) : detail.kind === 'span' ? (
          <SpanBody span={detail.span} />
        ) : (
          <NodeBody node={detail.node} metricLabel={detail.metricLabel} metricValue={detail.metricValue} />
        )}
      </div>
    </div>
  );
}

/** headerOf derives the kind chip + title for each variant (op id/name, span
 *  kind/name, node kind/label) so the dialog has a stable accessible name. */
function headerOf(detail: SpanDetail): { kind: string; title: string } {
  switch (detail.kind) {
    case 'op':
      return { kind: detail.op.kind, title: detail.op.name || detail.op.id };
    case 'span':
      return { kind: detail.span.kind, title: detail.span.name || detail.span.id };
    case 'node':
      return { kind: detail.node.kind, title: detail.node.label || detail.node.id };
  }
}

/** OpBody is the full Trace-tab rendering: every op field + the payload_refs
 *  list. This is the only variant that shows token/cost/model/context/payloads —
 *  the Trace tab is the only source that carries them. */
function OpBody({ op, titleId }: { op: OpDetail; titleId: string }) {
  const failed = op.error_class !== null;
  return (
    <>
      <dl className={styles.fields}>
        <Field label="Status" value={op.status} highlight={failed ? 'error' : undefined} />
        {op.model ? <Field label="Model" value={op.model} mono /> : null}
        {op.provider ? <Field label="Provider" value={op.provider} /> : null}
        <Field label="Start" value={formatTimestamp(op.start_ts)} mono />
        <Field label="End" value={formatTimestamp(op.end_ts)} mono />
        {/* Source-aware (P2): a point-event op is persisted with end_ts==start_ts
            AND duration_us==0; isInstantOp is true for it. It recorded NO measured
            duration, so show "—" (mirrors EventList), never a fabricated "0µs". */}
        <Field
          label="Duration"
          value={isInstantOp(op) ? '—' : formatDuration(op.duration_us)}
          mono
        />
        <Field label="Cost" value={formatCost(op.cost_usd)} mono />
        <Field label="Tokens in" value={formatNumber(op.tokens_in)} mono />
        <Field label="Tokens out" value={formatNumber(op.tokens_out)} mono />
        {op.ctx_used !== null || op.ctx_max !== null ? (
          <Field
            label="Context"
            value={`${formatNumber(op.ctx_used)} / ${formatNumber(op.ctx_max)}`}
            mono
          />
        ) : null}
        {op.child_session_id !== null ? (
          <Field label="Child session" value={op.child_session_id} mono />
        ) : null}
        {failed ? <Field label="Error" value={op.error_class ?? ''} highlight="error" /> : null}
      </dl>

      <section className={styles.payloads} aria-labelledby={`${titleId}-payloads`}>
        <h3 id={`${titleId}-payloads`} className={styles.payloadsTitle}>
          Payloads
        </h3>
        {op.payload_refs.length === 0 ? (
          <p className={styles.noPayloads}>No payloads for this op.</p>
        ) : (
          <ul className={styles.payloadList}>
            {op.payload_refs.map((ref) => (
              <PayloadRow key={ref.id} payload={ref} />
            ))}
          </ul>
        )}
      </section>
    </>
  );
}

/** SpanBody is the Timeline-tab rendering: only the lane/span fields. A timeline
 *  span carries no op metrics, so this shows NO cost/tokens/model/context/payloads
 *  (never a fabricated zero) and points the operator at the Trace tab. */
function SpanBody({
  span,
}: {
  span: { status: string; start_ts: number; end_ts: number | null; duration_us: number | null };
}) {
  const failed = span.status === 'failed';
  return (
    <>
      <dl className={styles.fields}>
        <Field label="Status" value={span.status} highlight={failed ? 'error' : undefined} />
        <Field label="Start" value={formatTimestamp(span.start_ts)} mono />
        <Field label="End" value={formatTimestamp(span.end_ts)} mono />
        <Field label="Duration" value={formatDuration(span.duration_us)} mono />
      </dl>
      <p className={styles.noPayloads}>Token &amp; cost detail: open this op in the Trace tab.</p>
    </>
  );
}

/** NodeBody is the per-session Topology-tab rendering: an aggregate actor. It
 *  shows the failure rate and the value of the currently-selected size metric
 *  (labeled honestly), never op-only fields and never payloads. */
function NodeBody({
  node,
  metricLabel,
  metricValue,
}: {
  node: TopologyNode;
  metricLabel: string;
  metricValue: number;
}) {
  const failed = node.failure_ratio > 0;
  return (
    <dl className={styles.fields}>
      <Field
        label="Failure rate"
        value={formatPct(node.failure_ratio)}
        highlight={failed ? 'error' : undefined}
      />
      <Field label={metricLabel} value={formatNodeMetric(metricLabel, metricValue)} mono />
    </dl>
  );
}

function Field({
  label,
  value,
  mono,
  highlight,
}: {
  label: string;
  value: string;
  mono?: boolean;
  // `| undefined` is explicit so a caller may pass `highlight={cond ? 'error'
  // : undefined}` under exactOptionalPropertyTypes.
  highlight?: 'error' | undefined;
}) {
  const valueClass = [
    styles.fieldValue,
    mono ? styles.mono : '',
    highlight === 'error' ? styles.error : '',
  ]
    .filter(Boolean)
    .join(' ');
  return (
    <div className={styles.field}>
      <dt className={styles.fieldLabel}>{label}</dt>
      <dd className={valueClass}>{value}</dd>
    </div>
  );
}

function PayloadRow({ payload }: { payload: PayloadRef }) {
  // Byte-preview deferred until GET /api/payloads/:ref exists; render metadata
  // and a disabled affordance now. When the route lands, this control enables
  // and fetches the first bytes — no structural change needed.
  return (
    <li className={styles.payloadRow}>
      <div className={styles.payloadMeta}>
        <span className={styles.payloadKind}>{payload.kind}</span>
        <span className={styles.payloadFormat}>{payload.format}</span>
        {payload.compression !== null ? (
          <span className={styles.payloadCompression}>{payload.compression}</span>
        ) : null}
        <span className={styles.payloadBytes}>
          {formatBytes(payload.stored_bytes)}
          {payload.original_bytes !== null && payload.original_bytes !== payload.stored_bytes
            ? ` (of ${formatBytes(payload.original_bytes)})`
            : ''}
        </span>
      </div>
      <button
        type="button"
        className={styles.payloadPreview}
        disabled
        title="Payload preview is coming in a later release"
      >
        Preview (coming soon)
      </button>
    </li>
  );
}
