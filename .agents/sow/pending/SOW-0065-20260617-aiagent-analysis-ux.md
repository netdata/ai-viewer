# SOW-0065 — ai-agent analysis UX design + implementation

## Status

Status: open (BLOCKED on SOW-0064)

## Requirements

### Purpose

Design and implement the views that answer the operator's core ai-agent analysis questions:
- "Which agents are expensive?" (cost + time + tokens, ranked)
- "Where do agents fail?" (error_class distribution, failure clusters, retry patterns)
- "How do agents interact?" (delegation trees, sub-agent call graphs, who spawns whom)
- "Where is the pressure?" (bottleneck ops, slow tools, token-heavy turns, compaction frequency)
- "What should I improve?" (correlation between model choice + failure rate, prompt size + cost, sub-agent depth + latency)

### Method

1. Use the 5-model reviewer set to brainstorm what an ai-agent operator actually needs (each model independently proposes the view set, then synthesize).
2. Present the design to the operator for approval.
3. Implement after approval.

### Key view candidates (to be refined in the gate)

- **Agent comparison dashboard** — side-by-side metrics across agent types (cost, time, tokens, failure rate, retry count)
- **Failure analysis view** — group errors by class, show the op that triggered them, link to payload, retry chains
- **Delegation tree visualization** — not just topology; show the call hierarchy with cost/time at each level, where the tree fans out, where it's deep
- **Token flow diagram** — where tokens are consumed across the agent tree (which sub-agents eat the most context)
- **Time breakdown** — wall-clock decomposition: LLM wait vs tool execution vs sub-agent delegation
- **Cross-session trends** — how an agent's cost/failure rate changes over time or across model versions

### Acceptance Criteria

1. A design document exists with the proposed view set, approved by the operator. **Verification**: operator sign-off.
2. Each implemented view answers at least one of the 5 core questions above. **Verification**: view → question mapping.
3. The views work on real data (the operator's 150k+ sessions). **Verification**: E2E test or operator walkthrough.

## Pre-Implementation Gate

(To be filled on pickup. Must: complete the 5-model brainstorm; present the synthesis to the operator; get approval before implementation.)

## Implementation / Validation / Reviews / Outcome

(Empty placeholders.)
