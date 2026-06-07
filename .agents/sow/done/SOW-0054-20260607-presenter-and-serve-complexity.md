# SOW-0054 - Presenter and Serve Complexity Reduction

## Status

Status: completed

Sub-state: delivered. SOW-0054 was split from SOW-0050 residual backend scan
and completed the presenter and serve command maintainability slice.

## Requirements

### Purpose

Reduce presenter HTTP/static/SSE/query complexity and serve command complexity
while preserving current REST, SSE, embedded-asset, and UI-serving behavior.

### User Request

Continue maintainability cleanup autonomously, SOW by SOW, with tests, security
checks, and external review before completion.

### Assistant Understanding

Facts:

- SOW-0050's post-worker strict backend/CLI scan left 25 warnings in
  `internal/presenter` and 2 warnings in `cmd/ai-viewer-serve`.
- The presenter warning set includes request handlers, filters, search, stats,
  source/session/log endpoints, middleware, embedded asset serving, topology,
  timeline, health, and SSE handling.
- `internal/presenter/events_sse.go` and `internal/notify/subscription.go` are
  protocol-sensitive and need stronger replay/shutdown characterization before
  refactoring.

Inferences:

- The safest order is to start with lower-protocol-risk request/asset helpers,
  then handle SSE/replay only after focused tests pin reconnect, `Last-Event-ID`,
  busy-stream, and shutdown behavior.

Unknowns:

- Which presenter warnings are already intentionally dense query builders must
  be decided after reading current tests and specs.

### Acceptance Criteria

- Presenter and serve warnings are ranked by protocol/security/user-facing risk
  with file/function evidence.
- Each selected slice has focused tests before implementation.
- REST/SSE/static asset behavior, HTTP status codes, headers, compression, and
  query parameter semantics remain unchanged.
- Remaining warnings are justified in this SOW or split further.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0050 declared backend/CLI strict Lizard scan after the ingest worker slice.

Current state:

- Presenter has the largest remaining backend warning count and covers public
  HTTP surfaces, so it should not be mixed into lower-level store/pricing work.

Risks:

- Handler regressions can change API payloads, status codes, compression,
  static-asset caching, or live-event delivery.
- SSE/replay regressions can create duplicate or missing events for connected
  browsers.
- Verified risk ranking for the first implementation pass:
  - Highest risk: `internal/presenter/events_sse.go:45` `handleEvents` because
    replay, reconnect, duplicate-stream, shutdown, and backpressure behavior are
    protocol-sensitive.
  - High risk: SQL/filter/search/stats/log pagination/cross-agent topology
    helpers because they combine query semantics with response shaping.
  - Medium risk: health/sources/session detail/timeline/topology builders
    because mistakes change visible JSON payloads.
  - Lowest risk: embedded asset serving and gzip negotiation because focused
    tests already pin status, headers, cache policy, content type, HEAD parity,
    gzip decisions, and traversal rejection.

## Pre-Implementation Gate

Status: completed; final gates are green and final external review converged.

Problem / root-cause model:

- Presenter code combines HTTP parsing, SQL/filter composition, response
  shaping, and transport details in several large functions. Serve command
  startup also carries orchestration complexity.

Evidence reviewed:

- SOW-0050 warning inventory:
  `internal/presenter/*` (25 warnings) and `cmd/ai-viewer-serve/main.go`
  (2 warnings).
- Current production warning inventory from
  `find internal/presenter cmd/ai-viewer-serve -name '*.go' ! -name '*_test.go' -print | sort | xargs lizard -l go -C 8 -L 50 -a 8 -w`:
  - `cmd/ai-viewer-serve/main.go:69` `run`.
  - `cmd/ai-viewer-serve/main.go:321` `serveHTTP`.
  - `internal/presenter/embed.go:155` `servePublicFile`.
  - `internal/presenter/embed.go:211` `serveAsset`.
  - `internal/presenter/embed.go:288` `contentTypeForAsset`.
  - `internal/presenter/events_sse.go:45` `handleEvents`.
  - `internal/presenter/filters.go:153` `parseScalarFilters`.
  - `internal/presenter/filters.go:371` `dimensionConds`.
  - `internal/presenter/health.go:98` `handleHealth`.
  - `internal/presenter/health.go:174` `collectSources`.
  - `internal/presenter/middleware.go:230` `gzipMiddleware`.
  - `internal/presenter/middleware.go:231` anonymous nested gzip middleware
    closure.
  - `internal/presenter/middleware.go:232` anonymous nested gzip response
    closure.
  - `internal/presenter/middleware.go:315` `parseAcceptWeight`.
  - `internal/presenter/presenter.go:116` `New`.
  - `internal/presenter/search.go:85` `handleSearch`.
  - `internal/presenter/search.go:352` `searchLogs`.
  - `internal/presenter/session_detail.go:120` `handleSessionDetail`.
  - `internal/presenter/session_logs.go:50` `handleSessionLogs`.
  - `internal/presenter/session_logs.go:166` `parseLogPaging`.
  - `internal/presenter/session_timeline.go:144` `attachTimelineSpans`.
  - `internal/presenter/session_topology_builder.go:92` `observeOp`.
  - `internal/presenter/session_topology_builder.go:159` `finish`.
  - `internal/presenter/sources.go:45` `handleSources`.
  - `internal/presenter/stats.go:79` `handleStats`.
  - `internal/presenter/stats_top.go:30` `handleStatsTop`.
  - `internal/presenter/topology_cross.go:168` `loadCrossAgents`.

Affected contracts and surfaces:

- REST handlers, query filters, search, stats, source/session/log endpoints,
  topology/timeline builders, gzip negotiation, embedded asset serving, health,
  SSE transport, and the `ai-viewer-serve` command.

Existing patterns to reuse:

