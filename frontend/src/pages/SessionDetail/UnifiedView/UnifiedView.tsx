// UnifiedView (ui-turn-view.md §ui-session-unified-view): the new Session
// Detail shell. Three zones:
//
// eslint-disable @typescript-eslint/no-unsafe-argument, @typescript-eslint/no-unsafe-assignment, @typescript-eslint/no-unsafe-call, @typescript-eslint/no-unsafe-member-access, @typescript-eslint/no-unsafe-return
// The setSearchParams callback's `prev` is typed as `any` by react-router-dom;
// every callsite uses `new URLSearchParams(prev)` which trips the strict rule.
// The escapes below are all local and intentional.
//
//   1. Header — breadcrumb + pin button (rendered by the page wrapper, NOT here).
//   2. Overview tiles — condensed 6-tile strip (Status / Duration / Tokens /
//      Cost / Failures / Context).
//   3. Resizable body:
//        - Left column (viz + bottom): resizable vertical split. The viz pane
//          holds the visualization tabs (Waterfall default; Topology/Timeline/
//          Statistics). The bottom pane holds the Event list / Logs / Raw tabs.
//        - Right column: the turn view. Reads `?op=<id>` from the URL, scrolls
//          + pulses the matching step.
//
// Click on any span / op / event in the LEFT pane → setSearchParams with
// `?op=<id>` → RIGHT pane scrolls to the matching step.
//
// Layout sizes are persisted via react-resizable-panels autoSaveId:
//   - ai-viewer.session.vbottom  → viz / bottom vertical split
//   - ai-viewer.session.vright   → left / right horizontal split
//
// The setSearchParams callback receives URLSearchParams, but react-router-dom
// types it loosely; every setSearchParams call site uses the same pattern
// (`new URLSearchParams(prev)`) which trips the strict rule.
//
//   1. Header — breadcrumb + pin button (rendered by the page wrapper, NOT here).
//   2. Overview tiles — condensed 6-tile strip (Status / Duration / Tokens /
//      Cost / Failures / Context).
//   3. Resizable body:
//        - Left column (viz + bottom): resizable vertical split. The viz pane
//          holds the visualization tabs (Waterfall default; Topology/Timeline/
//          Statistics). The bottom pane holds the Event list / Logs / Raw tabs.
//        - Right column: the turn view. Reads `?op=<id>` from the URL, scrolls
//          + pulses the matching step.
//
// Click on any span / op / event in the LEFT pane → setSearchParams with
// `?op=<id>` → RIGHT pane scrolls to the matching step.
//
// Layout sizes are persisted via react-resizable-panels autoSaveId:
//   - ai-viewer.session.vbottom  → viz / bottom vertical split
//   - ai-viewer.session.vright   → left / right horizontal split

import { useEffect, useMemo } from 'react';
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels';
import { useSearchParams } from 'react-router-dom';
import type { SessionDetailResponse, PayloadRef } from '../../../api/types';
import { useOpPayloadRefs, useTurnPayloadRefs } from '../../../api/sessions';
import { TurnView } from '../../../components/TurnView';
import { EmptyState } from '../../../components/StatusViews';
import { TraceTab } from '../TraceTab';
import { TopologyTab } from '../TopologyTab';
import { TimelineTab } from '../TimelineTab';
import { LogsTab } from '../LogsTab';
import { RawDataTab } from '../RawDataTab';
import { OverviewTiles } from './OverviewTiles';
import {
  parseVizTab,
  parseBottomTab,
  parseStepKindFilter,
  type VizTabKey,
  type BottomTabKey,
  type StepKindFilter,
} from './types';
import styles from './UnifiedView.module.css';

const VIZ_TABS: readonly { key: VizTabKey; label: string }[] = [
  { key: 'waterfall', label: 'Waterfall' },
  { key: 'topology', label: 'Topology' },
  { key: 'timeline', label: 'Timeline' },
  { key: 'stats', label: 'Statistics' },
];

const BOTTOM_TABS: readonly { key: BottomTabKey; label: string }[] = [
  { key: 'events', label: 'Event list' },
  { key: 'logs', label: 'Logs' },
  { key: 'raw', label: 'Raw Data' },
];

