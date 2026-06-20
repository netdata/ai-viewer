# UX Research & Gap Analysis (SOW-0079 input)

**Date**: 2026-06-20
**Author**: CTO, after operator feedback "the UX is still a student project, not a professional analytical tool".
**Status**: Use cases + flows identified. Gap analysis + priority proposal below. Implementation plan follows the operator's go-ahead.

## Why this document exists

The previous SOW chain (SOW-0073 through SOW-0078) was a **UI polish pass**, not UX work. The CTO conflated the two. This document is the work that should have come first: enumerate the actual use cases the app must serve, then measure the current app against them, then propose a fix plan.

The data layer is genuinely rich (`data-model.md` + `rest-api.md` cover sessions, turns, ops with kind/model/tool/ctx/tokens/cost, FTS5 search, rollups, logs, lineage, sub-agents, source metadata). The current app uses a small fraction of it. The gap is not data; the gap is **information architecture, flow, and density**.

The operator's framing — *"the key is not to support one goal, but as many use cases as possible"* — is the right framing. The app must serve multiple workflows well, not one workflow perfectly. Two distinct user populations share this app:

1. **Coding-agent users** (Codex, Claude Code, OpenCode) — primarily care about **usage**: cost, throughput, what tools were called, when did it run, what model.
2. **AI-agent builders** (ai-agent v2/v3) — primarily care about **understanding**: agent success rate, model reliability, prompt failures, error patterns, cost-per-success, performance.

These are different mental models, different daily questions, different navigation patterns. Treating them as one user is a UX failure.

## Personas

### P1. The Power User (Codex / Claude Code / OpenCode operator)
- Runs coding agents many times per day.
- Glances at the app between tasks; deep-dives after a session ends or feels wrong.
- Mental model: "is my agent fleet healthy and cost-effective?"
- Frequency: 5-20 opens/day. Most glances are < 5 seconds; deep-dives 1-10 minutes.

### P2. The AI-Agent Builder
- Built a multi-agent system on ai-agent v2/v3; wants to know which agent works.
- Mental model: "which agent should I improve next; which is broken?"
- Frequency: 1-3 deep-dives/week. Sessions 10-60 minutes.

### P3. The Team Lead
- Oversees multiple people/agents across all sources.
- Mental model: "are we within budget? who's using what?"
- Frequency: weekly review.

### P4. The Investigator / Auditor
- Something failed or produced wrong output. Needs to find what happened.
- Mental model: "root cause for this specific session / this specific error class".
- Frequency: 1-2x/month, urgent when it happens.

## Use cases (actual questions each persona asks)

### P1 Power User
1. **"What's running right now?"** — live sessions, any source.
2. **"How much have I spent today / this week / this month?"** — by source, by model, total.
3. **"What failed today? Show me."** — failed sessions, error class, when.
4. **"Which model is running the most right now?"** — count + cost, by model.
5. **"Show me my last 10 sessions across all sources."** — chronological feed.
6. **"Compare my usage across the three tools (codex vs claude-code vs opencode)."** — by source, period.
7. **"What's my most expensive session today? Why?"** — drill into trace.
8. **"Did session X succeed? What happened?"** — search by id or by criteria.
9. **"Stale running sessions?"** — sessions with status='running' but no recent activity (last_activity_ts > N min ago).

### P2 AI-Agent Builder
1. **"Which agent is failing most often?"** — by `agent_name` × failure rate.
2. **"What's the success rate per agent? Per model?"** — completed vs failed ratio.
3. **"What errors keep happening? Pattern across sessions?"** — by `error_class`, time-bucketed, with FTS snippet examples.
4. **"How long does each agent take?"** — duration distribution per agent (mean + p50/p95).
5. **"Which model gives best results for this agent?"** — by `agent_name` × `model` × success rate.
6. **"What tools are being called too many times? (loops, retries)"** — per `tool_namespace.name` frequency + failure count.
7. **"How much context are agents using? Hitting limits?"** — `ctx_used / ctx_max` distribution, ops with `ctx_used / ctx_max > 0.8`.
8. **"Show me a failing session's full trace."** — drill from agent → sessions → trace.
9. **"Sub-agent patterns: which agents spawn which, how often?"** — lineage analysis.
10. **"Cost per success"** — total cost / successful sessions, by agent.

### P3 Team Lead
1. **"Total spend this week / month."**
2. **"Spend per source, per model, per agent."**
3. **"Top tools by usage."**
4. **"Failure rate trend (weekly)."**
5. **"Who's using which tool/model right now?"** (P1 contributes; P3 wants aggregate)