- Existing presenter package tests, HTTP response helpers, filter tests, SSE
  tests, embedded-asset tests, and Playwright/API coverage where relevant.

Risk and blast radius:

- Medium to high because these are user-facing and HTTP-facing surfaces. No
  SQLite schema, adapter, ingest write path, or frontend design change is
  expected.

## Spec Deltas

- `.agents/sow/specs/presenter.md`: no target behavior change expected. This
  SOW is a behavior-preserving decomposition of presenter/serve internals; the
  spec will be manually audited after implementation and updated only if code
  review finds the current prose incomplete or stale.
- `.agents/sow/specs/rest-api.md`: no target API shape, status-code, header, or
  query-param semantic change expected.
- `.agents/sow/specs/sse-protocol.md`: no target wire-contract change expected.
  SSE code is high risk and must only be refactored after focused replay,
  busy-stream, disconnect, and shutdown tests prove the current behavior.

Sensitive data handling plan:

- Use synthetic stores and sanitized fixtures only. Do not add real prompts,
  tool output, private paths, secrets, personal data, or private endpoints.

Implementation plan:

1. Rank presenter/serve warnings by risk and existing coverage.
2. Start with lower-protocol-risk static/middleware/serve command helpers.
3. Refactor request parsing and response-shaping handlers only after focused
   tests pin status codes, headers, and error envelopes.
4. Treat SSE/replay as a high-risk slice: do not touch `handleEvents` until
   replay, `Last-Event-ID`, busy-stream, duplicate-stream, `HEAD`, and shutdown
   behavior are pinned by focused tests.
5. Refactor in package-local helpers while preserving public behavior.
6. Validate with focused tests, strict Lizard, local Codacy, full gates, and
   external review. Any residual warnings must be justified here or split into a
   new pending SOW before completion.

Validation plan:

- Focused presenter and serve tests selected after coverage audit:
  `internal/presenter/embed*_test.go`, `internal/presenter/middleware*_test.go`,
  `internal/presenter/health_test.go`, `internal/presenter/sources_test.go`,
  `internal/presenter/search*_test.go`, `internal/presenter/session_*_test.go`,
  `internal/presenter/stats*_test.go`, `internal/presenter/events_sse*_test.go`,
  and `cmd/ai-viewer-serve/main_test.go` depending on the touched slice.
- First slice focused command:
  `go test ./internal/presenter -run '^(TestContentTypeForAsset|TestContentTypeForAsset_MoreExtensions|TestLowerExt|TestServePublicFile_|TestServeAsset_|TestSafeAssetPath_RejectsTraversal|TestRoot_|TestServeIndex_|TestGzipMiddleware|TestClientAcceptsGzip|TestParseAcceptWeight)' -count=1`.
  Because this slice is behavior-preserving, new tests are characterization or
  package-local helper-contract tests for extracted helpers; public behavior
  tests must pass before and after the refactor.
- `go test ./internal/presenter ./cmd/ai-viewer-serve -count=1`
- Race tests for touched presenter/SSE paths; if SSE is touched, run the focused
  SSE tests with `-race -count=10`.
- Direct strict Lizard on changed files.
- Local Codacy analysis on changed files.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: presenter/API/SSE/static-serving specs only if behavior contracts
  change; otherwise record unchanged attestations.
- Runtime project skills: likely unaffected unless a new presenter
  decomposition convention emerges.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- This is internal presenter maintainability work. External references are
  required only if a selected slice changes protocol/library behavior.

Open decisions:

- None for the operator.

## Implementation

### Slice 1 - static asset and gzip negotiation complexity

Status: completed and locally verified.

Files changed:

- `internal/presenter/embed.go`
- `internal/presenter/middleware.go`
- `internal/presenter/embed_helpers_test.go`
- `internal/presenter/middleware_helpers_test.go`

Changes:

- Split root public-file serving, asset-file serving, content-type lookup, gzip
  bypass/response decisions, gzip response writing, and `Accept-Encoding`
  q-value parsing into package-local helpers.
- Added helper-contract and characterization tests for the extracted helpers.
- Preserved behavior for status codes, JSON error envelopes, cache headers,
  content types, HEAD empty-body parity, gzip decisions, and traversal
  rejection.

Validation:

- Focused static/gzip command passed:
  `go test ./internal/presenter -run '^(TestContentTypeForAsset|TestContentTypeForAsset_MoreExtensions|TestLowerExt|TestServePublicFile_|TestServeAsset_|TestSafeAssetPath_RejectsTraversal|TestRoot_|TestServeIndex_|TestGzipMiddleware|TestClientAcceptsGzip|TestParseAcceptWeight)' -count=1`.
- Focused static/gzip command with race detector and helper tests passed:
  `go test -race ./internal/presenter -run '^(TestContentTypeForAsset|TestContentTypeForAsset_MoreExtensions|TestLowerExt|TestServePublicFile_|TestServeAsset_|TestSafeAssetPath_RejectsTraversal|TestRoot_|TestServeIndex_|TestGzipMiddleware|TestClientAcceptsGzip|TestParseAcceptWeight|TestPublicRootFileName|TestStaticFileMethodAllowed|TestAssetContentTypeForExt|TestShouldBypassGzip|TestShouldGzipBufferedResponse|TestParseHTTPQValue|TestAcceptWeightParam)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- Presenter plus serve command tests passed:
  `go test ./internal/presenter ./cmd/ai-viewer-serve -count=1`.
- `gofmt -l` on touched files produced no output.
- `git diff --check` on touched files and this SOW produced no output.
- `rg -n "TODO|FIXME|t\\.Skip|//\\s*nolint"` on touched files produced no
  matches.
- Strict Lizard on touched production files produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/embed.go internal/presenter/middleware.go`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 27 to 20.
- All `internal/presenter/embed.go` and `internal/presenter/middleware.go`
  warnings from this SOW are cleared.
