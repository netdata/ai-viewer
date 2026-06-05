import type { TopologyEdge, TopologyNode } from '../api/types';
import {
  runForceLayout,
  type PositionedNode,
  type TopologyLayoutOpts,
} from './topology';

// Web Worker entry for the topology force simulation (frontend-architecture.md
// §Web Worker for D3 force simulation). Imported by the renderer with Vite's
// `?worker` idiom (`import ForceWorker from './forceWorker?worker'`) so it runs
// OFF the main thread above FORCE_WORKER_THRESHOLD nodes, keeping the O(n²)-per-
// tick math from janking scroll or the React tree. The Worker is DOM-free and
// React-free: it receives a plain {nodes, edges, opts} message and posts back
// the settled PositionedNode[]. All rendering (SVG/Canvas) stays on the main
// thread. D3 stays confined to viz/ (the D3-boundary rule) — the simulation math
// itself is runForceLayout, shared with the inline path so it is defined once.

/** Inbound message: the graph + layout opts. `seeded` picks the anchor mode. */
export interface ForceWorkerRequest {
  nodes: TopologyNode[];
  edges: TopologyEdge[];
  opts: TopologyLayoutOpts;
  seeded: boolean;
}

// Outbound message: either the settled positions, or an error string when the
// simulation threw. The error variant exists so a worker failure is SURFACED
// (AGENTS.md §6 "no silent failures") instead of leaving the consumer's
// onmessage to never fire — which would strand the graph permanently empty.
export type ForceWorkerResponse = { positioned: PositionedNode[] } | { error: string };

// The worker global. Typed locally so this module compiles without DOM "lib"
// pollution; `self` in a module worker is a DedicatedWorkerGlobalScope.
const ctx = self as unknown as {
  onmessage: ((e: MessageEvent<ForceWorkerRequest>) => void) | null;
  postMessage: (message: ForceWorkerResponse) => void;
};

function errorMessage(err: unknown): string {
  if (err instanceof Error) {
    return err.message;
  }
  return String(err);
}

ctx.onmessage = (e: MessageEvent<ForceWorkerRequest>): void => {
  // Always post a reply: on success the positions, on failure an error string.
  // If runForceLayout threw and we posted nothing, the consumer's onmessage
  // would never run and the graph would stay empty with no error surfaced.
  try {
    const { nodes, edges, opts, seeded } = e.data;
    const positioned = runForceLayout(nodes, edges, opts, seeded);
    ctx.postMessage({ positioned });
  } catch (err) {
    ctx.postMessage({ error: errorMessage(err) });
  }
};