### P4 Investigator
1. **"What happened in this session?"** — full trace.
2. **"What was the exact prompt + response?"** — payload viewer.
3. **"What tools were called, with what args, what errors?"** — payload viewer.
4. **"Was there a similar failure in another session recently?"** — FTS5 search by error message.
5. **"Show me all sessions for this agent in the last 24h."** — filter.
6. **"List all parse errors / ingest errors in the last week."** — `log_entries` with severity IN ('WRN','ERR').

## Information needs (data → display)

| Question | Data needed | Exists in DB? | Exposed in API? | Surfaced in UI today? |
|---|---|---|---|---|
| What's running now | sessions where status='running' AND last_activity_ts > now - N | ✅ (`idx_sessions_status`, `last_activity_ts`) | ✅ via `/api/sessions?status=running` | ⚠️ Filter exists; no "live" panel |
| Today's spend | sum(cost_usd) over sessions where start_ts ≥ today_00:00 | ✅ | ✅ via `/api/stats?from=…&to=…` | ⚠️ Stats page; no "today" shortcut |
| What failed today | sessions where status='failed' AND start_ts ≥ today_00:00 | ✅ | ✅ via `/api/sessions?status=failed` | ❌ buried under filters |
| Last N sessions | `ORDER BY start_ts DESC LIMIT N` | ✅ (`idx_sessions_start`) | ✅ | ⚠️ "Load more" is paginated, not "top N" |
| Cross-source compare | group by source_id, sum(cost_usd) etc. | ✅ (`catalog_sources`-like) | ✅ via `/api/stats?dimension=source` | ✅ in Stats page breakdown |
| Stale running | sessions where status='running' AND last_activity_ts < now - N min | ✅ | ⚠️ no first-class filter | ❌ |
| Agent failure rate | per-agent completed/failed counts over window | ✅ (`catalog_agents` + `sessions.failure_count`) | ✅ via `/api/stats?dimension=agent` | ⚠️ shown but no failure-rate emphasis |
| Error pattern | group by error_class, count, sample message | ✅ (`sessions.error_class` + `log_entries`) | ⚠️ `/api/stats?dimension=error_class` is sessions-level; ops-level FTS5 not exposed | ❌ |
| Duration per agent | per-agent SUM(duration_us) + count | ✅ | ✅ via `/api/stats/top` | ❌ |
| Tool frequency | per `(tool_namespace, name)` count + failures | ✅ (`catalog_tools`) | ⚠️ `/api/stats?dimension=tool` exists but is session-level (any op uses tool) | ❌ |
| Context usage | per-LLM-op `ctx_used/ctx_max` | ✅ (`ops.ctx_used`, `ops.ctx_max`, `catalog_models.ctx_max`) | ⚠️ only per-op via `/api/sessions/:id` | ❌ |
| Cost per success | sum(cost_usd) where status='completed' / count by agent | ✅ derivable | ⚠️ derivable client-side from `/api/stats` | ❌ |
| Sub-agent lineage | sessions + parent_session_id | ✅ (`idx_sessions_parent`) | ✅ via `/api/sessions/:id/trace` and `/topology` | ✅ in Trace + Topology tabs (per-session) |
| Parse errors | `log_entries` where severity IN ('WRN','ERR') | ✅ (`idx_log_severity`) | ⚠️ via `/api/sessions/:id/logs` (scoped) | ❌ no global "recent errors" view |
| Prompt/response inspection | `payload_refs` for llm_request/llm_response ops | ✅ | ✅ via `/api/payloads/<id>` | ⚠️ SpanDetailDrawer exists but the flow is rough |

**Summary**: the data is almost all there, but the *views* into the data are incomplete. The API is rich; the UI surface is shallow.

## Critical flows (the few that must be excellent)

### F1. "Open the app → see what's happening" (5 seconds)
Goal: power user opens the app between tasks, gets the answer in 5 seconds.
- Today: lands on Sessions list with 100+ rows. Must scan + filter. **FAIL**.
- Required: a single landing view with (a) currently-running sessions card (b) today's spend so far (c) failures in the last 24h (d) most expensive session today.

### F2. "Why did this session fail?" (drill-down, 30 seconds → 5 minutes)
Goal: investigator or power user opens a specific failed session, walks down to the failing op, reads the error.
- Today: Sessions list → filter by status=failed → click row → Overview tab → tabs → Trace → failing op. **WORKABLE but the tab strip doesn't surface failure**. No breadcrumb, no "where am I", no "back to failed list".
- Required: one-click navigation from "this failed" → "the failing op" with the error message in the topbar of the trace.

