package presenter

import (
	"time"
)

// effectiveStatus computes the operator-facing status from the persisted
// `status` snapshot + the freshness signals (end_ts, last_activity_ts).
//
// SOW-0089 chunk 5a — running-status hygiene. The persisted `status` column is
// a snapshot from ingest: it reflects what the source process reported at the
// time the watcher last tailed it. Many sources (notably the codex CLI) do NOT
// emit a clean exit event when the process dies, so the snapshot stays
// `running` long after the process is gone. That misleads the operator — the
// Sessions list says "running" while the actual session has been idle for
// hours.
//
// The fix is a presenter-side derivation:
//
//   - end_ts set             → the session closed → "completed"
//     (overrides everything; ingest is authoritative
//     for the clean-exit case)
//   - status=failed/aborted  → kept verbatim; these are intentional terminal
//     statuses the source set explicitly
//   - status=running + recent activity (last_activity_ts within
//     staleThresholdMicroseconds of now) → "running" verbatim
//   - status=running + stale (last_activity_ts older than the threshold OR
//     NULL while end_ts is also NULL) → "stale" — the UI shows the StaleBadge
//     with "Nm" since last activity
//
// `staleThresholdMicroseconds` is 10 minutes (the threshold the StaleBadge
// component honors — see ui-turn-view.md §Failure modes). Setting it lower
// causes false positives on legitimate long-running commands (a codex
// `sleep 30m` command would falsely look stale); setting it higher causes the
// opposite problem (a session whose process crashed 30 min ago still shows
// "running"). 10 minutes matches the operator's gut from the inception
// feedback ("incorrectly marks 'running' way too many sessions").
const staleThresholdMicroseconds = int64(10 * time.Minute / time.Microsecond)

// EffectiveStatus is the JSON enum returned by `SessionDetail.effective_status`
// and `SessionListItem.effective_status`. "stale" is a NEW status that the
// persisted `status` column never produces — it's only derivable from
// last_activity_ts + now.
type EffectiveStatus string

// Known values of EffectiveStatus. Anything else from the persisted column
// (failed/aborted/interrupted) is returned verbatim by deriveEffectiveStatus
// so the UI shows the source's intended terminal state — these constants
// are only the values the derivation can PRODUCE on its own.
const (
	// EffectiveStatusRunning: the source process is alive and the watcher
	// last saw activity within the 10-minute staleness threshold.
	EffectiveStatusRunning EffectiveStatus = "running"
	// EffectiveStatusCompleted: end_ts is set; the source marked the session
	// closed cleanly.
	EffectiveStatusCompleted EffectiveStatus = "completed"
	// EffectiveStatusStale: persisted status was "running" but the activity
	// is older than the threshold (or there was never any activity recorded
	// while end_ts is still NULL). The UI surfaces this as a separate "stale ·
	// Nm" badge so operators see the process is gone without losing the
	// session.
	EffectiveStatusStale EffectiveStatus = "stale"
)

// deriveEffectiveStatus returns the operator-facing status for a session.
// `persistedStatus` is the snapshot from `sessions.status`. `endTs` is the
// persisted `end_ts` (0 when still open). `lastActivityTs` is the persisted
// `last_activity_ts` (0 when unknown). `nowUs` is `now().UnixMicro()`.
//
// Rules:
//   - persistedStatus in {failed, abandoned, interrupted, aborted} → verbatim
//     (any future terminal status falls through verbatim too — be conservative
//     about overwriting what the source told us).
//   - endTs > 0 → "completed" (the source marked an end_ts; treat as closed
//     even if status is still "running").
//   - persistedStatus == "completed" → "completed".
//   - persistedStatus == "running":
//   - lastActivityTs > 0 AND lastActivityTs >= nowUs - threshold → "running"
//   - else → "stale"
//   - default → verbatim (covers "completed", "unknown", future statuses).
func deriveEffectiveStatus(persistedStatus string, endTs int64, lastActivityTs int64, nowUs int64) EffectiveStatus {
	switch persistedStatus {
	case "failed", "abandoned", "interrupted", "aborted":
		return EffectiveStatus(persistedStatus)
	}
	// A non-running status (`completed`, `unknown`, etc.) is returned verbatim.
	if persistedStatus != "running" {
		return EffectiveStatus(persistedStatus)
	}
	// Status=running. Has the source emitted a clean end_ts?
	if endTs > 0 {
		return EffectiveStatusCompleted
	}
	// Stale check. We treat BOTH "no activity recorded yet" (lastActivityTs==0)
	// AND "activity older than the threshold" as stale. The first covers the
	// "process died before any op" case (the file watcher hasn't reported
	// anything but the process is gone); the second covers the "process died
	// mid-session after emitting some ops" case.
	if lastActivityTs <= 0 || lastActivityTs < nowUs-staleThresholdMicroseconds {
		return EffectiveStatusStale
	}
	return EffectiveStatusRunning
}