- Remaining warnings are in `cmd/ai-viewer-serve/main.go`,
  `internal/presenter/events_sse.go`, `filters.go`, `health.go`,
  `presenter.go`, `search.go`, `session_detail.go`, `session_logs.go`,
  `session_timeline.go`, `session_topology_builder.go`, `sources.go`,
  `stats.go`, `stats_top.go`, and `topology_cross.go`.

### Slice 2 - shared filter parser and SQL condition builder complexity

Status: completed and locally verified.

Files changed:

- `internal/presenter/filters.go`
- `internal/presenter/filters_helpers_test.go`

Changes:

- Split scalar query validation into small helpers for `group`, `sort`,
  `order`, and `limit`.
- Split session dimension condition assembly into a package-local builder that
  preserves fixed SQL fragments and binds all operator values as args.
- Added helper-contract and exact condition/arg-order tests.

Validation:

- Focused filter tests passed:
  `go test ./internal/presenter -run '^(TestParseSessionFilter|TestSessionFilter|TestFilterScalar|TestDimension|TestWhereClause)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- Presenter package race tests passed: `go test -race ./internal/presenter -count=1`.
- `gofmt -l` on touched filter files produced no output.
- `git diff --check` on touched filter files produced no output.
- `rg -n "TODO|FIXME|t\\.Skip|//\\s*nolint"` found only the pre-existing
  `filters.go` `//nolint:unparam` suppression; no new suppression was added.
- Strict Lizard on `filters.go` produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/filters.go`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 20 to 18 in this slice.
- All `internal/presenter/filters.go` warnings from this SOW are cleared.
- Remaining warnings are in `cmd/ai-viewer-serve/main.go`,
  `internal/presenter/events_sse.go`, `health.go`, `presenter.go`,
  `search.go`, `session_detail.go`, `session_logs.go`,
  `session_timeline.go`, `session_topology_builder.go`, `sources.go`,
  `stats.go`, `stats_top.go`, and `topology_cross.go`.

### Slice 3 - health and sources handler complexity

Status: completed and locally verified.

Files changed:

- `internal/presenter/health.go`
- `internal/presenter/sources.go`
- `internal/presenter/health_helpers_test.go`
- `internal/presenter/sources_helpers_test.go`

Changes:

- Split health response assembly, status decision, source row scan, source lag
  mapping, `/api/sources` row loading, nullable field mapping, and source error
  response mapping into package-local helpers.
- Added helper-contract tests for health source mapping/status rules and source
  item nullable-field mapping.
- Preserved JSON field names, HTTP status behavior, health status rules,
  request-id logging on DB errors, `last_seq` semantics, `HEAD` parity, and
  source/source_progress left-join behavior.

Validation:

- Focused health/sources tests passed:
  `go test ./internal/presenter -run '^(TestHealth|TestSources|TestHEAD_DeferredRouteReturns404WithEmptyBody|TestWriteJSONErrorHEADHasEmptyBody)' -count=1`.
- Focused health/sources race tests passed:
  `go test -race ./internal/presenter -run '^(TestHealth|TestSources|TestHEAD_DeferredRouteReturns404WithEmptyBody|TestWriteJSONErrorHEADHasEmptyBody)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- `gofmt -l` on touched health/sources files produced no output.
- `git diff --check` on touched health/sources files produced no output.
- `rg -n "TODO|FIXME|t\\.Skip|//\\s*nolint"` on touched health/sources files
  produced no matches.
- Strict Lizard on health/sources production files produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/health.go internal/presenter/sources.go`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 18 to 15 in this slice.
- All `internal/presenter/health.go` and `internal/presenter/sources.go`
  warnings from this SOW are cleared.
- Remaining warnings are in `cmd/ai-viewer-serve/main.go`,
  `internal/presenter/events_sse.go`, `presenter.go`, `search.go`,
  `session_detail.go`, `session_logs.go`, `session_timeline.go`,
  `session_topology_builder.go`, `stats.go`, `stats_top.go`, and
  `topology_cross.go`.

### Slice 4 - presenter constructor complexity

Status: completed and locally verified.

Files changed:

- `internal/presenter/presenter.go`
- `internal/presenter/presenter_constructor_test.go`

Changes:

- Split presenter constructor default resolution into package-local helpers for
  logger, clock, started-at, schema version, hub, and notify poll interval.
- Added constructor helper tests and a wiring test that verifies resolved
  defaults plus hub removal cleanup of both subscription registry and
  `statsCoalesce` state.
- Preserved nil-DB validation, default logger subsystem field, UTC default
  clock, default schema version, default hub, default notify interval,
  `defaultSSEKeepalive`, `notifyNow`, subscription manager wiring,
  `statsCoalesce` initialization, and `hub.SetOnRemove(p.onSubRemoved)`.

Validation:

- Constructor helper tests passed:
  `go test ./internal/presenter -run '^(TestNewResolvePresenter|TestNewWiresResolvedDefaultsAndHubCleanup)$' -count=1`.
- Focused presenter constructor/schema/SSE/subscription tests passed:
  `go test ./internal/presenter -run '^(TestNew|TestPresenter|TestCheckSchema|TestSSE|TestSubscription)' -count=1`.
- Focused constructor/schema/SSE/subscription race tests passed:
  `go test -race ./internal/presenter -run '^(TestNewResolvePresenter|TestNewWiresResolvedDefaultsAndHubCleanup|TestNew|TestPresenter|TestCheckSchema|TestSSE|TestSubscription)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- `gofmt -l` on touched constructor files produced no output.
- `git diff --check` on touched constructor files produced no output.
- `rg -n "TODO|FIXME|t\\.Skip|//\\s*nolint"` on touched constructor files
  produced no matches.
