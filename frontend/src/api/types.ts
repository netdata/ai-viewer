/*
 * TypeScript mirror of the backend wire JSON. The Go structs in
 * internal/presenter/ are the contract — these types reflect the actual
 * serialized shapes (json tags, nullability, omitempty), cross-checked against
 * rest-api.md and sse-protocol.md.
 *
 * Nullability convention:
 *   - Go `*T` field with NO omitempty  -> `T | null` (key always present).
 *   - Go field with `,omitempty`        -> optional key (`?`).
 *   - Go non-pointer scalar             -> required, non-null.
 *
 * Microsecond timestamps are UNIX µs UTC (rest-api.md §Conventions).
 */

// ── Enums (canonical) ───────────────────────────────────────────────────────

// The server's enums are closed sets today, but the client treats them as OPEN
// unions: it must render an unknown future value rather than crash. `OpenEnum`
// keeps the known literals (so they autocomplete and switch-narrow) while still
// accepting any other string — `(string & {})` prevents the union from
// collapsing to plain `string` the way `T | string` would.
type OpenEnum<T extends string> = T | (string & {});

/** internal/canonical/events.go SessionStatus (known values; open union). */
export type SessionStatus = OpenEnum<
  'running' | 'completed' | 'failed' | 'abandoned' | 'interrupted'
>;

/** internal/canonical/events.go SessionKind (known values; open union). */
export type SessionKind = OpenEnum<'root' | 'sub_agent' | 'tool_internal' | 'fork'>;

/** internal/canonical/events.go OpKind (known values; open union). */
export type OpKind = OpenEnum<
  'llm' | 'tool' | 'session' | 'reasoning' | 'internal' | 'system' | 'compaction'
>;

/** log_entries.severity closed set (presenter session_logs.go validSeverities). */
export type LogSeverity = OpenEnum<'DBG' | 'INF' | 'WRN' | 'ERR'>;

/** /api/health top-level status union (observability.md). */
export type HealthStatus = 'ok' | 'degraded' | 'down';

// ── Error envelope ──────────────────────────────────────────────────────────

/** Stable machine-readable error codes (presenter errors.go). Open union so an
 *  unrecognized future code is still a usable string. */
export type ErrorCode = OpenEnum<
  | 'BAD_REQUEST'
  | 'NOT_FOUND'
  | 'CONFLICT'
  | 'INTERNAL_ERROR'
  | 'DB_UNAVAILABLE'
  | 'SERVICE_UNAVAILABLE'
  | 'SCHEMA_MISMATCH'
  | 'METHOD_NOT_ALLOWED'
  | 'TIMEOUT'
>;

export interface ErrorPayload {
  code: ErrorCode;
  message: string;
  details?: Record<string, unknown>;
}

export interface ErrorEnvelope {
  error: ErrorPayload;
}

// ── GET /api/sessions ───────────────────────────────────────────────────────

/** One row of GET /api/sessions (presenter sessionListItem). */
export interface SessionListItem {
  id: string;
  native_id: string;
  root_session_id: string;
  parent_session_id: string | null;
  source_id: string;
  kind: SessionKind;
  agent_name: string;
  model: string;
  status: SessionStatus;
  start_ts: number;
  end_ts: number | null;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  turn_count: number;
  op_count: number;
  failure_count: number;
  child_session_count: number;
}

export interface SessionListResponse {
  items: SessionListItem[];
  next_cursor?: string;
}

// ── GET /api/sessions/:id ───────────────────────────────────────────────────

/** Full session row (presenter sessionDetail). */
export interface SessionDetail {
  id: string;
  native_id: string;
  root_session_id: string;
  parent_session_id: string | null;
  source_id: string;
  kind: SessionKind;
  agent_name: string;
  model: string;
  provider: string;
  status: SessionStatus;
  error_class: string | null;
  start_ts: number;
  end_ts: number | null;
  tokens_in: number;
  tokens_out: number;
  /** Cached input tokens READ (cache-read rate); separate from tokens_in (fresh). */
  tokens_cache_read: number;
  /** Cache-CREATION tokens (cache-write rate); separate from tokens_in (fresh). */
  tokens_cache_write: number;
  cost_usd: number;
  turn_count: number;
  op_count: number;
  failure_count: number;
  child_session_count: number;
}

/**
 * One payload_refs row (presenter payloadRef). The byte-streaming route
 * (GET /api/payloads/<id>) is Phase 2 and not yet registered, so no `url`
 * field is present — only the ref metadata is surfaced today.
 */
export interface PayloadRef {
  id: number;
  kind: string;
  format: string;
  compression: string | null;
  original_bytes: number | null;
  stored_bytes: number | null;
}

