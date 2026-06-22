# SOW-0095 - Session diff comparison view

## Status

Status: completed
Completed: 2026-06-22
Sub-state: CTO proposed; awaiting operator sign-off. Phase: Development (CTO-discretion review per AGENTS.md). This is a new analytical feature, not a bug fix.

## Pre-Implementation Gate

### Problem / root-cause model

The operator runs the same AI-agent task multiple times — same prompt, different model, different tool set, different prompt template, different day. Today the only way to understand "why did session B cost 4× more / take 6× longer / hit 3 errors session A didn't" is to open each session detail page in a separate tab and mentally diff. The data is in the DB (canonical model, ops, payload_refs, error_class, tool usage), the queries are already running for the existing single-session endpoints, and the operator's stated goal for this project is "give the operator a fast, beautiful, low-friction way to see what their AI agents have been doing — across time, across formats, across sub-agents". The current surface answers the "single session" case; the "two sessions side-by-side" case is missing.

This is the analytical workflow the app's primary user (AI-agent builder) actually does: A/B test a prompt, compare model variants, see if a code change regressed an existing pass rate. The operator's standing principle ("do the thing that enables better/more future potentials") resolves toward a general-purpose compare endpoint (2-4 sessions) rather than a hard-coded "compare by prompt hash" feature, because the operator's actual comparisons are not always within a prompt cluster.

### Evidence reviewed

- `.agents/sow/specs/rest-api.md` — list of existing endpoints. No `/api/sessions/compare` exists. Single-session endpoints: `/api/sessions/:id`, `/api/sessions/:id/trace`, `/api/sessions/:id/topology`, `/api/sessions/:id/timeline`, `/api/sessions/:id/related`, `/api/sessions/:id/logs`, `/api/sessions/:id/payload_refs`.
- `internal/presenter/session_detail.go` and `internal/presenter/sessions_list.go` — the existing `sessionListItem` and `sessionDetail` types define the schema of a single session summary. The compare response can reuse the same `sessionListItem` shape (or a slim subset of it) to avoid inventing a parallel contract.
- `internal/canonical/events.go` — the canonical event types: session (with kind, agent_name, model, error_class, status), turn, op (with kind, name, status, tokens_in/out, cost_usd, started_at_us, duration_us). All compare queries can be written against these columns.
- `internal/store/migrations/0011_topology_sort_indexes.sql` — schema version 11; the `idx_sessions_duration`, `idx_sessions_op_count`, `idx_sessions_cost`, `idx_sessions_tokens` indexes plus the `duration_us` stored column are all in place. The compare endpoint's per-session summary query will be index-driven for the same reason `/api/topology` is.
- `frontend/src/pages/SessionDetail/` — single-session detail page. The compare page reuses the same visual primitives (TurnView, SpanDetailDrawer, design tokens).
- `.agents/sow/specs/frontend-architecture.md` and `.agents/sow/specs/design-system.md` — visual contract. The compare page follows the same grid + table + badge conventions as SessionsList / SessionDetail.
- `frontend/src/api/sessions.ts` — frontend API client; `useSessionDetail` is the pattern to mirror for `useCompareSessions`.
- `frontend/src/components/EntityList/` and `frontend/src/components/SessionRow/` — visual primitives for session rows; the compare side-by-side cards reuse these.

### Affected contracts and surfaces

- **New REST endpoint**: `GET /api/sessions/compare?ids=<csv>` — the data contract for the new view. Spec at `.agents/sow/specs/rest-api.md` §"GET /api/sessions/compare".
- **New Go package**: `internal/presenter/compare.go` — the compare engine. Spec at `.agents/sow/specs/presenter.md`.
- **New Go tests**: `internal/presenter/compare_test.go` — must cover empty input, single id, max-ids, missing ids, mismatch / divergent agent / model / tool / error / cost / tokens, N-way diff (3+ sessions), order preservation.
- **New frontend page**: `frontend/src/pages/Compare/Compare.tsx` (+ `.module.css`, `.test.tsx`) — the user-facing compare view. Spec at `.agents/sow/specs/ui-pages.md` §"Compare".
- **New frontend API hook**: `useCompareSessions(ids: string[])` in `frontend/src/api/sessions.ts` — mirror `useSessionDetail`.
- **Router wiring**: `frontend/src/App.tsx` — new `/compare` route, accepting `?ids=...`.
- **No DB migration**. The data is already in v11 schema.