- Strict Lizard on `presenter.go` produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/presenter.go`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 15 to 14 in this slice.
- The `internal/presenter/presenter.go` constructor warning from this SOW is
  cleared.
- Remaining warnings are in `cmd/ai-viewer-serve/main.go`,
  `internal/presenter/events_sse.go`, `search.go`, `session_detail.go`,
  `session_logs.go`, `session_timeline.go`, `session_topology_builder.go`,
  `stats.go`, `stats_top.go`, and `topology_cross.go`.

### Slice 5 - session detail and logs handler complexity

Status: completed and locally verified.

Files changed:

- `internal/presenter/session_detail.go`
- `internal/presenter/session_logs.go`
- `internal/presenter/session_helpers_test.go`

Changes:

- Reused the existing session path-id validation helper for detail and logs,
  preserving raw control-byte rejection before trimming.
- Split session detail response loading and session logs request parsing,
  limit parsing, cursor parsing/validation, and logs response loading into
  package-local helpers.
- Added helper-contract tests for detail/logs response loaders, log request
  parsing, limit validation, and cursor validation.
- Preserved detail/logs response shapes, `HEAD`/405 behavior, unknown-session
  `NOT_FOUND`, DB error handling, severity filter semantics, keyset cursor
  binding, and parameterized query behavior.

Validation:

- Focused session detail/logs tests passed:
  `go test ./internal/presenter -run '^(TestSessionDetail|TestSessionLogs|TestParseLog|TestLogPaging|TestLogsPage|TestDecodeExtras)' -count=1`.
- Focused session detail/logs race tests passed:
  `go test -race ./internal/presenter -run '^(TestSessionDetail|TestSessionLogs|TestParseLog|TestLogPaging|TestLogsPage|TestDecodeExtras)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- Presenter package race tests passed: `go test -race ./internal/presenter -count=1`.
- `gofmt -l` on touched session files produced no output.
- `git diff --check` on touched session files produced no output.
- `rg -n "TODO|FIXME|t\\.Skip|//\\s*nolint"` on touched session files
  produced no matches.
- Strict Lizard on touched session production files produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/session_detail.go internal/presenter/session_logs.go`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 14 to 11 in this slice.
- All `internal/presenter/session_detail.go` and
  `internal/presenter/session_logs.go` warnings from this SOW are cleared.
- Remaining warnings are in `cmd/ai-viewer-serve/main.go`,
  `internal/presenter/events_sse.go`, `search.go`,
  `session_timeline.go`, `session_topology_builder.go`, `stats.go`,
  `stats_top.go`, and `topology_cross.go`.

### Slice 6 - session timeline and topology builder complexity

Status: completed and locally verified.

Files changed:

- `internal/presenter/session_timeline.go`
- `internal/presenter/session_topology_builder.go`
- `internal/presenter/session_timeline_helpers_test.go`
- `internal/presenter/session_topology_builder_test.go`

Changes:

- Split timeline span bound tracking, SQL row decoding, null-end handling, and
  defensive lane attachment into package-local helpers.
- Split topology agent metric accumulation, `ctx_pct` maximum tracking, node
  materialization, maximum size tracking, and defensive dangling-edge filtering
  into package-local helpers.
- Added helper-contract tests for extracted timeline bounds/lane attachment and
  topology metric/node helpers.
- Preserved SQL ordering, response JSON shape, topology metric semantics, lane
  attachment behavior, and defensive out-of-tree child edge dropping.

Validation:

- Focused topology/timeline tests passed:
  `go test ./internal/presenter -run '^(TestTopology|TestTimeline|TestTopo|TestAttachTimeline|TestToolSize|TestAgentLabel)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- Presenter package race tests passed: `go test -race ./internal/presenter -count=1`.
- `gofmt -l` on touched topology/timeline files produced no output.
- `git diff --check` on touched topology/timeline files produced no output.
- Marker scan for TODOs, skipped tests, lint suppressions, personal-name
  leakage, and tool-attribution leakage on touched topology/timeline files
  produced no matches.
- Strict Lizard on touched topology/timeline production files produced no
  warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/session_timeline.go internal/presenter/session_topology_builder.go`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 11 to 8 in this slice.
- All `internal/presenter/session_timeline.go` and
  `internal/presenter/session_topology_builder.go` warnings from this SOW are
  cleared.
- Remaining production warnings:
  - `cmd/ai-viewer-serve/main.go:69` `run`.
  - `cmd/ai-viewer-serve/main.go:321` `serveHTTP`.
  - `internal/presenter/events_sse.go:45` `handleEvents`.
  - `internal/presenter/search.go:85` `handleSearch`.
  - `internal/presenter/search.go:352` `searchLogs`.
  - `internal/presenter/stats.go:79` `handleStats`.
  - `internal/presenter/stats_top.go:30` `handleStatsTop`.
  - `internal/presenter/topology_cross.go:168` `loadCrossAgents`.

### Slice 7 - search handler and log search complexity

Status: completed and locally verified.

Files changed:

- `internal/presenter/search.go`
- `internal/presenter/search_helpers_test.go`

Changes:

- Split search request parsing, response loading, log SQL assembly, nullable
  `op_id` mapping, and `limit+1` log page trimming into package-local helpers.
- Added helper-contract tests for search-owned query params, cursor binding,
  parameterized log SQL, nullable log `op_id`, and log page trimming.
- Preserved FTS5 `MATCH` parameter binding, all-session search scope, cursor
  fingerprint behavior, `logs_indexed` semantics, deterministic log ordering,
  status/error mapping, and response shape.

Validation:

- Focused search tests passed:
  `go test ./internal/presenter -run '^(TestSearch|TestSearchHelper|TestSearchResponse|TestSearchLog)' -count=1`.
- Focused search race tests passed:
  `go test -race ./internal/presenter -run '^(TestSearch|TestSearchHelper|TestSearchResponse|TestSearchLog)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- `gofmt -l` on touched search files produced no output.
