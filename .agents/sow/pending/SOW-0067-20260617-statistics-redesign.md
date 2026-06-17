# SOW-0067 — Statistics redesign: multi-dimension slicing with error analytics

## Status

Status: open

## Requirements

### Purpose

Transform the Statistics page from "basic charts with selectors" into the primary analysis tool for evaluating models, harnesses, and tools. The operator needs to slice the data by any dimension and see both happy-path metrics (tokens, duration, cost, cache efficiency) AND failure metrics (error classes, failure rates, retry patterns).

### Background

The operator's core analysis questions at the aggregate level:
- "Which models are expensive?" — cost per model, over time
- "Which models fail most?" — failure rate per model, by error class
- "Which harnesses (claude-code, codex, opencode, ai-agent) are most efficient?"
- "How does cost/failure change over time or across model versions?"
- "Which tools are slowest / most failure-prone?"
- "What's my cache hit ratio per model?"

### Current state

The Statistics page has: a summary metrics bar, a trend line chart (one metric, one time series), a top-N breakdown (one dimension, one metric), a data table, and CSV export. Each chart has its own independent selector — they don't work together.

### Design goals

1. **Unified filtering** — one filter bar (time range, source, model, agent, tool, status) drives ALL charts simultaneously
2. **Multi-dimension comparison** — the operator picks a dimension (model, source, agent, tool, status, error_class) and sees a comparative breakdown across that dimension for MULTIPLE metrics at once (cost, tokens, duration, failure rate, cache hit ratio)
3. **Error analytics** — a dedicated section showing failure distribution: error_class pie/bar, failure rate over time, failure rate per model, retry statistics
4. **Time-series with dimension overlay** — the trend chart should show multiple series overlaid (e.g. cost for model A vs model B over time)
5. **Export** — CSV export of whatever the current view shows

### Acceptance Criteria

1. One unified filter bar drives all charts (changing a filter updates every chart on the page, not just one). **Verification**: UI test.
2. The operator can select a "group by" dimension and see a comparative table showing cost/tokens/duration/failure-rate/cache-hit across all values of that dimension. **Verification**: the table renders with real data.
3. A failure-analysis section shows: error_class distribution (bar/pie), failure rate trend over time, failure rate per model. **Verification**: visible with the ai-agent error taxonomy data (905 classified failures).
4. The trend chart can overlay multiple series (e.g. compare cost across 2+ models or sources). **Verification**: multiple colored lines.
5. All views export to CSV. **Verification**: download produces the right data.

### Implementation approach

- Backend: the existing `/api/stats` (live aggregates) + `/api/stats/aggregate` (time series) + `/api/stats/top` (rankings) cover most needs. Add a `/api/stats/failures` endpoint that returns error_class distribution + failure rates by dimension (the data is in the DB — `sessions.error_class` is now populated for ai-agent).
- Frontend: redesign the Stats page into sections: Summary bar → Trends (multi-series) → Breakdown table (group-by + multi-metric) → Failure analysis → Search. Each section reads from the unified filter.

## Pre-Implementation Gate / Implementation / Validation / Reviews / Outcome

(Empty placeholders.)