### Spec deltas to land before any test or code

1. `.agents/sow/specs/rest-api.md` — add `### GET /api/sessions/compare` section with full request + response schema, including the diff structure (summary, tool_usage, errors, models, agents, kind_distribution). Also document: 2-4 ids required, order preserved in the response, `400` for invalid id count or non-existent id, `404` if any requested id is not in the DB.
2. `.agents/sow/specs/presenter.md` — add `## Session diff` section documenting the compare engine's responsibilities, query shape, and diff algorithms. Note that compare uses the same `sessionListItem` shape for the per-session summary to keep one canonical session-summary contract.
3. `.agents/sow/specs/ui-pages.md` — add `## Compare` page section with the URL contract, the per-tab layout, and the side-by-side summary card design. Note that the page reuses `SessionRow`-style cards scaled to the column count, with the "winner" / "loser" indicator per metric when 2+ sessions are compared.
4. `.agents/sow/specs/frontend-architecture.md` — add `### /compare` page to the route table, and document the `useCompareSessions` hook contract.
5. `.agents/sow/specs/index.md` — add Compare page + compare endpoint to the table of contents.

### Existing patterns to reuse

- **Per-session summary query** mirrors `internal/presenter/sessions_list.go` `loadSessionList` (the SELECT clause of the list query). Compare is "load N summaries keyed by id, plus the diff".
- **Error surfacing pattern** mirrors `internal/presenter/session_detail.go` (structured error with status code, message, field name).
- **Frontend API hook pattern** mirrors `useSessionDetail` in `frontend/src/api/sessions.ts` (useQuery with `enabled: ids.length >= 2`).
- **Side-by-side card design** mirrors `frontend/src/components/SessionRow/SessionRow.tsx` for the compact summary + status badge + sparkline.
- **Diff viz patterns** (kind distribution, model badges, tool list) mirror `frontend/src/pages/Stats/Stats.tsx` and `frontend/src/pages/Agents/AgentsList.tsx`.

### Risk and blast radius

- **Risk: Backend query complexity.** A naive implementation could be O(N × |ops|) for tool / error / kind diffs. Mitigation: use a single SQL query per diff dimension with `GROUP BY`, leveraging existing indexes (`idx_ops_session_id`, `idx_ops_kind`, `idx_ops_name`, plus the new topology sort indexes on the session table). For 4 sessions × 6,955 ops (the biggest codex session), this is at most ~28k rows per dimension — well within index-driven performance budgets.
- **Risk: Frontend bundle size.** A new page adds 5-10 KB. The 500 KB gz budget has 287 KB headroom (current 213 KB gz); the compare page is comfortably under-budget.
- **Risk: Visual regression in the existing single-session views.** Mitigation: the compare page is a new route, not a modification to existing routes. The visual primitives are reused, not changed.
- **Risk: Long URLs.** A 4-id compare URL could be ~400 chars (typical session ids are 32+ chars). Mitigation: this is fine for browser URLs; no need for a server-side "comparison set" feature in v1.
- **Blast radius**: additive only. No existing endpoint, route, schema, or test changes. The only shared surface touched is the design tokens (no change) and the `useSessionDetail` API client file (new function added; existing function unchanged).

### Sensitive data handling

- Compare queries only return the same summary data the existing `/api/sessions` and `/api/sessions/:id` endpoints return. No new sensitive fields.
- No fixture data needed for tests; the test seeds in `internal/presenter/sessions_testseed_test.go` already cover multi-session, multi-agent, multi-model, multi-error-class, multi-tool scenarios. The compare tests will reuse the existing seed patterns.
- No operator identity, no API keys, no customer data exposed.

### Implementation plan