- `git diff --check` on touched search files produced no output.
- Marker scan for TODOs, skipped tests, lint suppressions, personal-name
  leakage, and tool-attribution leakage on touched search files produced no
  matches.
- Strict Lizard on `search.go` produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/search.go`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 8 to 6 in this slice.
- All `internal/presenter/search.go` warnings from this SOW are cleared.

### Slice 8 - stats and stats-top handler complexity

Status: completed and locally verified.

Files changed:

- `internal/presenter/stats.go`
- `internal/presenter/stats_top.go`
- `internal/presenter/stats_helpers_test.go`
- `internal/presenter/stats_top_helpers_test.go`

Changes:

- Split stats response initialization, filter scope construction, ordered stats
  query execution, stats-top request parsing, stats-top parse-error mapping, and
  top response limiting into package-local helpers.
- Added helper-contract tests for non-nil breakdown slices, parameterized stats
  scope, first failing query-stage reporting, stats-top defaults/all-session
  scope, bad-param classification, and top response sorting/limiting.
- Preserved `/api/stats` live summary behavior, `/api/stats/top` rollup/live
  aggregate behavior, HEAD/405/bad-param behavior, `n` clamp semantics, error
  envelopes, and response shapes.

Validation:

- Focused stats tests passed:
  `go test ./internal/presenter -run '^(TestStats|TestStatsTop|TestTop|TestStatsHandler|TestStatsResponse)' -count=1`.
- Focused stats race tests passed:
  `go test -race ./internal/presenter -run '^(TestStats|TestStatsTop|TestTop|TestStatsHandler|TestStatsResponse)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- `gofmt -l` on touched stats files produced no output.
- `git diff --check` on touched stats files produced no output.
- Marker scan for TODOs, skipped tests, lint suppressions, personal-name
  leakage, and tool-attribution leakage on touched stats files produced no
  matches.
- Strict Lizard on touched stats production files produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/stats.go internal/presenter/stats_top.go`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 6 to 4 in this slice.
- All `internal/presenter/stats.go` and `internal/presenter/stats_top.go`
  warnings from this SOW are cleared.

### Slice 9 - cross-session topology loader complexity

Status: completed and locally verified.

Files changed:

- `internal/presenter/topology_cross.go`
- `internal/presenter/topology_cross_helpers_test.go`

Changes:

- Split cross-agent SQL assembly, row scanning, row-to-node mapping, and
  `maxTopologyNodes+1` truncation into package-local helpers.
- Added helper-contract tests for parameterized query assembly, label/metric
  mapping, and truncation behavior.
- Preserved all-session scope, top-N node cap, truncation flag, metric
  expressions, failure ratio mapping, lineage-edge behavior, SQL parameter
  binding, and response shape.

Validation:

- Focused cross-topology tests passed:
  `go test ./internal/presenter -run '^(TestCrossTopology|TestTopologyCross|TestCrossAgent|TestLoadCross)' -count=1`.
- Focused cross-topology race tests passed:
  `go test -race ./internal/presenter -run '^(TestCrossTopology|TestTopologyCross|TestCrossAgent|TestLoadCross)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- `gofmt -l` on touched cross-topology files produced no output.
- `git diff --check` on touched cross-topology files produced no output.
- Marker scan for TODOs, skipped tests, lint suppressions, personal-name
  leakage, and tool-attribution leakage on touched cross-topology files
  produced no matches.
- Strict Lizard on `topology_cross.go` produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/topology_cross.go`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 4 to 3 in this slice.
- The `internal/presenter/topology_cross.go` warning from this SOW is cleared.

### Slice 10 - serve command startup and graceful-shutdown complexity

Status: completed and locally verified.

Files changed:

- `cmd/ai-viewer-serve/main.go`
- `cmd/ai-viewer-serve/main_test.go`

Changes:

- Split serve runtime construction, presenter construction, HTTP server
  configuration, listener error normalization, notify-poller lifecycle, and
  graceful-shutdown sequencing into package-local helpers.
- Added helper-characterization tests for SSE-safe HTTP server timeouts, server
  close-error normalization, notify-poller cancellation/drain, and graceful
  shutdown ordering.
- Preserved CLI exit-code behavior, localhost-only bind validation, read-only
  store open, schema check, embedded frontend wiring, notify poller ownership,
  shutdown order, and listener error mapping.

Validation:

- Serve command tests passed: `go test ./cmd/ai-viewer-serve -count=1`.
- Focused serve command tests passed:
  `go test ./cmd/ai-viewer-serve -run '^(TestParseFlags|TestAssertLocalhost|TestEmbeddedFrontend|TestServe|TestRun|TestHTTPServer)' -count=1`.
- Serve command race tests passed:
  `go test -race ./cmd/ai-viewer-serve -count=1`.
- Presenter plus serve package tests passed:
  `go test ./internal/presenter ./cmd/ai-viewer-serve -count=1`.
- `gofmt -l` on touched serve files produced no output.
- `git diff --check` on touched serve files produced no output.
- Marker scan for TODOs, skipped tests, lint suppressions, personal-name
  leakage, and tool-attribution leakage on touched serve files produced no
  matches.
- Strict Lizard on `cmd/ai-viewer-serve/main.go` produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w cmd/ai-viewer-serve/main.go`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 3 to 1 in this slice.
- All `cmd/ai-viewer-serve/main.go` warnings from this SOW are cleared.
- Remaining production warning:
  - `internal/presenter/events_sse.go:45` `handleEvents`.

### Slice 11 - SSE event stream handler complexity

Status: completed and locally verified after the method-gate correction below.

