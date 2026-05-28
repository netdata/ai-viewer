import { API_BASE } from './client';

// Payload streaming (GET /api/payloads/:ref) — Phase 2. The detail view links
// to payload bytes via the `url` already present on each PayloadRef, so for now
// we only expose a helper to build that URL. Inline fetching/rendering of
// payload bodies lands with the Trace tab.

/** payloadUrl returns the streaming URL for a payload ref id. */
export function payloadUrl(ref: number): string {
  return `${API_BASE}/payloads/${ref}`;
}
