package presenter

import (
	"testing"
	"time"
)

// deriveEffectiveStatus is the operator-facing status. SOW-0089 chunk 5a.
// The rules (see session_status.go):
//   - persistedStatus in {failed, abandoned, interrupted, aborted} → verbatim
//   - endTs > 0 → "completed"
//   - persistedStatus != "running" → verbatim
//   - persistedStatus == "running":
//       - lastActivityTs == 0 (no activity ever) → "stale"
//       - lastActivityTs < nowUs - 10min → "stale"
//       - else → "running"
//
// Tests cover every branch including the boundary at exactly 10 minutes.

func TestDeriveEffectiveStatus_TableDriven(t *testing.T) {
	const (
		minute = int64(time.Minute / time.Microsecond)
		nowUs  = int64(1_700_000_000_000_000) // arbitrary fixed clock
	)
	tests := []struct {
		name            string
		persistedStatus string
		endTs           int64
		lastActivityTs  int64
		want            EffectiveStatus
	}{
		// ── terminal statuses are returned verbatim ───────────────────────
		{
			name:            "failed is terminal even with recent activity",
			persistedStatus: "failed",
			endTs:           0,
			lastActivityTs:  nowUs - minute, // fresh-ish
			want:            "failed",
		},
		{
			name:            "abandoned is terminal",
			persistedStatus: "abandoned",
			endTs:           0,
			lastActivityTs:  0,
			want:            "abandoned",
		},
		{
			name:            "interrupted is terminal",
			persistedStatus: "interrupted",
			endTs:           0,
			lastActivityTs:  0,
			want:            "interrupted",
		},
		{
			name:            "aborted is terminal",
			persistedStatus: "aborted",
			endTs:           0,
			lastActivityTs:  0,
			want:            "aborted",
		},

		// ── end_ts set overrides everything (the source marked it closed) ─
		{
			name:            "end_ts wins over running+sentinel stale",
			persistedStatus: "running",
			endTs:           nowUs - 100*minute, // long ago
			lastActivityTs:  0,
			want:            "completed",
		},
		{
			name:            "end_ts wins over running+fresh activity",
			persistedStatus: "running",
			endTs:           nowUs - minute,
			lastActivityTs:  nowUs - minute,
			want:            "completed",
		},

		// ── non-running, no end_ts: returned verbatim ──────────────────────
		{
			name:            "completed verbatim",
			persistedStatus: "completed",
			endTs:           0,
			lastActivityTs:  0,
			want:            "completed",
		},
		{
			name:            "unknown verbatim",
			persistedStatus: "unknown",
			endTs:           0,
			lastActivityTs:  0,
			want:            "unknown",
		},

		// ── status=running + no end_ts ──────────────────────────────────────
		{
			name:            "running with no activity recorded → stale",
			persistedStatus: "running",
			endTs:           0,
			lastActivityTs:  0,
			want:            "stale",
		},
		{
			name:            "running with fresh activity (just now) → running",
			persistedStatus: "running",
			endTs:           0,
			lastActivityTs:  nowUs - 1, // 1µs ago
			want:            "running",
		},
		{
			name:            "running with 9m activity → running",
			persistedStatus: "running",
			endTs:           0,
			lastActivityTs:  nowUs - 9*minute,
			want:            "running",
		},
		{
			name:            "running with exactly 10m activity → running (within window)",
			persistedStatus: "running",
			endTs:           0,
			lastActivityTs:  nowUs - 10*minute,
			want:            "running",
		},
		{
			name:            "running with 10m + 1µs activity → stale (just past window)",
			persistedStatus: "running",
			endTs:           0,
			lastActivityTs:  nowUs - 10*minute - 1,
			want:            "stale",
		},
		{
			name:            "running with 11m activity → stale",
			persistedStatus: "running",
			endTs:           0,
			lastActivityTs:  nowUs - 11*minute,
			want:            "stale",
		},
		{
			name:            "running with 24h activity → stale",
			persistedStatus: "running",
			endTs:           0,
			lastActivityTs:  nowUs - 24*60*minute,
			want:            "stale",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveEffectiveStatus(tt.persistedStatus, tt.endTs, tt.lastActivityTs, nowUs)
			if got != tt.want {
				t.Errorf("deriveEffectiveStatus(%q, %d, %d, %d) = %q, want %q",
					tt.persistedStatus, tt.endTs, tt.lastActivityTs, nowUs, got, tt.want)
			}
		})
	}
}

// TestDeriveEffectiveStatus_Constant verifies the documented staleness
// threshold is 10 minutes (matching the StaleBadge component).
func TestDeriveEffectiveStatus_Constant(t *testing.T) {
	if staleThresholdMicroseconds != int64(10*time.Minute/time.Microsecond) {
		t.Errorf("staleThresholdMicroseconds = %d, want 10 minutes", staleThresholdMicroseconds)
	}
}