Files changed:

- `internal/presenter/events_sse.go`
- `internal/presenter/events_sse_helpers_test.go`

Changes:

- Split SSE stream attach/status mapping, stream header start, replay/resync
  prelude, and initial resync writing into package-local helpers.
- Added helper-characterization tests for non-flusher rejection before
  `hub.Attach`, unknown-sub 404 mapping, busy-sub 409 mapping, and successful
  attach without writing the stream response.
- Preserved the exact SSE wire/lifecycle contract: GET/HEAD method behavior,
  non-mutating HEAD, single active stream per subscription, attach-only detach,
  header flush, write-deadline clearing, Last-Event-ID resync/replay, keepalive,
  write-error exit, hub shutdown exit, and graceful `disconnect` broadcast.

Validation:

- Helper tests passed:
  `go test ./internal/presenter -run '^TestAttachEventStream_' -count=1`.
- Focused SSE tests passed:
  `go test ./internal/presenter -run '^(TestEvents|TestSSE|TestShutdownSSE|TestStreamLoop|TestWriteResync|TestFanOut|TestSubscriptionsCreate_ShutdownRace)' -count=1`.
- Focused SSE race tests passed:
  `go test -race ./internal/presenter -run '^(TestEvents|TestSSE|TestShutdownSSE|TestStreamLoop|TestWriteResync|TestFanOut|TestSubscriptionsCreate_ShutdownRace)' -count=1`.
- Focused SSE race stress passed:
  `go test -race ./internal/presenter -run '^(TestEvents|TestSSE|TestShutdownSSE|TestStreamLoop|TestWriteResync|TestFanOut|TestSubscriptionsCreate_ShutdownRace)' -count=10`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- `gofmt -l` on touched SSE files produced no output.
- `git diff --check` on touched SSE files produced no output.
- Marker scan for TODOs, skipped tests, lint suppressions, personal-name
  leakage, and tool-attribution leakage on touched SSE files produced no
  matches.
- Strict Lizard on `events_sse.go` produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/events_sse.go`.
- Full SOW production warning scan produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w -x "*_test.go" cmd/ai-viewer-serve internal/presenter`.

Current remaining production warning inventory:

- SOW-0054 warning count reduced from 1 to 0 in this slice.
- All SOW-0054 target production warnings are cleared.

### Corrective gate slice - REST method guard visibility

Status: completed and locally verified.

Trigger:

- The first full `./scripts/gates.sh` run failed at `spec-drift self-test`.
- Root cause: the behavior-preserving helper extraction made three registered
  route method guards invisible to `scripts/spec-drift.sh`, which intentionally
  fails closed when it cannot extract each handler's `r.Method` gate:
  `handleSessionDetail`, `handleSessionLogs`, and `handleEvents`.

Changes:

- Restored direct in-handler `r.Method != http.MethodGet &&
  r.Method != http.MethodHead` guards in `handleSessionDetail`,
  `handleSessionLogs`, and `handleEvents`.
- Kept extracted helper decomposition for response loading, stream attach,
  stream start, and replay/resync behavior.
- Removed the SSE method-gate helper path from the SOW slice description
  because registered REST/SSE handlers must keep method guards visible to the
  drift checker.

Validation:

- Targeted method/SSE helper tests passed:
  `go test ./internal/presenter -run '^(TestSessionDetail_MethodNotAllowed|TestEvents_MethodGate|TestEvents_HeadReturnsHeadersNoStream|TestAttachEventStream_)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- Live spec drift detector passed:
  `scripts/spec-drift.sh`.
- Spec drift self-test passed:
  `scripts/test/spec-drift-test.sh` (`26 passed, 0 failed`).
- Strict Lizard on touched production files produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/events_sse.go internal/presenter/session_detail.go internal/presenter/session_logs.go`.

Durable lesson:

- For registered `mux.HandleFunc("/api/...", p.<handler>)` endpoints, keep the
  HTTP method guard as direct `r.Method != http.Method...` comparisons inside
  the registered handler body. Extracting that guard into a helper weakens the
  checker evidence path and correctly fails `spec-drift`.

### Corrective Codacy slice - helper test complexity

Status: completed and locally verified.

Trigger:

- `codacy-analysis analyze --diff` resolved to zero files in the uncommitted
  local branch state, so an explicit changed-file scan was run instead.
- The first explicit Codacy scan reported 11 warning-level Lizard findings in
  newly added presenter helper test files: 8 medium cyclomatic-complexity
  warnings and 3 medium NLOC warnings. Trivy, Semgrep/Opengrep, Agentlinter,
  and markdownlint were clean.

Changes:

- Split branchy or oversized helper tests in:
  `events_sse_helpers_test.go`, `health_helpers_test.go`,
  `presenter_constructor_test.go`, `search_helpers_test.go`,
  `session_helpers_test.go`, `sources_helpers_test.go`, and
  `stats_top_helpers_test.go`.
- Replaced branchy hand-written comparison helpers with focused `cmp.Diff`
  helpers where that produces clearer failures.
- Preserved test coverage and production behavior.

Validation:

- Focused helper tests passed:
  `go test ./internal/presenter -run '^(TestAttachEventStream_StatusMapping|TestHealthBuildSource|TestNewWiresResolvedDefaultsAndHubCleanup|TestSearchHelperParseRequestOwnsSearchParams|TestParseLogRequest_DefaultsAndSeverity|TestLogPagingCursorParamValidation|TestSourcesBuildItem|TestParseStatsTopRequestDefaultsAndForcesAllSessions)' -count=1`.