export function UnifiedView({ detail }: { detail: SessionDetailResponse }) {
  const [searchParams, setSearchParams] = useSearchParams();

  // Active tabs live in the URL so they survive a refresh + are shareable.
  const vizTab = parseVizTab(searchParams.get('tab:viz'));
  const bottomTab = parseBottomTab(searchParams.get('tab:bottom'));

  const setVizTab = (next: VizTabKey): void => {
    setSearchParams(
      (prev) => {
        const sp = new URLSearchParams(prev);
        sp.set('tab:viz', next);
        return sp;
      },
      { replace: true },
    );
  };

  const setBottomTab = (next: BottomTabKey): void => {
    setSearchParams(
      (prev) => {
        const sp = new URLSearchParams(prev);
        sp.set('tab:bottom', next);
        return sp;
      },
      { replace: true },
    );
  };

  // Focused op is shared between left pane (waterfall/event list clicks)
  // and right pane (turn view reads this to scroll + pulse).
  const focusedOpId: string | null = searchParams.get('op');

  const setFocusedOpId = (opId: string | null): void => {
    setSearchParams(
      (prev) => {
        const sp = new URLSearchParams(prev);
        if (opId === null) {
          sp.delete('op');
        } else {
          sp.set('op', opId);
        }
        return sp;
      },
      { replace: true },
    );
  };

  // Sync the focused op up to the URL. The viz pane (TraceTab) does NOT
  // touch the URL directly; this effect bridges internal "selected" state
  // to the URL when it changes. TraceTab still owns its own selection UI;
  // the right pane only ever reads from the URL.
  // (We don't need a setter from TraceTab — clicking a span opens the
  // SpanDetailDrawer locally; the operator can also click "show in turn"
  // in the drawer to set ?op. For now, the URL is the integration point.)

  // Find the turn that contains the focused op so we can pre-pick the turn
  // picker. Empty for unknown ops.
  const focusedTurn = useMemo(() => {
    if (focusedOpId === null) return null;
    for (const turn of detail.turns) {
      if (turn.ops.some((op) => op.id === focusedOpId)) return turn;
    }
    return null;
  }, [focusedOpId, detail.turns]);

  // Side effect: when the user navigates with ?op=<id> pointing at a turn
  // that the right pane isn't currently showing, scroll it into view. The
  // TurnView component handles its own focusOpId + scrollIntoView.
  useEffect(() => {
    if (focusedOpId === null) return;
    // No-op: TurnView's useEffect handles the scroll when focused prop is set.
  }, [focusedOpId]);

  return (
    <div className={styles.unified}>
      {/* 1. Overview tiles row */}
      <OverviewTiles detail={detail} />

      {/* 2. Resizable body: left (viz + bottom) | right (turn view) */}
      <div className={styles.body}>
        <PanelGroup
          direction="horizontal"
          autoSaveId="ai-viewer.session.vright"
          className={styles.outerGroup}
        >
          <Panel
            defaultSize={70}
            minSize={40}
            className={styles.leftColumn}
            role="region"
            aria-label="Visualization and event list"
          >
            <PanelGroup
              direction="vertical"
              autoSaveId="ai-viewer.session.vbottom"
              className={styles.innerGroup}
            >
              {/* Viz zone */}
              <Panel
                defaultSize={65}
                minSize={25}
                className={styles.vizPanel}
                role="region"
                aria-label={`${vizTab} visualization`}
              >
                <div className={styles.tabBar}>
                  {VIZ_TABS.map((t) => (
                    <button
                      key={t.key}
                      type="button"
                      className={styles.tab}
                      data-active={vizTab === t.key}
                      onClick={() => {
                        setVizTab(t.key);
                      }}
                      aria-pressed={vizTab === t.key}
                    >
                      {t.label}
                    </button>
                  ))}
                </div>
                <div className={styles.vizContent}>
                  {vizTab === 'waterfall' ? (
                    <TraceTab detail={detail} />
                  ) : vizTab === 'topology' ? (
                    <TopologyTab sessionId={detail.session.id} />
                  ) : vizTab === 'timeline' ? (
                    <TimelineTab sessionId={detail.session.id} />
                  ) : (
                    <EmptyState>Per-session statistics coming soon.</EmptyState>
                  )}
                </div>
              </Panel>

              <PanelResizeHandle className={styles.resizeHandleV ?? ''} />

              {/* Bottom zone: event list / logs / raw */}
              <Panel
                defaultSize={35}
                minSize={15}
                className={styles.bottomPanel}
                role="region"
                aria-label={`${bottomTab} panel`}
              >
                <div className={styles.tabBar}>
                  {BOTTOM_TABS.map((t) => (
                    <button
                      key={t.key}
                      type="button"
                      className={styles.tab}
                      data-active={bottomTab === t.key}
                      onClick={() => {
                        setBottomTab(t.key);
                      }}
                      aria-pressed={bottomTab === t.key}
                    >
                      {t.label}
                    </button>
                  ))}
                </div>
                <div className={styles.bottomContent}>
                  {bottomTab === 'events' ? (
                    <TraceTab detail={detail} mode="events" />
                  ) : bottomTab === 'logs' ? (
                    <LogsTab sessionId={detail.session.id} />
                  ) : (
                    <RawDataTab detail={detail} />
                  )}
                </div>
              </Panel>
            </PanelGroup>
          </Panel>

          <PanelResizeHandle className={styles.resizeHandleH ?? ''} />

          {/* Right zone: turn view */}
          <Panel
            defaultSize={30}
            minSize={20}
            className={styles.rightColumn}
            role="region"
            aria-label="Turn view"
          >
            <TurnViewPane
              detail={detail}
              focusOpId={focusedOpId}
              focusedTurnId={focusedTurn?.id ?? null}
              initialStepKindFilter={parseStepKindFilter(searchParams.get('stepKindFilter'))}
              onStepKindFilterChange={(next) => {
                setSearchParams(
                  (prev) => {
                    const sp = new URLSearchParams(prev);
                    if (next === 'all') sp.delete('stepKindFilter');
                    else sp.set('stepKindFilter', next);
                    return sp;
                  },
                  { replace: true },
                );
              }}
              onClearFocus={() => {
                setFocusedOpId(null);
              }}
              onFocusTurn={(opId) => {
                setFocusedOpId(opId);
              }}
            />
          </Panel>
        </PanelGroup>
      </div>
    </div>
  );
}

