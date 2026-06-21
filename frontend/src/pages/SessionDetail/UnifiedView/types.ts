// Tab keys for the unified Session Detail view (ui-turn-view.md §ui-session-unified-view).

/** Visualization zone tabs. Each corresponds to a real component:
 *  - waterfall → Waterfall + ByTurnWaterfall (the existing Trace tab's top)
 *  - topology  → per-session Topology
 *  - timeline  → Timeline
 *  - stats     → per-session stats summary
 */
export type VizTabKey = 'waterfall' | 'topology' | 'timeline' | 'stats';

/** Bottom zone tabs. All read the same session data:
 *  - events → EventList (the existing Trace tab's bottom section)
 *  - logs   → LogsTab
 *  - raw    → RawDataTab
 */
export type BottomTabKey = 'events' | 'logs' | 'raw';

export const VIZ_TAB_KEYS: ReadonlySet<VizTabKey> = new Set([
  'waterfall', 'topology', 'timeline', 'stats',
]);

export const BOTTOM_TAB_KEYS: ReadonlySet<BottomTabKey> = new Set([
  'events', 'logs', 'raw',
]);

export function parseVizTab(raw: string | null): VizTabKey {
  return raw !== null && VIZ_TAB_KEYS.has(raw as VizTabKey) ? (raw as VizTabKey) : 'waterfall';
}

export function parseBottomTab(raw: string | null): BottomTabKey {
  return raw !== null && BOTTOM_TAB_KEYS.has(raw as BottomTabKey) ? (raw as BottomTabKey) : 'events';
}