/** One ops row plus its payload_refs (presenter opDetail). */
export interface OpDetail {
  id: string;
  kind: OpKind;
  name: string;
  model: string;
  provider: string;
  /** Canonical id of the op this op nests under (ops.parent_op_id); null for a
   *  top-level op. The server always emits the key (Go `*string`, no omitempty);
   *  typed optional so existing op literals/fixtures that predate the Trace view
   *  remain valid — the Trace view rebuilds the span tree from this parentage. */
  parent_op_id?: string | null;
  start_ts: number;
  end_ts: number | null;
  duration_us: number | null;
  status: string;
  error_class: string | null;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  ctx_used: number | null;
  ctx_max: number | null;
  child_session_id: string | null;
  payload_refs: PayloadRef[];
}

/** One turns row with its ordered ops (presenter turnDetail). */
export interface TurnDetail {
  id: string;
  seq: number;
  start_ts: number;
  end_ts: number | null;
  status: string;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  op_count: number;
  ops: OpDetail[];
}

/** One direct-child session summary (presenter childSummary). */
export interface ChildSummary {
  id: string;
  native_id: string;
  kind: SessionKind;
  agent_name: string;
  model: string;
  status: SessionStatus;
  start_ts: number;
  end_ts: number | null;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  op_count: number;
  failure_count: number;
}

export interface SessionDetailResponse {
  session: SessionDetail;
  turns: TurnDetail[];
  child_sessions: ChildSummary[];
}

// ── GET /api/sessions/:id/topology ──────────────────────────────────────────

/**
 * The ?metric= selector for the topology node size (presenter
 * session_topology.go parseTopologyMetric; default `duration`). Closed set on
 * the server: an ABSENT or empty value defaults to `duration`; an
 * UNKNOWN/invalid value is rejected with BAD_REQUEST (rest-api.md §GET
 * /api/sessions/:id/topology) — it does NOT silently fall back to duration.
 */
export type TopologyMetric = 'cost' | 'tokens' | 'duration' | 'calls' | 'ctx_pct';

/**
 * One node of GET /api/sessions/:id/topology (presenter topoNode). `kind`
 * distinguishes an agent (a session in the tree) from a tool. `size_metric`
 * carries the raw value of the selected ?metric= (the client normalizes the
 * node radius against `max_size_metric`); `failure_ratio` is failed/total ops
 * in 0..1 (drives node color).
 */
export interface TopologyNode {
  id: string;
  kind: OpenEnum<'agent' | 'tool'>;
  label: string;
  size_metric: number;
  failure_ratio: number;
}

/**
 * One aggregated caller→callee edge (presenter topoEdge). `calls` is the op
 * count; `total_us` is the summed duration (NULL durations counted as 0).
 */
export interface TopologyEdge {
  source: string;
  target: string;
  calls: number;
  total_us: number;
}

/**
 * Topology envelope shared by GET /api/sessions/:id/topology and GET
 * /api/topology (presenter topologyResponse). Nodes and edges are always
 * present arrays (never null). `max_size_metric` is the maximum `size_metric`
 * across nodes (0 when there are no nodes); the client normalizes node radii
 * against it. `truncated` is emitted ONLY by the cross-session /api/topology
 * route when its node cap dropped sessions (Go `omitempty` → optional key); the
 * per-session route never truncates so the key is absent there.
 */
export interface TopologyResponse {
  nodes: TopologyNode[];
  edges: TopologyEdge[];
  max_size_metric: number;
  truncated?: boolean;
}

// ── GET /api/sessions/:id/timeline ──────────────────────────────────────────

/**
 * One span on a Timeline lane (presenter timelineSpan, session_timeline.go).
 * `end_ts` is NULLABLE: null for a still-running op or a point event (the client
 * draws it as an instant marker, not a bar). `kind==='compaction'` is emitted as
 * an ordinary span; the client keys on the kind to draw a full-height breakpoint.
 */
export interface TimelineSpan {
  id: string;
  kind: OpKind;
  name: string;
  start_ts: number;
  end_ts: number | null;
  status: string;
}

/**
 * One lane = one session's ordered spans (presenter timelineLane). `spans` is
 * always a non-nil array (an op-less session serializes as []). One lane per
 * session in the resolved tree (root + children stacked).
 */
export interface TimelineLane {
  key: string;
  label: string;
  spans: TimelineSpan[];
}

/**
 * GET /api/sessions/:id/timeline envelope (presenter timelineResponse). `lanes`
 * is always present. `t_start`/`t_end` are the min start / max end across every
 * span (0/0 when the tree has no ops); a null end_ts contributes its start_ts to
 * `t_end` server-side, so the window always covers a running op's known extent.
 */
export interface TimelineResponse {
  lanes: TimelineLane[];
  t_start: number;
  t_end: number;
}