### F3. "Which agent is broken? Show me why" (10 minutes)
Goal: builder compares agents, drills into the worst.
- Today: Stats page → Top N by failure rate → click agent name (no link!) → ? **BROKEN**. There's no per-agent page.
- Required: Top-N table on the Agents page (when built) with link to the agent's session list.

### F4. "Show me everything that broke in the last week" (5 minutes)
Goal: investigator or builder finds recent failures across all sources.
- Today: not possible. Sessions list filter by status=failed + from/to filter = ~6 clicks. **MISSING**.
- Required: a "Recent failures" view: sessions with status IN ('failed','abandoned','interrupted') over last 7 days, with error_class, first-error-op, drilldown.

### F5. "What tools is this agent calling?" (5 minutes)
Goal: builder audits a specific agent's tool usage.
- Today: not possible without opening each session and reading the trace. **MISSING**.
- Required: per-agent page with "Tools called" breakdown (catalog_tools joined to this agent's ops).

### F6. "How does my context usage look?" (5 minutes)
Goal: power user checks context pressure.
- Today: per-session via Overview stat card. No aggregate. **PARTIAL**.
- Required: an ops-level view that shows LLM ops with `ctx_used/ctx_max > 0.8` across the filtered set.

## Gap analysis

### A. Information architecture (high impact, medium effort)

| # | Gap | Affected personas | Severity |
|---|---|---|---|
| A1 | **No "Home" view** — landing on `/` shows a paginated list. P1 needs an at-a-glance summary. | P1, P3 | High |
| A2 | **No "Recent failures" view** — failures are 5+ clicks deep. | P2, P4 | High |
| A3 | **No "Per-agent" page** — Agents route is a placeholder. The data (`catalog_agents`) and APIs (`/api/sessions?agents=X`, `/api/stats?dimension=agent`) are all there. | P2 | High |
| A4 | **No "Per-model" page** — same story. | P2, P3 | High |
| A5 | **No "Per-tool" page** — same. | P2 | High |
| A6 | **No "Cost per success" / reliability view** — derivable from existing data, never shown. | P2, P3 | Medium |
| A7 | **No "Context pressure" view** — `ctx_used/ctx_max` is in the DB, never aggregated for display. | P1, P2 | Medium |
| A8 | **Stats page is monolithic** — 5 panels stacked. No quick-filter buttons ("today" / "this week" / "failed only"). | P1, P3 | High |
| A9 | **No "Today's spend" widget anywhere** | P1, P3 | High |
| A10 | **No "Stale running" indicator** — sessions marked running but stale (`last_activity_ts` < threshold) are silently taking up row space. | P1 | Medium |
| A11 | **No "Parse errors / ingest errors" view** — `log_entries` with severity IN ('WRN','ERR') is the operator's "is anything broken in my data pipeline" check. Currently invisible. | All | High |
| A12 | **No "Cross-source comparison" view** — P1 needs to compare codex vs claude-code vs opencode at a glance. The data is there, the view doesn't exist. | P1, P3 | Medium |
| A13 | **Sessions page has no "row click → expand inline summary"** — must navigate to detail page to see anything beyond columns. Forces extra navigation. | P1, P2 | High |
| A14 | **No "Saved views" or "Recent sessions"** — P1 revisits the same session repeatedly. No way to pin. | P1, P4 | Medium |

### B. Information density / visualization (high impact, medium effort)

| # | Gap | Severity |
|---|---|---|
| B1 | **Sessions table is 13 columns wide** at full width. Overwhelming. No column-hide, no density modes beyond row-padding. | High |
| B2 | **No "sparkline" or "trend" in the sessions table** — can't see at-a-glance "is this session spiking in cost". | Medium |
| B3 | **No heatmap** — "failures by hour-of-day" is a classic observability primitive. The data is there. | Medium |
| B4 | **No "small multiples"** — one small chart per agent/model for comparison. | Medium |
| B5 | **No interactive tooltips on stats charts** — hover shows title only. No "this bucket: $X, Y tokens, Z failures". | High |
| B6 | **The "Stats page" charts don't link to a filtered list** — clicking a chart point should filter the sessions list. | High |
| B7 | **The Topology graph is read-only** — no filter, no search, no "highlight only failures". | Medium |
| B8 | **Status pills take 1/4 the row width** but the most important column is `Cost`. The pill should be a dot, the cost should be the primary number. | Medium |
| B9 | **No "duration" visualization** in the sessions list — just a number. A tiny horizontal bar would be a 100x density win. | Medium |
| B10 | **The Sources page table** doesn't surface the "lag" warning visually — just a number with `—` if unknown. | Low |

### C. UX primitives consistency (medium impact, low effort)

| # | Gap | Severity |
|---|---|---|
| C1 | **Some pages have a "Refresh" button, others don't** (Sources has it, Sessions/Stats don't). | Medium |
| C2 | **Loading states are inconsistent** — Sessions has a skeleton, Stats has text, Sources has a card. | Medium |
| C3 | **Filter UI is inconsistent** — global FilterBar (legacy) vs URL filters vs page-local filters all behave differently. | High |
| C4 | **Date format is inconsistent** — some show "6/19/2026, 7:37:59 PM" (US locale), others show "Jun 19, 7:37 PM". | Low |
| C5 | **The Sessions table's "Show secondary" toggle is unclear** — what does "secondary" mean? The label should be "Sub-agents and forks" or be hidden behind a "More" menu. | High |
| C6 | **"0 active filters" badge** appears on Sessions toolbar but isn't clickable to clear. | Low |
| C7 | **Tab strip on Session Detail** uses no `aria-current`, hover state is subtle, no keyboard shortcut to switch tabs. | Low |
| C8 | **The "Comfortable / Compact" density toggle on Sessions** only changes row padding, not data shown. Doesn't earn the name. | Medium |
| C9 | **Sort indicators** in the Sessions table are tiny icons. The active sort column is hard to identify. | Low |
| C10 | **The command palette (⌘K) is undiscoverable** — no visible button after first use. Should be in the topbar as a "Search ⌘K" affordance. | Medium |

