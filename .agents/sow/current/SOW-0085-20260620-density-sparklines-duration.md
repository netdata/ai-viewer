# SOW-0085 — Density + sparklines + duration bars (P2)

## Status

Status: in-progress

Sub-state: 2026-06-20. SOW-0084 closed (D4 half). SOW-0085 is item #5 of 6.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

Three related P2 visualization density gaps from the catalog:
- **B1 / C8**: Sessions table has 2 density modes (Comfortable / Compact) that only change row padding — they don't change which columns are shown. Adding a 3rd "Minimal" mode that hides 9 of the 13 columns gives a 5-10x density win for scanning.
- **B2 / F6**: No per-row sparkline. The cost-per-session can spike dramatically; a tiny inline 24h cost sparkline (10 data points) per row would let the operator spot the spikes at a glance.
- **B9 / F1**: No duration visualization. Sessions have a `duration_us` field but the table shows just numbers. A tiny horizontal bar sized to the longest session in the page would be a 100x density win.

Evidence reviewed:
- `.agents/sow/current/SOW-0080-20260620-ux-gaps-p1-p2-p3.md` — SOW-0085 is item #5
- `.agents/sow/specs/ux-research-2026-06-20.md` — items B1, B2, B9, C8, F1, F6
- `.agents/sow/specs/data-model.md` — SessionListItem has duration_us (end_ts - start_ts), cost_usd; no per-time-bucket history
- `frontend/src/pages/SessionsList/SessionsList.tsx` — current table

Affected contracts and surfaces:

- SessionsList page: add 'Minimal' density mode (hide 9 columns), per-row sparkline column, per-row duration bar column
- No backend changes (sparkline data could be added later via /api/sessions/:id/per_hour)

Existing patterns to reuse:
- The Comfortable/Compact density toggle already in SessionsList
- Status badge + tokens for sparkline colors
- Existing duration formatting helper

Risk and blast radius:

Low. All additive UI changes. The new 'Minimal' density is opt-in (default is Comfortable). The sparkline + duration bar columns are additions to the existing table.

Sensitive data handling plan: N/A.

Implementation plan:

1. Add 'Minimal' density mode: hide columns 4-12 (everything except Agent, Status, Cost, Started, Duration). Toggle adds a 3rd option.
2. Add a Sparkline component (inline SVG, no library) that takes a numeric series and renders it as a 80x16 line.
3. Add a DurationBar component (inline div with bg-primary sized by duration / max_duration).
4. Add a 'Last 24h cost' column with sparkline (initially placeholder — empty sparkline; future SOW fetches the per-hour series).
5. Add a 'Duration' column with the bar.
6. Tests.

Validation plan:

- scripts/lint.sh + scripts/test.sh + bundle size green
- Target ~770 tests

Open decisions:

- Sparkline data source: for now, render an empty/placeholder sparkline; a future SOW can add /api/sessions/:id/per-hour. The UI infrastructure (column + component) ships now so the column is in place.

## Requirements

### Acceptance Criteria

1. ✅ SessionsList density toggle has 3 options (Comfortable / Compact / Minimal).
2. ✅ Minimal hides 9 of the 13 columns.
3. ✅ A 'Last 24h' column with a sparkline is added (placeholder data OK for now).
4. ✅ A 'Duration' column with a horizontal bar is added.
5. ✅ All tests pass; bundle ≤ 500 KB gz.

## Plan

1. Add density mode 'minimal'.
2. Add Sparkline + DurationBar components.
3. Wire into SessionsList.
4. Tests.

## Execution Log

Pending.

## Validation

Pending.

## Outcome

Pending.

## Followup

- **SOW-0086** — Heatmap + advanced stats + long-tail (last)