/** TurnViewPane — the right-sidebar turn view. Shows the focused turn (if any)
 *  or a placeholder listing every turn in the session. The focused-op URL
 *  param drives which turn is shown; the TurnView component then scrolls +
 *  pulses the matching step within that turn. */
function TurnViewPane({
  detail,
  focusOpId,
  focusedTurnId,
  initialStepKindFilter,
  onStepKindFilterChange,
  onClearFocus,
  onFocusTurn,
}: {
  detail: SessionDetailResponse;
  focusOpId: string | null;
  focusedTurnId: string | null;
  initialStepKindFilter: StepKindFilter;
  onStepKindFilterChange: (next: StepKindFilter) => void;
  onClearFocus: () => void;
  onFocusTurn: (_opId: string) => void;
}) {
  void focusOpId; // consumed inside TurnView via the focusOpId prop below
  // The turn picker list is collapsed (chips) when the focused turn fills
  // the pane; expanded when no focus is set so the operator can pick.
  const focusedTurn = useMemo(() => {
    if (focusedTurnId === null) return null;
    return detail.turns.find((t) => t.id === focusedTurnId) ?? null;
  }, [focusedTurnId, detail.turns]);

  // SOW-0092 chunk 3: the session-detail page ships the slim shape (no
  // payload_refs) by default. The right sidebar TurnView renders per-op
  // payload previews, so it needs the refs for the ops it actually
  // shows — at most one turn worth (~5-50 refs). We lazy-fetch the refs
  // for the FOCUSED op (the one the operator clicked to enter the right
  // pane); the few refs for sibling ops in the same turn that TurnView
  // also renders get fetched as a single batch via ?turn=<id> when the
  // pane mounts. The two requests share a 30 s in-memory cache so the
  // operator's session-navigation flow stays snappy.
  const focusedOpRefs = useOpPayloadRefs(detail.session.id, focusOpId);
  const focusedTurnRefs = useTurnPayloadRefs(detail.session.id, focusedTurnId);

  const decoratedTurn = useMemo(() => {
    if (!focusedTurn) return null;
    // Build a payload_refs index from BOTH responses (op-scoped first,
    // turn-scoped fills in the rest). Empty when both are still loading
    // and the slim session shape left every op's refs as [].
    const refsByOp = new Map<string, PayloadRef[]>();
    const turnRefs = focusedTurnRefs.data?.refs ?? [];
    for (const r of turnRefs) {
      if (r.op_id === undefined) continue;
      const arr = refsByOp.get(r.op_id) ?? [];
      arr.push(r);
      refsByOp.set(r.op_id, arr);
    }
    // Op-scoped query takes precedence (fresher on focus change).
    if (focusedOpRefs.data !== undefined && focusOpId !== null) {
      refsByOp.set(focusOpId, focusedOpRefs.data.refs);
    }
    const ops = focusedTurn.ops.map((op) => {
      const refs = refsByOp.get(op.id);
      if (refs === undefined || refs.length === 0) return op;
      return { ...op, payload_refs: refs };
    });
    // Build a new turn object with the spliced refs. If nothing changed,
    // ops === focusedTurn.ops structurally so the parent memoizes anyway.
    return { ...focusedTurn, ops };
  }, [focusedTurn, focusedTurnRefs.data, focusedOpRefs.data, focusOpId]);

  if (focusedTurn) {
    return (
      <div className={styles.turnPane}>
        <header className={styles.turnPaneHeader}>
          <button
            type="button"
            className={styles.clearFocus}
            onClick={onClearFocus}
            aria-label="Clear focus — show all turns"
          >
            ← All turns
          </button>
          <span className={styles.turnPaneMeta}>
            {detail.turns.length} turns · focused
          </span>
        </header>
        <TurnView
          turn={decoratedTurn ?? focusedTurn}
          {...(focusOpId !== null ? { focusOpId: focusOpId } : {})}
          initialStepKindFilter={initialStepKindFilter}
          onStepKindFilterChange={onStepKindFilterChange}
        />
      </div>
    );
  }

  // No focus — show a TURN PICKER (NOT TurnViews). Rendering every turn's
  // payload would fire thousands of fetches on mount; the picker instead
  // renders ONE row per turn with summary metadata (seq, op count, status,
  // timestamp). Clicking a row sets ?op=<first-op-id> which focuses the
  // turn in the right pane.
  const visible = detail.turns.slice(0, 50);
  const hidden = detail.turns.length - visible.length;

  return (
    <div className={styles.turnPane}>
      <header className={styles.turnPaneHeader}>
        <h3 className={styles.turnPaneTitle}>Turns</h3>
        <span className={styles.turnPaneMeta}>
          {detail.turns.length} total
          {hidden > 0 ? ` · showing ${visible.length}` : ''}
        </span>
      </header>
      <p className={styles.turnPickerHint}>
        Click any turn to see its full content here.
      </p>
      <ol className={styles.turnList}>
        {visible.map((turn) => {
          const firstOp = turn.ops[0];
          const lastOp = turn.ops[turn.ops.length - 1];
          const ts = lastOp?.end_ts ?? turn.end_ts ?? turn.start_ts;
          return (
            <li key={turn.id}>
              <button
                type="button"
                className={styles.turnPickerItem}
                onClick={() => {
                  if (firstOp) {
                    onFocusTurn(firstOp.id);
                  }
                }}
              >
                <span className={styles.turnPickerSeq}>#{turn.seq}</span>
                <span className={styles.turnPickerMeta}>
                  {turn.op_count} {turn.op_count === 1 ? 'op' : 'ops'}
                  {' · '}
                  <span data-status={turn.status}>{turn.status}</span>
                </span>
                <span className={styles.turnPickerTs}>
                  {new Date(ts / 1000).toLocaleString(undefined, {
                    month: 'short',
                    day: 'numeric',
                    hour: '2-digit',
                    minute: '2-digit',
                  })}
                </span>
              </button>
            </li>
          );
        })}
      </ol>
      {hidden > 0 ? (
        <p className={styles.hiddenHint}>
          {hidden} more turn{hidden === 1 ? '' : 's'} — focus an op from the
          left pane to see them in context.
        </p>
      ) : null}
    </div>
  );
}