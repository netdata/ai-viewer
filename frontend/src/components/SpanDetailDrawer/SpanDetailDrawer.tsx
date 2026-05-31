import { useCallback, useEffect, useId, useRef } from 'react';
import type { KeyboardEvent, MouseEvent } from 'react';
import type { OpDetail, PayloadRef } from '../../api/types';
import {
  formatBytes,
  formatCost,
  formatDuration,
  formatNumber,
  formatTimestamp,
} from '../../lib/format';
import styles from './SpanDetailDrawer.module.css';

// Shared right-side span detail drawer (ui-pages.md §Span detail drawer). NOT a
// modal — the visualization behind stays visible (aria-modal="false"), but the
// drawer is still focus-trapped while open for keyboard a11y, closes on Esc /
// outside-click, and returns focus to the element that opened it. It renders the
// full op row + a list of payload_refs. The payload byte-preview route
// (GET /api/payloads/:ref) is Phase 2 (rest-api.md §GET /api/payloads); until it
// lands the drawer shows each ref's metadata and a DISABLED "preview coming
// soon" control, structured so the byte-preview wires in trivially later.

export interface SpanDetailDrawerProps {
  /** The op to detail, or null when the drawer is closed. */
  op: OpDetail | null;
  onClose: () => void;
}

const FOCUSABLE =
  'a[href], button:not([disabled]), textarea, input, select, [tabindex]:not([tabindex="-1"])';

export function SpanDetailDrawer({ op, onClose }: SpanDetailDrawerProps) {
  const titleId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  // The element focused before the drawer opened, restored on close.
  const restoreRef = useRef<Element | null>(null);

  const open = op !== null;

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

  const failed = op.error_class !== null;

  return (
    <div
      className={styles.overlay}
      data-testid="drawer-overlay"
      onMouseDown={onOverlayMouseDown}
    >
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
            <span className={styles.kind} data-kind={op.kind}>
              {op.kind}
            </span>
            <h2 id={titleId} className={styles.title}>
              {op.name || op.id}
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

        <dl className={styles.fields}>
          <Field label="Status" value={op.status} highlight={failed ? 'error' : undefined} />
          {op.model ? <Field label="Model" value={op.model} mono /> : null}
          {op.provider ? <Field label="Provider" value={op.provider} /> : null}
          <Field label="Start" value={formatTimestamp(op.start_ts)} mono />
          <Field label="End" value={formatTimestamp(op.end_ts)} mono />
          <Field label="Duration" value={formatDuration(op.duration_us)} mono />
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
          {failed ? (
            <Field label="Error" value={op.error_class ?? ''} highlight="error" />
          ) : null}
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
      </div>
    </div>
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