// ── GET /api/sessions/:id/logs ──────────────────────────────────────────────

/** One log entry (presenter logItem). */
export interface LogItem {
  ts: number;
  severity: LogSeverity;
  source: string;
  op_id: string | null;
  message: string;
  extras: Record<string, unknown> | null;
}

export interface LogsResponse {
  items: LogItem[];
  next_cursor?: string;
}

// ── GET /api/stats ──────────────────────────────────────────────────────────

export interface StatsTotals {
  session_count: number;
  turn_count: number;
  op_count: number;
  tokens_in: number;
  tokens_out: number;
  /** Cached input tokens READ (cache-read rate); separate from tokens_in (fresh). */
  tokens_cache_read: number;
  /** Cache-CREATION tokens (cache-write rate); separate from tokens_in (fresh). */
  tokens_cache_write: number;
  cost_usd: number;
  failures: number;
  duration_us: number;
}

export interface StatModelRow {
  name: string;
  provider: string;
  calls: number;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  failures: number;
  pct_of_cost: number;
}

export interface StatToolRow {
  namespace: string;
  name: string;
  calls: number;
  failures: number;
  total_us: number;
  pct_of_calls: number;
}

export interface StatAgentRow {
  name: string;
  sessions: number;
  failures: number;
  tokens_in: number;
  tokens_out: number;
  cost_usd: number;
  pct_of_sessions: number;
}

export interface StatStatusRow {
  status: SessionStatus;
  count: number;
}

export interface StatSourceRow {
  source: string;
  sessions: number;
  failures: number;
}

export interface StatsResponse {
  totals: StatsTotals;
  by_model: StatModelRow[];
  by_tool: StatToolRow[];
  by_agent: StatAgentRow[];
  by_status: StatStatusRow[];
  by_source: StatSourceRow[];
}

// ── GET /api/sources ────────────────────────────────────────────────────────

/** One row of GET /api/sources (presenter sourceItem). */
export interface SourceItem {
  id: string;
  format: string;
  location: string;
  enabled: boolean;
  parse_errors: number;
  last_seen_at: number | null;
  created_at: number;
  cursor: string;
  last_seq: number;
  last_ts_us: number | null;
  updated_at: number | null;
}

export interface SourcesResponse {
  items: SourceItem[];
}

// ── GET /api/health ─────────────────────────────────────────────────────────

/** Per-source health row (presenter healthSource). */
export interface HealthSource {
  id: string;
  format: string;
  location: string;
  enabled: boolean;
  last_seen_at: number | null;
  lag_us: number;
  parse_errors: number;
  last_seq: number;
}

export interface HealthNotify {
  last_seq: number;
  lag_us: number;
}

export interface HealthSSE {
  subscriptions: number;
}

export interface HealthResponse {
  status: HealthStatus;
  version: string;
  schema_version: number;
  uptime_s: number;
  db_path: string;
  db_size_bytes: number;
  sources: HealthSource[];
  notify: HealthNotify;
  sse: HealthSSE;
}

// ── POST /api/subscriptions ─────────────────────────────────────────────────

/** {from,to} sub-object; both bounds optional UNIX µs (presenter timeRangeJSON). */
export interface TimeRangeJSON {
  from?: number | null;
  to?: number | null;
}

/** Wire shape of the subscription filter request (presenter subscriptionFilterJSON).
 *  All fields optional; omitted = no constraint. */
export interface SubscriptionFilterRequest {
  time_range?: TimeRangeJSON;
  sources?: string[];
  agents?: string[];
  models?: string[];
  tools?: string[];
  status?: string[];
  session_id?: string | null;
  root_session_id?: string | null;
}

export interface SubscriptionRequest {
  filter?: SubscriptionFilterRequest;
}

/** Normalized filter echoed back; omitted dimensions are absent
 *  (presenter normalizedFilter, all fields omitempty). */
export interface NormalizedFilter {
  time_range?: TimeRangeJSON;
  sources?: string[];
  agents?: string[];
  models?: string[];
  tools?: string[];
  status?: string[];
  session_id?: string;
  root_session_id?: string;
}

export interface CreateSubscriptionResponse {
  id: string;
  filter_normalized: NormalizedFilter;
}

// ── SSE event frames (sse-protocol.md §Event Types) ─────────────────────────

export interface SessionChangedEvent {
  session_id: string;
  root_session_id: string;
  ts: number;
  /** Present only when > 0: client missed `dropped` events, should re-fetch. */
  dropped?: number;
}

export interface StatsInvalidatedEvent {
  ts: number;
}

export interface SourceStatusChangedEvent {
  source_id: string;
  ts: number;
}

export interface DisconnectEvent {
  reason: string;
  retry_after_ms: number;
}

export interface ResyncEvent {
  reason: string;
}