- Presenter package tests passed: `go test ./internal/presenter -count=1`.
- Strict Lizard on the seven edited helper test files produced no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/presenter/events_sse_helpers_test.go internal/presenter/health_helpers_test.go internal/presenter/presenter_constructor_test.go internal/presenter/search_helpers_test.go internal/presenter/session_helpers_test.go internal/presenter/sources_helpers_test.go internal/presenter/stats_top_helpers_test.go`.
- Codacy explicit changed-file analysis passed with 0 issues and 0 tool errors:
  `codacy-analysis analyze --files <SOW-0054 changed file set> --parallel-tools 4 --tool-timeout 600000 --output-format json`.
- After staging the new helper tests, `golangci-lint` surfaced one unused
  helper in `sources_helpers_test.go`. The unused helper was removed, focused
  `TestSourcesBuildItem` tests passed, and presenter-local `golangci-lint`
  passed with 0 issues.

### Corrective review slice - file organization and source row scan failure semantics

Status: completed and locally verified.

Trigger:

- First external review found that several behavior-preserving refactors still
  left production files above the project's 400-line maintainability smell
  threshold.
- First external review also found that `readSourceItemRows` returned a
  partially populated slice alongside a scan or iteration failure. The current
  caller discarded the slice, but the helper contract was still an avoidable
  future footgun.
- The first delegated file-organization repair misplaced `package main` helper
  files under `internal/presenter`; master verification caught the compile
  break before staging the final state.

Changes:

- Split serve runtime and HTTP lifecycle helpers out of
  `cmd/ai-viewer-serve/main.go` into `cmd/ai-viewer-serve/runtime.go` and
  `cmd/ai-viewer-serve/server.go`.
- Split static asset content-type lookup, gzip middleware internals, session
  SQL filter fragments, and log search helpers into focused presenter package
  files.
- Changed `readSourceItemRows` to return `nil` items on scan and iteration
  failure, with a focused regression test for the mid-iteration scan-error
  case.

Validation:

- Package-layout check passed and printed nothing:
  `find internal/presenter -maxdepth 1 -name '*.go' -exec awk 'FNR==1 && $0=="package main" { print FILENAME }' {} +`.
- Focused presenter and serve tests passed:
  `go test ./cmd/ai-viewer-serve ./internal/presenter -count=1`.
- Focused presenter and serve race tests passed:
  `go test -race ./cmd/ai-viewer-serve ./internal/presenter -count=1`.
- Full presenter/serve strict Lizard scan passed with no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w -x "*_test.go" cmd/ai-viewer-serve internal/presenter`.
- Reviewed file line counts after the split:
  `cmd/ai-viewer-serve/main.go` 277,
  `cmd/ai-viewer-serve/runtime.go` 72,
  `cmd/ai-viewer-serve/server.go` 134,
  `internal/presenter/presenter.go` 348,
  `internal/presenter/schema.go` 72,
  `internal/presenter/embed.go` 353,
  `internal/presenter/middleware.go` 201,
  `internal/presenter/filters.go` 350,
  `internal/presenter/search.go` 357.

### Corrective final-review slice - residual file size and row-scan contracts

Status: completed and locally verified.

Trigger:

- Final external review found `internal/presenter/presenter.go` was still 414
  lines, above the project's 400-line maintainability smell threshold.
- The same review cycle noted that `readHealthSourceRows` still returned a
  partial source slice on scan/iteration failure, unlike the corrected
  `readSourceItemRows` contract.
- A follow-up master line-count audit found pre-existing
  `internal/presenter/stats_rollup.go` at 490 lines. It was outside the
  original Lizard warning list, but it is still presenter complexity and falls
  under this SOW's purpose.

Changes:

- Moved schema-version validation helpers from `presenter.go` into
  `schema.go`, preserving `CheckSchema`, `schemaVersionError`, and `itoa`
  behavior.
- Changed `readHealthSourceRows` to return `nil` sources on scan and iteration
  failure, with a focused mid-iteration scan-error regression test.
- Split pure rollup metric/dimension/bucket parsing and SQL table/column
  helpers from `stats_rollup.go` into `stats_rollup_defs.go`, preserving all
  SQL strings, `#nosec` justifications, parser semantics, and function names.

Validation:

- Focused schema/health tests passed:
  `go test ./internal/presenter -run '^(TestCheckSchema|TestHealth|TestHealthBuildSource|TestReadHealthSourceRows)' -count=1`.
- Focused stats rollup tests passed:
  `go test ./internal/presenter -run '^(TestStatsAggregate|TestStatsTop|TestRollup|TestTop)' -count=1`.
- Presenter and serve package tests passed:
  `go test ./cmd/ai-viewer-serve ./internal/presenter -count=1`.
- Presenter package race tests passed:
  `go test -race ./internal/presenter -count=1`.
- Full presenter/serve strict Lizard scan passed with no warnings:
  `lizard -l go -C 8 -L 50 -a 8 -w -x "*_test.go" cmd/ai-viewer-serve internal/presenter`.
- Production file-size scan across `cmd/ai-viewer-serve` and
  `internal/presenter` found no non-test Go file above 400 lines:
  `find cmd/ai-viewer-serve internal/presenter -name '*.go' ! -name '*_test.go' -print0 | xargs -0 wc -l | awk '$2 != "total" && $1 > 400 { print }'`.
- Final relevant line counts:
  `internal/presenter/presenter.go` 348,
  `internal/presenter/schema.go` 72,
  `internal/presenter/stats_rollup.go` 313,
  `internal/presenter/stats_rollup_defs.go` 193.

## Reviews

### Round 1 - external second opinions on staged implementation

Status: actionable findings addressed; superseded by final-state review below.

Reviewers counted:

- `codex`: completed.
- `glm`: completed.
- `qwen`: completed.

Reviewer unavailable / not counted:

- `gemini`: exited non-zero with no usable review output.
- `kimi`: replacement reviewer was still running while the staged state changed
  during corrective work, so its output cannot count as final-state review
  evidence.