### D. Navigation (high impact, high effort)

| # | Gap | Severity |
|---|---|---|
| D1 | **No breadcrumbs on Session Detail** — you know you're on a session, but not "where in the tree". | High |
| D2 | **No "back to list" affordance** — once you drill into a session from Sessions, you must use browser back. | High |
| D3 | **Sidebar order doesn't match mental model** — "Drill-down" comes after primary nav, but for P2 (AI-Agent Builder), Agents is the primary thing. | Medium |
| D4 | **No global search** — ⌘K is a command palette, not a search. You can't "search for sessions containing 'database error'" from the topbar. | High |
| D5 | **No "Pinned sessions" or "Recent" list** — P1/P4 revisit the same sessions. | Medium |
| D6 | **Tab strip and sidebar nav mix concepts** — sidebar = routes, tabs = sub-views of a session. Acceptable, but a "Current session" breadcrumb in the tab strip would help. | Low |
| D7 | **No "All sessions for this agent" link** — when looking at an agent in the Stats page, no way to filter the sessions list to just that agent. | High |

### E. Ingestion (medium impact, high effort)

| # | Gap | Severity |
|---|---|---|
| E1 | **Parse errors are silently tracked** — codex has 2042, ai-agent_v2 has 544, but no UI surfaces this. | High |
| E2 | **No "ingest is healthy" / "ingest is degraded" alert** — the /api/health endpoint reports it, but the operator doesn't see it. | High |
| E3 | **No global "recent log entries"** — `log_entries` with severity IN ('WRN','ERR') should be browsable. | Medium |
| E4 | **Sources page doesn't show "lag" warning visually** — a source lagging 5 minutes is the operator's first sign of trouble. | Medium |

### F. Data exposure (low impact, low effort — but easy wins)

| # | Gap | Severity |
|---|---|---|
| F1 | **Sessions list doesn't show duration as a bar** — a horizontal bar sized to the longest session in the page would be a 100x density win. | High |
| F2 | **The Sessions page doesn't show agent distribution** — a tiny "agents: 5" / "models: 3" / "sources: 2" stat strip above the table. | High |
| F3 | **No "session length distribution" anywhere** — "how long are my typical sessions?" | Medium |
| F4 | **No "context pressure" indicator on a per-session basis** in the sessions list. | Medium |
| F5 | **Cost per success is never shown** — but it's a 1-line derived value. | High |
| F6 | **No "what tools did this session call?" inline summary** in the sessions list. | Medium |

## Priority proposal

The operator asked for a 2-step plan. This document is Step 1. Here is what I'd propose for Step 2, in priority order:

### P0 — done before anything else
- **A1 Home view** (10-line change to the Sessions page: add a summary card strip at the top showing today/week/failures/cost/active). This single change makes the app usable for P1 in 5 seconds. Effort: ~half a day.
- **A2 Recent failures view** (new route `/failures`, query: `?status=failed,abandoned,interrupted&from=last_7d`). The data is already there. Effort: ~half a day.
- **F5 Cost per success** — add a column to the sessions table + a "Reliability" stat tile. Effort: 1 hour.
- **C5 Rename "Show secondary" to "Sub-agents and forks"** — pure copy fix, high info value. Effort: 5 minutes.
- **C10 Make ⌘K discoverable** — add a small "Search ⌘K" button to the topbar. Effort: 10 minutes.

### P1 — the rest of the high-severity gaps
- **A3 Per-agent page** (real, not a placeholder). Sessions filtered to this agent, success rate, top tools, top errors. Effort: 1-2 days.
- **A4 Per-model page** (same shape).
- **A5 Per-tool page** (same shape).
- **A11 Ingest errors view** (a new "Ingest health" or extend Sources with a "Recent errors" tab). Effort: 1 day.
- **A13 Inline session summary** on the Sessions list (expand row to show 1-line summary). Effort: 1 day.
- **D1/D2 Breadcrumbs + back-to-list** on Session Detail. Effort: 2 hours.
- **D4 Global search from topbar** (extend ⌘K or add a separate "search sessions" input). Effort: 1 day.
- **B5/B6 Interactive tooltips on stats charts + click-to-filter**. Effort: 1 day.

### P2 — visualization density
- **B1 Column hide / density levels beyond "Comfortable / Compact"** — add a "Minimal" mode that shows only Agent/Status/Cost/Started. Effort: half a day.
- **B2 Sparkline column in sessions list**. Effort: half a day.
- **B3 Heatmap view** (failures by hour-of-day, by agent, by model). Effort: 1 day.
- **F1 Duration bar in sessions list** — tiny horizontal bar per row. Effort: 1 hour.
- **F2 Tiny stat strip above sessions table** (agents/models/sources count). Effort: 30 minutes.

### P3 — nice to have
- **A6 Reliability / cost-per-success view**. Effort: 1 day.
- **A7 Context pressure view**. Effort: 1-2 days (needs aggregation that may not exist client-side).
- **A8 Stats page quick-filter buttons**. Effort: 2 hours.
- **A10 Stale running indicator**. Effort: 2 hours.
- **B4 Small multiples**. Effort: 1 day.

## What I am NOT going to do

Per the operator's "as many use cases as possible" framing, I should not optimize for one workflow. But I also should not chase every gap. Specifically:
- **Visual identity / logo** — the operator didn't ask, and it's separable from UX. Defer.
- **Full brand redesign** — defer.
- **The Topology D3 graph internals** — defer (the chrome is polished; the graph is its own design problem).
- **Mobile-first redesign** — the app is desktop-first by intent (an analytical tool). Improve but don't rebuild.

## How I'll work after operator approval

Per the SOW lifecycle:
1. Each P0 item gets a single focused commit.
2. P1 items land as their own SOWs (A3 / A4 / A5 = the "Pages: Agents / Models / Tools come alive" SOW; A11 = "Ingest health" SOW; D1/D2 = "Breadcrumbs + back" SOW).
3. The 5-reviewer loop runs on each P1 SOW (or its visual deliverable) — same as SOW-0073.
4. Specs (this one, the design-system one) update to reflect the new views.

## What I need from the operator

1. **Go / no-go** on this plan. If the priorities are wrong, tell me which gaps matter most.
2. **One specific real workflow** you've done recently. "I wanted to find X and I had to click Y" — concrete examples. The plan above is my best guess; your real workflow may surface gaps I've missed.
3. **End-state judgment** — when the P0 + P1 are done, is the app "professional, polished, modern"? I won't keep polishing forever; I need a clear "stop here" signal.

## References
- `.agents/sow/specs/data-model.md` — full schema (sources, sessions, turns, ops, log_entries, rollups, fts_ops, fts_logs).
- `.agents/sow/specs/rest-api.md` — every endpoint, every filter param.
- `.agents/sow/specs/design-system.md` — the visual language established in SOW-0073.
- `.agents/sow/done/SOW-0073-20260619-visual-foundation.md` — the foundation SOW.
- Operator feedback 2026-06-20: "the UX is still a student project, not a professional analytical tool."