1. **Backend: present Compare engine** (`internal/presenter/compare.go`)
   - `type compareRequest struct { IDs []string }` parsed from `?ids=a,b,c` (csv).
   - `type compareResponse struct { Sessions []sessionListItem; Summary compareSummary; ToolUsage compareTools; Errors compareErrors; Models compareModels; Agents compareAgents; KindDistribution compareKinds }`.
   - `type compareSummary struct { Metric string; Best string; Worst string; Min float64; Max float64; Delta float64; PerSession map[string]float64 }`.
   - `type compareToolBucket struct { Added []string; Removed []string; Common []string; PerSession map[string]map[string]int64 }`.
   - `type compareErrors struct { OnlyIn map[string][]opErrorRef; Common []opErrorRef }` where `opErrorRef { OpID, Kind, Name, Class, StartedAtUS }`.
   - **Diff algorithm per dimension**:
     - **Tool usage**: `SELECT name, COUNT(*) FROM ops WHERE session_id IN (?,?,?,?) AND kind='tool' GROUP BY name` → pivot in Go; tools in only one session = "added/removed" per pair; intersect across all sessions = "common".
     - **Errors**: `SELECT session_id, op_id, kind, name, error_class, started_at_us FROM ops WHERE session_id IN (?,?,?,?) AND error_class != '' AND error_class != 'none' ORDER BY session_id, started_at_us`. Per-session error set; intersect = common; diff = only-in.
     - **Models / agents**: already on the session row; just pivot.
     - **Kind distribution**: `SELECT session_id, kind, COUNT(*) FROM ops WHERE session_id IN (?,?,?,?) GROUP BY session_id, kind` → pivot.
     - **Summary stats**: re-use the same SELECT clause as the `/api/sessions` list query, parameterized to filter by id IN (...). Apply the same `duration_us`, `tokens`, `cost_usd` indexes (the topology sort indexes cover this exact pattern).
   - The "winner / loser" is computed client-side for clarity (frontend decides whether the lower or higher value is "best" per metric — e.g. lower duration is best, higher tokens is bad if cost matters, etc.).
   - For the v1, the "diff dimension" is one endpoint returning all diffs in one payload. Future work: `?dims=tools,errors` opt-in pattern to slim the response if compare is used in tight loops (not in v1).

2. **Backend: route + tests** (`internal/presenter/compare.go` + `internal/presenter/compare_test.go` + `internal/presenter/presenter.go` + `internal/presenter/router_test.go` if needed)
   - `func (p *Presenter) handleCompareSessions(w http.ResponseWriter, r *http.Request)` registered in `init()` next to `handleSessionDetail`.
   - Tests:
     - `TestCompare_Empty` → 400, "no ids provided"
     - `TestCompare_OneID` → 400, "2-4 ids required"
     - `TestCompare_FiveIDs` → 400, "max 4 ids"
     - `TestCompare_UnknownID` → 404, "session not found"
     - `TestCompare_TwoSessionsIdentical` → all "common", no diffs
     - `TestCompare_TwoSessionsDivergentModel` → models.diverged populated
     - `TestCompare_TwoSessionsDivergentAgent` → agents.diverged populated
     - `TestCompare_TwoSessionsOneHasError` → errors.only_in populated for the one
     - `TestCompare_TwoSessionsDifferentTool` → tool_usage.added/removed populated
     - `TestCompare_ThreeSessions` → all diffs cross-computed
     - `TestCompare_SummaryStats` → duration_us, op_count, cost_usd, tokens per session, with best/worst
     - `TestCompare_KindDistribution` → per-session kind histogram
     - `TestCompare_OrderPreserved` → response.sessions order matches request ids order
   - All tests use the existing `withSeed` helper pattern from `internal/presenter/sessions_testseed_test.go`.

3. **Frontend: API hook** (`frontend/src/api/sessions.ts`)
   - `useCompareSessions(ids: string[]): { data: CompareResponse | undefined; isLoading: boolean; error: Error | null }` using `useQuery` with `queryKey: ['compare', ids.join(',')]`, `enabled: ids.length >= 2 && ids.length <= 4`, `queryFn: () => fetch('/api/sessions/compare?ids=' + ids.join(',')).then(...)`.

4. **Frontend: page** (`frontend/src/pages/Compare/Compare.tsx` + `.module.css` + `.test.tsx`)
   - Layout: top row of N summary cards (2-4 columns), each showing: agent, model, status, op_count, duration, cost, tokens, started_at, error_class, child count. Cards highlight the "best" / "worst" per metric with a small green/red indicator.
   - Tabbed body (4 tabs): **Overview**, **Tools**, **Errors**, **Kinds**.
   - **Overview** tab: side-by-side summary table (rows = metrics, columns = sessions). Cells show value + diff-vs-best delta. Best metric per row gets a green check; worst gets a red ✗.
   - **Tools** tab: 3 columns "Common", "Only in A", "Only in B" (extensible to 3-4 with a per-session legend). Tool name + call count.
   - **Errors** tab: same 3-or-4 column layout. Error class + op_id + timestamp.
   - **Kinds** tab: bar chart of per-session kind distribution (reuse `frontend/src/pages/Stats/charts/BarChart.tsx`).
   - Empty state: "Pick 2-4 sessions to compare" with a link to /sessions.
   - Error state: "Session not found: <id>".
   - Loading state: skeleton cards + skeleton tab body.

