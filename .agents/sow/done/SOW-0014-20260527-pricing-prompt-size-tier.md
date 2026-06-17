# SOW-0014 — Prompt-size price tiers (Gemini >200k input)

Status: pending

Owner: assistant (CTO)

Filed: 2026-05-27 (during Chunk 10 iter-3 cleanup)

## Problem

Some vendors price the same model at different rates depending on the
size of the input prompt. Google publishes a >200k input/output/cache
price bracket for `gemini-2.5-pro` that is materially different from
the <=200k bracket. The current Chunk 10 pricing schema and the
`Pricer.Cost` / `Pricer.CostWithDetail` interfaces have no knob for
the input-prompt size, so the embedded data captures only the
<=200k tier and silently under-prices any op whose prompt exceeds the
threshold.

Evidence:
- `internal/pricing/pricing.json:255` — `gemini-2-5-pro` carries only
  the <=200k prices.
- `.agents/sow/specs/pricing.md` — schema does not model a
  prompt-size bracket; `Pricer.Cost` takes token counts but no
  signal that would map to a bracket.
- Source: https://ai.google.dev/gemini-api/docs/pricing — the page
  clearly shows two columns ("Prompts <= 200k tokens" vs "Prompts >
  200k tokens").

## Out of scope for Chunk 10

This requires:
1. Schema change: each tier needs an optional `prompt_size_brackets`
   array with `{min_input_tokens, prices}` entries, OR a tier-level
   `applies_when` filter expressing the bracket condition.
2. `Pricer.Cost` interface change. Note the existing signature
   ALREADY passes `tokensIn` (see `internal/ingest/pricing.go:19`),
   so this is NOT a missing-argument problem — it is a SEMANTIC
   problem: what value should the pricer compare against the
   bracket threshold? Three candidates exist, and the SOW must pick
   one before any code change:

   a. **Total billable input** (current `tokensIn`): the simple
      reading — what the vendor invoices. Matches OpenAI's
      `prompt_tokens` semantic. Risk: cache reads are billed at
      a discounted rate but still count toward the prompt size
      threshold on some vendors. Gemini's >200k bracket DOES
      include cache_read tokens.

   b. **Prompt + context combined** (`ev.CtxUsed`, already on
      `OpFinalizedEvent`): the "what the model actually saw"
      reading. Matches the spec's mental model. Risk: not every
      adapter populates `CtxUsed`, so falling back to `tokensIn`
      is required when unset — adding a quiet correctness gap.

   c. **Cache-inclusive prompt size**
      (`tokensIn + tokensCacheRead`): the most defensible reading
      against Google's published bracket definition. Risk: more
      arithmetic at every call site, and the spec must document
      the formula precisely so a future reader knows why the
      threshold check differs from the cost formula's input term.

   Recommendation (pending implementation review): option (c) for
   Gemini specifically, with a per-vendor `bracket_input_formula`
   discriminator in the tier definition so other vendors can adopt
   their own semantics without forcing one global rule. This
   defers the design choice into data rather than code.
3. Refresh script: LiteLLM does carry the >200k Gemini prices in a
   sibling field; the script needs to surface them as a second
   bracket rather than overwriting the first.
4. Writer change: pass the prompt size into the pricer call site (the
   value lives on `OpFinalizedEvent.TokensIn` already).
5. Spec update + tests for both brackets.

Estimated effort: small-to-medium (one Pricer interface change rippled
across ingest + adapter test doubles, plus a new structural section
in pricing.json and a one-line refresh-script extraction).

## Punt rationale (recorded so the next reviewer sees the trade)

Chunk 10's purpose is the temporal-tier scaffold. The prompt-size
bracket is a second axis of resolution that does not invalidate any of
the work in Chunk 10 — the temporal tier still picks correctly; the
prompt-size bracket just picks a different `prices` sub-object within
the resolved tier. Doing it in Chunk 10 would expand the surface
area (interface change, schema change, refresh-script change,
test-double updates across every adapter test) without unblocking any
Chunk 11+ work. The error is bounded to Gemini ops with input >200k
tokens, which on the operator's workstation is the long-tail.

## Acceptance

- Schema and `Pricer.Cost` interface support per-prompt-size brackets
  for at least Gemini 2.5 Pro.
- Refresh script populates both brackets from LiteLLM.
- Writer passes the op's `TokensIn` to the pricer at the call site.
- Tests cover bracket selection in both directions plus the
  "no bracket data, fall back to the only tier" path so existing
  models without brackets keep working unchanged.
