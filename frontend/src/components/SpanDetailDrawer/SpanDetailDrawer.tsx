import { useCallback, useEffect, useId, useRef, useState } from 'react';
import type { KeyboardEvent, MouseEvent } from 'react';
import { fetchPayloadContent } from '../../api/payloads';
import { fetchOpPayloadRefs } from '../../api/sessions';
import type { OpDetail, PayloadRef, TopologyNode } from '../../api/types';
import {
  formatBytes,
  formatCost,
  formatDuration,
  formatNumber,
  formatPct,
  formatTimestamp,
} from '../../lib/format';
import { isInstantOp, type TraceOpFields } from '../../viz/trace';
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
// The payload byte-preview route (GET /api/payloads/:id, SOW-0033) is live: the
// 'op' drawer shows each ref's metadata plus an active Preview button that
// fetches the first ~4 KB on click. Failed ops also surface error_message
// (SOW-0070).

/**
 * SpanDetail is the source-aware drawer payload. The discriminant `kind` selects
 * which fields are real for the view that opened the drawer; the drawer renders
 * only those, so an unavailable field is never shown as a fabricated zero.
 *
 * 'op' carries TraceOpFields (the slim trace shape: span + tree + error + session
 * tags) rather than the full OpDetail. The trace endpoint dropped tokens/cost/
 * ctx/model/provider for high-volume perf (SOW-0092 chunk 2); the drawer shows
 * the metrics section only when those fields are present, otherwise directs
 * the operator to the session-detail panel for the full breakdown.
 */
export type SpanDetail =
  | { kind: 'op'; op: TraceOpFields | OpDetail }
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

function hasPositiveNumber(value: unknown): value is number {
  return typeof value === 'number' && value > 0;
}

function maskLocationURI(uri: string): string {
  const filePrefix = 'file://';
  if (uri.startsWith(filePrefix)) {
    const path = uri.slice(filePrefix.length);
    const basename = path.split('/').filter(Boolean).pop();
    return basename ? `${filePrefix}.../${basename}` : `${filePrefix}...`;
  }
  const schemeIdx = uri.indexOf('://');
  if (schemeIdx > 0) {
    return `${uri.slice(0, schemeIdx + 3)}...`;
  }
  return uri.length > 24 ? `...${uri.slice(-21)}` : uri;
}

export interface SpanDetailDrawerProps {
  /** The source-aware detail to show, or null when the drawer is closed. */
  detail: SpanDetail | null;
  /** Owning session id, required only for lazy op proof loading. */
  sessionId?: string;
  onClose: () => void;
}

const FOCUSABLE =
  'a[href], button:not([disabled]), textarea, input, select, [tabindex]:not([tabindex="-1"])';

interface FocusableBounds {
  firstFocusable: HTMLElement;
  lastFocusable: HTMLElement;
}

function focusableBounds(panel: HTMLElement): FocusableBounds | null {
  let firstFocusable: HTMLElement | null = null;
  let lastFocusable: HTMLElement | null = null;

  for (const element of panel.querySelectorAll<HTMLElement>(FOCUSABLE)) {
    if (element.offsetParent === null && element !== document.activeElement) {
      continue;
    }
    firstFocusable ??= element;
    lastFocusable = element;
  }

  if (firstFocusable === null || lastFocusable === null) {
    return null;
  }
  return { firstFocusable, lastFocusable };
}

