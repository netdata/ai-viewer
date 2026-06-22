# Reviewer 9: operator UX (CTO does this — not a CLI reviewer)

**NOTE**: The operator's 9th reviewer (gemini) had a broken
authentication in this session (the OAuth token expired
2025-11-20). Rather than block on a re-auth, the CTO took this
slot directly. The 9th reviewer slot is "operator UX — does the
turn viewer present all the captured information?" The CTO will
do this as a separate chunk of the SOW work.

If you (the operator) want to dispatch this slot to a real
reviewer later, the brief is below. Otherwise, the CTO's UX
review will be appended to the SOW's chunk-1 review record
directly.

```
SCOPE-SPECIFIC BRIEF — REVIEWER 9 (PENDING CTO HANDOFF OR
REVIEWER DISPATCH)

You are reviewing the OPERATOR UX — specifically, does the turn
viewer (TurnView + UnifiedView + SpanDetailDrawer) present all
the captured information from the canonical model? And does the
operator WANT to see fields we don't currently surface?

The 11th invariant is "is the turn viewer presenting all the
captured information?". You are the reviewer for that invariant.

FILE PATHS:

Frontend (the surface):
  frontend/src/pages/SessionDetail/UnifiedView/UnifiedView.tsx
  frontend/src/pages/SessionDetail/UnifiedView/UnifiedView.module.css
  frontend/src/components/TurnView/TurnView.tsx
  frontend/src/components/TurnView/TurnStep.tsx
  frontend/src/components/TurnView/OpIdBadge.tsx
  frontend/src/components/TurnView/stepMeta.ts
  frontend/src/components/SpanDetailDrawer/SpanDetailDrawer.tsx
  frontend/src/api/types.ts  (the frontend mirror of the canonical types)
  frontend/src/components/StaleBadge/StaleBadge.tsx

Backend (the data the UI gets):
  internal/canonical/events.go
  internal/presenter/session_detail.go
  internal/presenter/session_trace.go

UX REVIEW QUESTIONS:

  1. The TurnStep component renders one op. Walk through what
     fields of an op are displayed. For each canonical.Op field,
     is it shown in the UI? If not, why not?

     canonical.Op fields (per internal/canonical/events.go):
       id, turn_id, session_id, parent_op_id, seq, kind, name,
       tool_namespace, model, provider, provider_alias, reasoning_kind,
       start_ts, end_ts, duration_us, status, error_class,
       error_message, tokens_in, tokens_out, tokens_cache_read,
       tokens_cache_write, cost_usd, bytes_in, bytes_out, chars_in,
       chars_out, ctx_used, ctx_max, child_session_id, extras_json

     For each: "shown" / "computed-and-shown" / "not shown — but
     would the operator want it?" / "not shown — by design".

  2. The SpanDetailDrawer (the side panel that opens when you
     click an op in the trace view). Walk through what it
     renders. Does it cover the SAME fields as TurnStep, or
     more / less? Where are the gaps?

  3. The UnifiedView (the 3-zone resizable layout from SOW-0088).
     Top: summary tiles. Middle: trace/waterfall/topology/timeline/
     stats tabs. Bottom: events/logs/raw tabs. For each of the
     canonical data dimensions, which tab (if any) shows it?
     Where are the gaps?

  4. The Invariant 11 test: write a checklist. For each of:
       - user prompt text
       - assistant output text
       - reasoning content
       - tool name + namespace
       - tool request payload
       - tool response payload
       - LLM error class + message
       - tool error class + message
       - sub-agent relationship (child session)
       - per-op token breakdown (in/out/cache)
       - per-op cost
       - per-op context window (used / max)
       - per-op duration
       - per-op parent_op_id (span tree)
     For each: is the operator able to SEE this in the UI today?
     If yes, how (which component, which view)? If no, what's
     the cost to add it (small / medium / large)?

  5. The 11 invariant surfacing: when the framework ships (chunk
     2), the operator needs to SEE when drift happens. The SOW
     proposes a topbar "Drift" indicator. Walk the existing
     AppTopbar and AppSidebar — where's the right place to put
     the indicator? Should it be a clickable pill (links to
     /api/invariants) or a dedicated /drift page? Sketch the
     visual.

  6. Inverse question: are there UI components that display
     data we DON'T capture? E.g. a "draft next prompt" input
     that pre-fills from a session's last user_input — is that
     possible today, or are we missing the field?

OUTPUT: a structured report. For each of the 14 fields in §4
above:
  - Status: shown / not-shown-but-wanted / not-shown-by-design
  - Where in the UI: file:line of the component that shows it
  - Cost to add: small (CSS only) / medium (new component) / large (new data path)
  - Verdict: P0 (operator needs it; capture is broken) / P1
    (operator wants it; capture is fine) / P2 (nice-to-have)

Plus:
  §5. Topbar drift indicator sketch
  §6. Inverse-question findings
  §7. The "5 things the operator wants to see that we don't
       capture" list. This is the actionable output for the
       SOW's chunk 4 (per-adapter gap remediation).
```
