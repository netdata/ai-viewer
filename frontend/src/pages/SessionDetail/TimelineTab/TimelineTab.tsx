import { useEffect, useMemo, useState } from 'react';
import type { OpDetail } from '../../../api/types';
import { useTimeline } from '../../../api/sessions';
import { SpanDetailDrawer } from '../../../components/SpanDetailDrawer';
import { LoadingState, ErrorState, EmptyState } from '../../../components/StatusViews';
import {
  layoutTimeline,
  type PositionedSpan,
  type TimelineLaneInput,
} from '../../../viz/timeline';
import { startThemeColorWatch } from '../../../viz/color';
import { TimelineRenderer } from './TimelineRenderer';
import styles from './TimelineTab.module.css';

// Timeline tab (ui-pages.md §/sessions/:id #4 "Timeline" — video-editor style).
// Fetches the whole session tree's lane/span model (useTimeline), lays it out via
// viz/timeline (lane→y, span→x on a shared time scale; null-end ops as instant
// markers; compaction ops flagged), and renders it through TimelineRenderer.
// Overlap between lanes is intentional (parallel sub-agents). Pan/zoom is
// SOW-0006's default — shift+wheel zooms, plain wheel pans. Clicking a span opens
// the shared SpanDetailDrawer (a timeline span maps to an op, so a minimal
// OpDetail is synthesized exactly as TopologyTab does for a node).

const VIEW_WIDTH = 1000;
const LANE_HEIGHT = 40;
const AXIS_HEIGHT = 22; // mirrors TimelineRenderer's reserved axis band.
const MIN_TRACK_HEIGHT = 220;

/**
 * spanToOpDetail synthesizes a minimal OpDetail so the shared SpanDetailDrawer
 * (an op-shaped panel) can present a clicked timeline span. A timeline span
 * carries only the lane-view fields (id/kind/name/start/end/status); the numeric
 * op fields the drawer also shows are not in the timeline payload, so they are
 * zeroed/nulled and there are no payloads. This reuses the drawer exactly
 * (SOW-0006 decision: "clicking any span/node/row opens the shared drawer")
 * without weakening its OpDetail contract — the duration is derived from the
 * span's own start/end so a closed span still shows its real duration.
 */
function spanToOpDetail(s: PositionedSpan): OpDetail {
  const { span } = s;
  const durationUs =
    span.end_ts !== null && span.end_ts >= span.start_ts ? span.end_ts - span.start_ts : null;
  return {
    id: span.id,
    kind: span.kind,
    name: span.name,
    model: '',
    provider: '',
    start_ts: span.start_ts,
    end_ts: span.end_ts,
    duration_us: durationUs,
    status: span.status,
    error_class: null,
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    ctx_used: null,
    ctx_max: null,
    child_session_id: null,
    payload_refs: [],
  };
}

export function TimelineTab({ sessionId }: { sessionId: string }) {
  const [selected, setSelected] = useState<PositionedSpan | null>(null);

  useEffect(() => startThemeColorWatch(), []);

  const { data, isPending, isError, error } = useTimeline(sessionId);

  const lanes = useMemo<TimelineLaneInput[]>(() => data?.lanes ?? [], [data]);
  const tStart = data?.t_start ?? 0;
  const tEnd = data?.t_end ?? 0;

  const layout = useMemo(
    () => layoutTimeline(lanes, { width: VIEW_WIDTH, laneHeight: LANE_HEIGHT, tStart, tEnd }),
    [lanes, tStart, tEnd],
  );

  // Track height = axis + all lanes, with a sensible minimum so an empty/short
  // timeline still fills the panel.
  const trackHeight = Math.max(MIN_TRACK_HEIGHT, AXIS_HEIGHT + layout.lanes.length * LANE_HEIGHT);
  const spanCount = layout.spans.length;
  const hasSpans = spanCount > 0;

  return (
    <div className={styles.wrap}>
      <div className={styles.toolbar}>
        <span className={styles.hint}>Shift + wheel to zoom, wheel to pan</span>
        <span className={styles.spacer} />
        <span className={styles.spanCount}>
          {spanCount} span{spanCount === 1 ? '' : 's'} · {lanes.length} lane
          {lanes.length === 1 ? '' : 's'}
        </span>
      </div>

      {isPending ? (
        <LoadingState label="Loading timeline…" />
      ) : isError ? (
        <ErrorState error={error} title="Failed to load timeline" />
      ) : !hasSpans ? (
        <EmptyState>No spans recorded for this session.</EmptyState>
      ) : (
        <>
          <div className={styles.vizArea}>
            <TimelineRenderer
              lanes={layout.lanes}
              spans={layout.spans}
              width={VIEW_WIDTH}
              height={trackHeight}
              tStart={tStart}
              tEnd={tEnd}
              selectedId={selected?.span.id ?? null}
              onSpanClick={(s) => {
                setSelected(s);
              }}
            />
          </div>
          <Legend />
        </>
      )}

      <SpanDetailDrawer
        op={selected ? spanToOpDetail(selected) : null}
        onClose={() => {
          setSelected(null);
        }}
      />
    </div>
  );
}

/** Legend explains the timeline encodings (bar = op, tick = instant/running,
 *  dashed rule = compaction breakpoint). */
function Legend() {
  return (
    <div className={styles.legend} aria-label="Timeline legend">
      <span className={styles.legendItem}>
        <span className={styles.legendSwatch} style={{ background: 'var(--accent)' }} />
        Span (bar, colored by kind)
      </span>
      <span className={styles.legendItem}>
        <span className={styles.legendInstant} />
        Instant / running op
      </span>
      <span className={styles.legendItem}>
        <span className={styles.legendBreakpoint} />
        Compaction breakpoint
      </span>
    </div>
  );
}