export function SpanDetailDrawer({ detail, sessionId, onClose }: SpanDetailDrawerProps) {
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
      const bounds = focusableBounds(panel);
      if (bounds === null) {
        e.preventDefault();
        return;
      }
      const active = document.activeElement;
      if (e.shiftKey && active === bounds.firstFocusable) {
        e.preventDefault();
        bounds.lastFocusable.focus();
      } else if (!e.shiftKey && active === bounds.lastFocusable) {
        e.preventDefault();
        bounds.firstFocusable.focus();
      } else if (active !== null && !panel.contains(active)) {
        // Focus somehow escaped — pull it back to the first focusable.
        e.preventDefault();
        bounds.firstFocusable.focus();
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
          <OpBody
            op={detail.op}
            {...(sessionId !== undefined ? { sessionId } : {})}
            titleId={titleId}
          />
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
function payloadHasProof(ref: PayloadRef): boolean {
  return ref.location_uri !== undefined || ref.sha256 !== undefined;
}

interface OpProofState {
  key: string;
  refs: PayloadRef[] | null;
  loading: boolean;
  error: string | null;
}

function OpBody({
  op,
  sessionId,
  titleId,
}: {
  op: TraceOpFields | OpDetail;
  sessionId?: string;
  titleId: string;
}) {
  const failed = op.error_class !== null && op.error_class !== undefined;
  const proofKey = `${sessionId ?? ''}\0${op.id}`;
  const [proofState, setProofState] = useState<OpProofState>({
    key: '',
    refs: null,
    loading: false,
    error: null,
  });
  const activeProof = proofState.key === proofKey
    ? proofState
    : { key: proofKey, refs: null, loading: false, error: null };

  const loadProofRefs = useCallback(async () => {
    if (sessionId === undefined || sessionId === '') {
      return;
    }
    setProofState({ key: proofKey, refs: null, loading: true, error: null });
    try {
      const out = await fetchOpPayloadRefs(sessionId, op.id, { includeProof: true });
      setProofState({ key: proofKey, refs: out.refs, loading: false, error: null });
    } catch (e) {
      setProofState({
        key: proofKey,
        refs: null,
        loading: false,
        error: e instanceof Error ? e.message : 'fetch failed',
      });
    }
  }, [op.id, proofKey, sessionId]);

  // Source-aware metrics: OpDetail carries model/tokens/cost/ctx; TraceOpFields
  // does NOT (SOW-0092 dropped them for high-volume perf). We detect by
  // checking for tokens_in (a non-zero on real ops, absent on the slim
  // trace shape) — when absent, omit the metrics section entirely instead
  // of rendering fabricated zeros (per the source-aware drawer contract
  // documented at the top of this file).
  const tokensIn = 'tokens_in' in op ? op.tokens_in : undefined;
  const hasMetrics = tokensIn !== undefined;
  return (
    <>
      <dl className={styles.fields}>
        <Field label="Status" value={op.status} highlight={failed ? 'error' : undefined} />
        {'model' in op && op.model ? <Field label="Model" value={op.model} mono /> : null}
        {'provider' in op && op.provider ? <Field label="Provider" value={op.provider} /> : null}
        {'tool_namespace' in op && op.tool_namespace ? (
          <Field label="Tool namespace" value={op.tool_namespace} />
        ) : null}
        {'provider_alias' in op && op.provider_alias ? (
          <Field label="Provider alias" value={op.provider_alias} />
        ) : null}
        {'reasoning_kind' in op && op.reasoning_kind ? (
          <Field label="Reasoning kind" value={op.reasoning_kind} />
        ) : null}
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
        {hasMetrics ? (
          // Safe to index op.tokens_in etc — hasMetrics proves tokens_in is present.
          ((metricsOp) => (
            <>
              <Field label="Cost" value={formatCost(metricsOp.cost_usd)} mono />
              <Field label="Tokens in" value={formatNumber(metricsOp.tokens_in)} mono />
              <Field label="Tokens out" value={formatNumber(metricsOp.tokens_out)} mono />
              {hasPositiveNumber(metricsOp.tokens_cache_read) ? (
                <Field label="Cache read" value={formatNumber(metricsOp.tokens_cache_read)} mono />
              ) : null}
              {hasPositiveNumber(metricsOp.tokens_cache_write) ? (
                <Field label="Cache write" value={formatNumber(metricsOp.tokens_cache_write)} mono />
              ) : null}
              {metricsOp.ctx_used !== null && metricsOp.ctx_max !== null ? (
                <Field
                  label="Context"
                  value={`${formatNumber(metricsOp.ctx_used)} / ${formatNumber(metricsOp.ctx_max)}`}
                  mono
                />
              ) : null}
              {hasPositiveNumber(metricsOp.bytes_in) ? (
                <Field label="Bytes in" value={formatBytes(metricsOp.bytes_in)} mono />
              ) : null}
              {hasPositiveNumber(metricsOp.bytes_out) ? (
                <Field label="Bytes out" value={formatBytes(metricsOp.bytes_out)} mono />
              ) : null}
              {hasPositiveNumber(metricsOp.chars_in) ? (
                <Field label="Chars in" value={formatNumber(metricsOp.chars_in)} mono />
              ) : null}
              {hasPositiveNumber(metricsOp.chars_out) ? (
                <Field label="Chars out" value={formatNumber(metricsOp.chars_out)} mono />
              ) : null}
            </>
          ))(op as OpDetail)
        ) : (
          <p className={styles.fieldsNote}>
            Full op metrics (model, tokens, cost, context) are delivered by
            /api/sessions/:id. The trace endpoint returns the slim shape for
            high-volume perf (SOW-0092).
          </p>
        )}
        {op.child_session_id !== null ? (
          <Field label="Child session" value={op.child_session_id} mono />
        ) : null}
        {failed ? <Field label="Error" value={op.error_class ?? ''} highlight="error" /> : null}
        {failed && op.error_message ? (
          <Field label="Error message" value={op.error_message} highlight="error" />
        ) : null}
      </dl>

      <section className={styles.payloads} aria-labelledby={`${titleId}-payloads`}>
        <h3 id={`${titleId}-payloads`} className={styles.payloadsTitle}>
          Payloads
        </h3>
        {(() => {
          const inlineRefs: PayloadRef[] = 'payload_refs' in op ? (op.payload_refs ?? []) : [];
          const refs = activeProof.refs ?? inlineRefs;
          const canLoadProof = sessionId !== undefined && sessionId !== '';
          const needsProof = canLoadProof && activeProof.refs === null && !refs.some(payloadHasProof);
          return refs.length === 0 ? (
            <>
              {needsProof ? (
                <button
                  type="button"
                  className={styles.payloadPreview}
                  onClick={() => { void loadProofRefs(); }}
                  disabled={activeProof.loading}
                >
                  {activeProof.loading ? 'Loading proof…' : 'Load proof'}
                </button>
              ) : null}
              {activeProof.error !== null ? (
                <div className={styles.payloadError} role="alert">{activeProof.error}</div>
              ) : null}
              <p className={styles.noPayloads}>No payloads for this op.</p>
            </>
          ) : (
            <>
              {needsProof ? (
                <button
                  type="button"
                  className={styles.payloadPreview}
                  onClick={() => { void loadProofRefs(); }}
                  disabled={activeProof.loading}
                >
                  {activeProof.loading ? 'Loading proof…' : 'Load proof'}
                </button>
              ) : null}
              {activeProof.error !== null ? (
                <div className={styles.payloadError} role="alert">{activeProof.error}</div>
              ) : null}
              <ul className={styles.payloadList}>
                {refs.map((ref) => (
                  <PayloadRow key={ref.id} payload={ref} />
                ))}
              </ul>
            </>
          );
        })()}
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
  const [preview, setPreview] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showPreview, setShowPreview] = useState(false);
  const [showProof, setShowProof] = useState(false);
  const [copyError, setCopyError] = useState<string | null>(null);

  const hasProof = payload.location_uri !== undefined || payload.sha256 !== undefined;

  const copyLocation = useCallback(async () => {
    if (payload.location_uri === undefined) {
      return;
    }
    try {
      await navigator.clipboard.writeText(payload.location_uri);
      setCopyError(null);
    } catch (e) {
      setCopyError(e instanceof Error ? e.message : 'copy failed');
    }
  }, [payload.location_uri]);

  const fetchPreview = useCallback(async () => {
    if (preview !== null) {
      setShowPreview((v) => !v);
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const content = await fetchPayloadContent(payload.id);
      const { text, headers } = content;
      const total = headers.totalBytes;
      setPreview(headers.truncated && total !== null ? `${text}\n\n--- truncated (showing first 4 KB of ${formatBytes(total)}) ---` : text);
      setShowPreview(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'fetch failed');
    } finally {
      setLoading(false);
    }
  }, [payload.id, preview]);

  return (
    <li className={styles.payloadRow}>
      <div className={styles.payloadMeta}>
        <span className={styles.payloadKind}>{payload.kind}</span>
        <span className={styles.payloadClass}>{payload.artifact_class}</span>
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
        {payload.location_uri !== undefined ? (
          <span className={styles.payloadSelector}>{maskLocationURI(payload.location_uri)}</span>
        ) : null}
      </div>
      <div className={styles.payloadActions}>
        {hasProof ? (
          <button
            type="button"
            className={styles.payloadPreview}
            onClick={() => {
              setShowProof((v) => !v);
            }}
            title="Show payload selector/hash proof"
          >
            {showProof ? 'Hide proof' : 'Proof'}
          </button>
        ) : null}
        <button
          type="button"
          className={styles.payloadPreview}
          onClick={() => { void fetchPreview(); }}
          disabled={loading}
          title="Preview payload content (first 4 KB)"
        >
          {loading ? 'Loading…' : showPreview ? 'Hide' : 'Preview'}
        </button>
      </div>
      {error !== null && (
        <div className={styles.payloadError} role="alert">{error}</div>
      )}
      {copyError !== null && (
        <div className={styles.payloadError} role="alert">{copyError}</div>
      )}
      {showProof ? (
        <dl className={styles.payloadProof}>
          {payload.location_uri !== undefined ? (
            <div className={styles.payloadProofRow}>
              <dt>Selector</dt>
              <dd>
                <code>{payload.location_uri}</code>
                <button
                  type="button"
                  className={styles.payloadPreview}
                  onClick={() => { void copyLocation(); }}
                >
                  Copy selector
                </button>
              </dd>
            </div>
          ) : null}
          {payload.sha256 !== undefined && payload.sha256 !== null ? (
            <div className={styles.payloadProofRow}>
              <dt>SHA-256</dt>
              <dd><code>{payload.sha256}</code></dd>
            </div>
          ) : null}
        </dl>
      ) : null}
      {showPreview && preview !== null && (
        <pre className={styles.payloadPreviewContent}>
          <code>{preview}</code>
        </pre>
      )}
    </li>
  );
}
