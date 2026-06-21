// useTraceNodes — shared derivation for the trace visualization + the event
// list. Both halves of the unified view (viz pane + event-list pane) need
// the SAME filtered flat-op list; computing it twice would (a) duplicate
// React work, (b) desync the filters between panes. This hook is the
// single source of truth.

import { useEffect, useMemo, useState } from 'react';
import type { SessionDetailResponse } from '../../../api/types';
import { useSessionTrace } from '../../../api/sessions';
import {
  buildMergedTree,
  buildOpTree,
  flattenTree,
  SVG_SPAN_CEILING,
  type TraceNode,
} from '../../../viz/trace';
import { startThemeColorWatch } from '../../../viz/color';

export type WaterfallMode = 'detailed' | 'byturn';

export interface TraceFilters {
  kind: string;
  status: string;
  agent: string;
}

/** useTraceNodes returns the whole-session flat op list, the filter controls,
 *  and the canvas-vs-SVG ceiling signal. Used by TraceTab (full) and by the
 *  UnifiedView (viz + event list halves share the same hook). */
export function useTraceNodes(detail: SessionDetailResponse): {
  flatAll: TraceNode[];
  flat: TraceNode[];
  roots: TraceNode[];
  agentOptions: string[];
  filters: TraceFilters;
  setFilters: {
    setKind: (_: string) => void;
    setStatus: (_: string) => void;
    setAgent: (_: string) => void;
  };
  useCanvas: boolean;
  turnBoundaryIds: ReadonlySet<string>;
} {
  // Keep the viz palette in sync with theme flips while the consumer is mounted.
  useEffect(() => startThemeColorWatch(), []);

  const [kindFilter, setKindFilter] = useState<string>('all');
  const [statusFilter, setStatusFilter] = useState<string>('all');
  const [agentFilter, setAgentFilter] = useState<string>('all');

  const trace = useSessionTrace(detail.session.id);
  const roots = useMemo(() => {
    if (trace.data && trace.data.ops.length > 0) {
      return buildMergedTree(trace.data.ops);
    }
    return buildOpTree(detail.turns);
  }, [trace.data, detail.turns]);
  const flatAll = useMemo(() => flattenTree(roots), [roots]);

  const agentOptions = useMemo(() => {
    const names = new Set<string>();
    for (const n of flatAll) {
      if (n.sessionAgent) {
        names.add(n.sessionAgent);
      }
    }
    return [...names].sort();
  }, [flatAll]);

  const flat = useMemo(() => {
    return flatAll.filter((n) => {
      if (kindFilter !== 'all' && n.op.kind !== kindFilter) return false;
      if (statusFilter === 'failed' && n.op.error_class === null) return false;
      if (statusFilter === 'completed' && n.op.status !== 'completed') return false;
      if (agentFilter !== 'all' && (n.sessionAgent ?? '') !== agentFilter) return false;
      return true;
    });
  }, [flatAll, kindFilter, statusFilter, agentFilter]);

  const turnBoundaryIds = useMemo(() => turnBoundaries(detail), [detail]);
  const useCanvas = flat.length > SVG_SPAN_CEILING;

  return {
    flatAll,
    flat,
    roots,
    agentOptions,
    filters: { kind: kindFilter, status: statusFilter, agent: agentFilter },
    setFilters: { setKind: setKindFilter, setStatus: setStatusFilter, setAgent: setAgentFilter },
    useCanvas,
    turnBoundaryIds,
  };
}

function turnBoundaries(detail: SessionDetailResponse): ReadonlySet<string> {
  const ids = new Set<string>();
  detail.turns.forEach((turn, index) => {
    if (index === 0 || turn.ops.length === 0) {
      return;
    }
    let earliest: { id: string; start_ts: number } | null = null;
    for (const op of turn.ops) {
      if (earliest === null || op.start_ts < earliest.start_ts) {
        earliest = { id: op.id, start_ts: op.start_ts };
      }
    }
    if (earliest !== null) {
      ids.add(earliest.id);
    }
  });
  return ids;
}