Findings and resolution:

- `codex`: several production files remained above 400 lines after the first
  refactor. Addressed by splitting serve runtime/HTTP lifecycle helpers and
  presenter helper families into focused package files, then re-running focused
  tests, race tests, and strict Lizard.
- `codex`: SOW evidence was stale because the full-gate result had not yet been
  recorded. Addressed by keeping `Outcome` pending until final gates are rerun
  on the final staged state.
- `codex`: the Pre-Implementation Gate still said "ready for future
  activation". Addressed in this SOW update.
- `qwen`: `readSourceItemRows` returned partial results on scan/iteration
  failure. Addressed by returning `nil` items on both failure kinds and adding
  focused regression coverage.
- `qwen`: linear static asset content-type lookup is small and bounded. Not
  changed because the table has 15 entries, is local to static asset serving,
  keeps the current testable data-table pattern, and is not performance or
  security sensitive.
- `qwen`: `serveRuntime.close` nil-receiver concern was a false positive. Go
  methods can run on nil receivers, and this receiver checks `rt != nil` before
  dereferencing.
- `glm`: no blocking findings. Low-severity observations overlapped with the
  bounded content-type table and pre-existing close-error behavior; neither
  required a behavior-preserving SOW change.

### Round 2 - external second opinions after first corrective slice

Status: actionable findings addressed; superseded by final-state review below.

Reviewers counted:

- `codex`: completed.
- `glm`: completed.

Reviewer unavailable / not counted:

- `qwen`: produced useful partial findings but did not exit before the staged
  state changed during corrective work, so it is not counted as convergence
  evidence for this round.

Findings and resolution:

- `codex`: `internal/presenter/presenter.go` remained above 400 lines and SOW
  line-count evidence omitted it. Addressed by moving schema helpers into
  `schema.go`, then re-running focused schema tests, presenter tests, race
  tests, strict Lizard, and the production file-size scan.
- `qwen` partial output: `readHealthSourceRows` had the same partial-return
  footgun that was fixed for `readSourceItemRows`. Addressed by returning `nil`
  sources on scan/iteration failure and adding focused regression coverage.
- `qwen` partial output: duplicated method-not-allowed writer helpers were
  cosmetic. Not changed because the direct in-handler method guard is the
  important spec-drift evidence path; error-writer consolidation is not needed
  for this behavior-preserving SOW.
- `glm`: no blocking findings.
- Master follow-up audit: `stats_rollup.go` was 490 lines. Addressed by
  splitting pure rollup definitions into `stats_rollup_defs.go`.

### Final Round - external second opinions on final integrated staged state

Status: converged; no actionable findings remain.

Reviewers counted:

- `minimax-m3`: completed; approved the staged state for production.
- `mimo-v2.5-pro`: completed; found no correctness, race, security, spec-drift,
  or unwanted-side-effect issues.
- `kimi-2.6`: completed; found no blocking issues and said the staged change is
  ready to merge.

Findings and resolution:

- `kimi-2.6`: test-only strict Lizard length warning in
  `internal/presenter/filters_helpers_test.go`. Not changed because project
  gates intentionally apply this strict Lizard inventory to touched production
  files for this SOW, the test is low-complexity, and CI does not gate
  `_test.go` on this threshold.
- `kimi-2.6`: `/api/health` error-edge shape now returns `sources: null` on
  mid-iteration row errors instead of partial rows. Accepted because returning
  partial data with an error was the footgun fixed by this SOW and is pinned by
  `TestReadHealthSourceRows_ScanErrorReturnsNilSources`.
- `minimax-m3`: only non-blocking future ergonomics notes around SSE test-helper
  reuse and package-local event stream placement. No SOW change required.

## Final Validation

Status: gates green; final external review converged.

Local static analysis:

- Final Codacy changed-file scan passed with 0 issues across 42 files:
  Trivy, Semgrep/Opengrep, Lizard, Agentlinter, and markdownlint all reported
  0 issues.
- Production file-size scan across `cmd/ai-viewer-serve` and
  `internal/presenter` found no non-test Go file above 400 lines.
- Post-completion SOW audit initially flagged synthetic identity-looking
  examples in durable docs/skills. The examples were rewritten to neutral prose
  in `.agents/skills/project-quality-gates/SKILL.md`,
  `.agents/sow/specs/quality-gates.md`, and
  `.agents/sow/specs/adapter-codex.md`, then the artifact checks were rerun.

Full local gates:

- Final `./scripts/gates.sh` passed on the staged state.
- Gate summary:
  - `lint.sh` passed.
  - `scan-secrets` and `scan-ai-attribution` passed.
  - `spec-drift` self-test and live drift check passed.
  - `build.sh` passed, including frontend build, bundle-size gate, embedded
    assets, and Go binaries.
  - Benchmark regression gate passed after retry; the first-attempt
    `HubFanout` regression did not reproduce and was classified as local
    measurement noise by the gate.
  - `test.sh` passed, including Go `-race`, Go coverage, and frontend Vitest.
  - Go coverage gate passed; `internal/presenter` reported 92.8% statements and
    every gated internal package plus aggregate was at or above 80%.
  - Adapter fuzz seed corpus passed.
  - Frontend Playwright + axe passed: 51 tests, 0 serious/critical axe
    violations.
  - Total runtime: 999 seconds; the SOW-0013 known long pole remains the Go
    race suite plus local benchmark gate.

## Outcome

Completed. SOW-0054 reduced the presenter and serve command production warning
inventory to zero under the strict local scan, kept every touched production Go
file below 400 lines, added focused characterization/regression coverage for
the extracted helpers, updated durable project skills for the method-gate
visibility rule, passed local Codacy with 0 issues, passed full local gates, and
converged final external review with no remaining actionable findings.