5. **Frontend: routing** (`frontend/src/App.tsx`)
   - Add `<Route path="/compare" element={<Compare />} />`.

6. **Frontend: entry-point for diff comparison** (low-cost addition for v1)
   - On `SessionRow` and `SessionDetail` header, add a small "Compare" button: opens `/compare?ids=<currentId>` (one-id mode — the user picks a second session from a popover of recent sessions).
   - This is the discovery path: without it, the operator has to type session ids into the URL by hand, which is hostile. v1 keeps it minimal (a button that opens the compare page in "pick second" mode if only one id is provided; the compare page itself prompts for the remaining ids).

### Validation plan (named test files + behaviors)

- `internal/presenter/compare.go` — Compare engine + handler (above)
- `internal/presenter/compare_test.go` — all behavior tests above
- `frontend/src/api/sessions.ts` — `useCompareSessions` hook (typed, not test-covered directly; tested via page tests)
- `frontend/src/pages/Compare/Compare.test.tsx` — page-level tests:
  - `TestCompare_TwoSessions` — renders 2 cards, both tabs populate from fixture
  - `TestCompare_ThreeSessions` — 3-card layout, kind distribution chart renders
  - `TestCompare_OneID` — empty state "Pick 2-4 sessions to compare"
  - `TestCompare_UnknownID` — error state
  - `TestCompare_TabNavigation` — click Tools tab, content swaps
  - `TestCompare_Indicator` — best/worst per metric shown
- Visual: manual smoke in the dev server; capture before/after screenshots into `.agents/sow/done/SOW-0095-screenshots/post/` and add to the SOW.

### Artifact impact plan

- New files (no existing file modified except for additive API client and router):
  - `internal/presenter/compare.go`
  - `internal/presenter/compare_test.go`
  - `frontend/src/pages/Compare/Compare.tsx`
  - `frontend/src/pages/Compare/Compare.module.css`
  - `frontend/src/pages/Compare/Compare.test.tsx`
  - `frontend/src/pages/Compare/index.ts`
- Files modified (additive only):
  - `internal/presenter/presenter.go` — register `handleCompareSessions`
  - `frontend/src/api/sessions.ts` — add `useCompareSessions` hook + `CompareResponse` type
  - `frontend/src/api/types.ts` — add `CompareResponse`, `CompareSummary`, `CompareToolBucket`, `CompareErrorRef` types
  - `frontend/src/App.tsx` — add `/compare` route
  - `frontend/src/components/SessionRow/SessionRow.tsx` — add small "Compare" button (optional for v1; if skipped, the URL is the entry point)
  - `frontend/src/pages/SessionDetail/SessionDetail.tsx` — same optional Compare button
  - `.agents/sow/specs/rest-api.md` — add Compare endpoint section
  - `.agents/sow/specs/presenter.md` — add Session diff section
  - `.agents/sow/specs/ui-pages.md` — add Compare page section
  - `.agents/sow/specs/frontend-architecture.md` — add /compare route + hook contract
  - `.agents/sow/specs/index.md` — TOC update
- Schema impact: **none** (v11 is sufficient).

### Open decisions

None — the design follows existing patterns closely, and the operator's standing tiebreaker (long-term-best) resolves the optional-entry-point question toward **add the small Compare button to SessionRow + SessionDetail** (it's the only way the compare page is discoverable; without it, the operator would have to URL-hack).

### Out of scope (v1)

- Per-op alignment (turn-by-turn diff with a Needleman-Wunsch-style sequence alignment). Deferred; the kind-distribution + summary diffs answer 90% of the analytical question; full alignment is a v2 feature.
- Persisted comparison sets (saved URLs, shared links). The URL is shareable as-is.
- Per-payload diff ("show the actual output of session A vs B for op X"). Deferred; can be added by clicking into a session from the compare view.
- Time-series diff (e.g. "this prompt regressed over the last week"). Deferred; that's a Stats view feature.
- Export to JSON / CSV. Deferred; the JSON response is already exportable via curl.
