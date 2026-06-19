import type {
  AggregateGroupBy,
  StatsBucket,
  StatsMetric,
  TopDimension,
} from '../../api/types';

// The /stats chart controls live in the URL so the whole view (filters + chart
// controls) is shareable / bookmarkable (ui-pages.md §/stats "Copy-share-link").
// This mirrors filters.ts: a typed parse/serialize pair with own param names,
// distinct from the global filter keys (agents/models/tools/status/sources/
// from/to/q) so the two coexist under one URLSearchParams without collision.
//
// Reads CLAMP: an absent OR unknown param falls back to the default — never
// throws — so a hand-edited / stale link degrades to the default view instead
// of breaking the page. Writes go through a merge patch on the existing params
// (functional setSearchParams), so changing a chart control preserves the
// filter params and vice-versa (same merge-patch contract as applyPatch).

/**
 * The trend metric. Extends the server `StatsMetric` enum with one
 * FRONTEND-ONLY derived value: `failure_rate`. A rate cannot be SUM-ed by the
 * rollup fast path (ratios do not aggregate), so the server has no
 * `failure_rate` metric; instead the page fetches the `failures` AND `calls`
 * aggregates for the same group_by and divides per bucket
 * (rest-api.md §GET /api/stats — SOW-0067). `topMetric` stays the plain server
 * enum because the top-N ranking is a single absolute metric.
 */
export type TrendMetric = StatsMetric | 'failure_rate';

/** The own (chart-control) param names. Distinct from the filter keys. */
export const STAT_PARAM_KEYS = {
  trendMetric: 'stat_metric',
  bucket: 'stat_bucket',
  trendGroupBy: 'stat_group',
  topDimension: 'top_dim',
  topMetric: 'top_metric',
} as const;

/** Decoded, page-facing chart-control state. */
export interface StatControls {
  trendMetric: TrendMetric;
  bucket: StatsBucket;
  trendGroupBy: AggregateGroupBy;
  topDimension: TopDimension;
  topMetric: StatsMetric;
}

/** A partial patch over the chart controls (one control changes at a time). */
export type StatControlsPatch = Partial<StatControls>;

/** Defaults match the pre-URL useState defaults (Stats Chunk 9b). */
export const STAT_CONTROL_DEFAULTS: StatControls = {
  trendMetric: 'cost',
  bucket: 'daily',
  trendGroupBy: 'total',
  topDimension: 'model',
  topMetric: 'cost',
};

// Closed valid sets — the parse step clamps to these; an unknown value is NOT
// an error (it falls back to the default), matching the server's own closed
// enums (api/types.ts). Kept as Sets for O(1) membership without a cast trick.
const METRIC_VALUES = new Set<StatsMetric>([
  'cost',
  'tokens_in',
  'tokens_out',
  'calls',
  'failures',
  'duration_us',
  'sessions',
]);
const TREND_METRIC_VALUES = new Set<TrendMetric>([
  ...METRIC_VALUES,
  'failure_rate',
]);
const BUCKET_VALUES = new Set<StatsBucket>(['hourly', 'daily']);
const GROUP_BY_VALUES = new Set<AggregateGroupBy>([
  'model',
  'provider',
  'tool',
  'agent',
  'cwd',
  'source_format',
  'total',
]);
const DIMENSION_VALUES = new Set<TopDimension>([
  'model',
  'provider',
  'tool',
  'agent',
  'cwd',
]);

function parseTrendMetric(raw: string | null): TrendMetric {
  return raw !== null && TREND_METRIC_VALUES.has(raw as TrendMetric)
    ? (raw as TrendMetric)
    : STAT_CONTROL_DEFAULTS.trendMetric;
}

function parseMetric(raw: string | null, fallback: StatsMetric): StatsMetric {
  return raw !== null && METRIC_VALUES.has(raw as StatsMetric)
    ? (raw as StatsMetric)
    : fallback;
}

function parseBucket(raw: string | null): StatsBucket {
  return raw !== null && BUCKET_VALUES.has(raw as StatsBucket)
    ? (raw as StatsBucket)
    : STAT_CONTROL_DEFAULTS.bucket;
}

function parseGroupBy(raw: string | null): AggregateGroupBy {
  return raw !== null && GROUP_BY_VALUES.has(raw as AggregateGroupBy)
    ? (raw as AggregateGroupBy)
    : STAT_CONTROL_DEFAULTS.trendGroupBy;
}

function parseDimension(raw: string | null): TopDimension {
  return raw !== null && DIMENSION_VALUES.has(raw as TopDimension)
    ? (raw as TopDimension)
    : STAT_CONTROL_DEFAULTS.topDimension;
}

/** readStatControls decodes URLSearchParams into the typed control state,
 *  clamping every value to its valid enum (unknown → default, never throws). */
export function readStatControls(params: URLSearchParams): StatControls {
  return {
    trendMetric: parseTrendMetric(params.get(STAT_PARAM_KEYS.trendMetric)),
    bucket: parseBucket(params.get(STAT_PARAM_KEYS.bucket)),
    trendGroupBy: parseGroupBy(params.get(STAT_PARAM_KEYS.trendGroupBy)),
    topDimension: parseDimension(params.get(STAT_PARAM_KEYS.topDimension)),
    topMetric: parseMetric(
      params.get(STAT_PARAM_KEYS.topMetric),
      STAT_CONTROL_DEFAULTS.topMetric,
    ),
  };
}

/**
 * applyStatPatch writes the patched control(s) onto a COPY of the current
 * params, leaving every other param (the global filters, and any control not in
 * the patch) untouched. Each control is always a valid enum value, so it is
 * written verbatim; there is no "clear" gesture (a control always has a value).
 */
export function applyStatPatch(
  current: URLSearchParams,
  patch: StatControlsPatch,
): URLSearchParams {
  const next = new URLSearchParams(current);
  if (patch.trendMetric !== undefined) {
    next.set(STAT_PARAM_KEYS.trendMetric, patch.trendMetric);
  }
  if (patch.bucket !== undefined) {
    next.set(STAT_PARAM_KEYS.bucket, patch.bucket);
  }
  if (patch.trendGroupBy !== undefined) {
    next.set(STAT_PARAM_KEYS.trendGroupBy, patch.trendGroupBy);
  }
  if (patch.topDimension !== undefined) {
    next.set(STAT_PARAM_KEYS.topDimension, patch.topDimension);
  }
  if (patch.topMetric !== undefined) {
    next.set(STAT_PARAM_KEYS.topMetric, patch.topMetric);
  }
  return next;
}
