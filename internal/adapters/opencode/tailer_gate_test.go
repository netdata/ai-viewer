package opencode

import (
	"testing"
	"time"
)

// This file is the AC#6 load-bearing proof: the pure MAX(time_updated) gating
// predicate (shouldProbeTimeUpdated) returns false during steady-state idle and
// true only after a WAL event or once the safety net elapses. It also pins the
// cadence state machine (pollState.nextInterval). The literal-SQL no-idle-probe
// assertion via the counting driver lives in tailer_test.go (it needs a real DB).

// TestShouldProbeTimeUpdated is the direct, deterministic AC#6 gate test. It
// asserts the predicate's exact truth table so a regression that re-opens the
// expensive probe on idle polls fails here.
func TestShouldProbeTimeUpdated(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	net := 60 * time.Second

	cases := []struct {
		name         string
		now          time.Time
		lastWALEvent time.Time
		lastProbe    time.Time
		want         bool
	}{
		{
			name:         "steady idle within net: no probe",
			now:          base.Add(10 * time.Second),
			lastWALEvent: time.Time{}, // no WAL event ever
			lastProbe:    base,
			want:         false,
		},
		{
			name:         "wal event after last probe: probe",
			now:          base.Add(1 * time.Second),
			lastWALEvent: base.Add(500 * time.Millisecond),
			lastProbe:    base,
			want:         true,
		},
		{
			name:         "wal event before last probe (already consumed): no probe",
			now:          base.Add(2 * time.Second),
			lastWALEvent: base,
			lastProbe:    base.Add(1 * time.Second),
			want:         false,
		},
		{
			name:         "safety net exactly elapsed: probe",
			now:          base.Add(net),
			lastWALEvent: time.Time{},
			lastProbe:    base,
			want:         true,
		},
		{
			name:         "safety net just under: no probe",
			now:          base.Add(net - time.Millisecond),
			lastWALEvent: time.Time{},
			lastProbe:    base,
			want:         false,
		},
		{
			name:         "safety net well past: probe",
			now:          base.Add(5 * net),
			lastWALEvent: time.Time{},
			lastProbe:    base,
			want:         true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldProbeTimeUpdated(tc.now, tc.lastWALEvent, tc.lastProbe, net)
			if got != tc.want {
				t.Errorf("shouldProbeTimeUpdated(now=%v, wal=%v, probe=%v) = %v, want %v",
					tc.now.Sub(base), tc.lastWALEvent.Sub(base), tc.lastProbe.Sub(base), got, tc.want)
			}
		})
	}
}

// TestShouldProbeTimeUpdated_ManyIdlePolls simulates a run of idle polls (no WAL
// event) at the active cadence and asserts the gate stays CLOSED on every poll
// until the 60 s net elapses — the property that keeps the unindexed full scan
// off the idle hot path.
func TestShouldProbeTimeUpdated_ManyIdlePolls(t *testing.T) {
	t.Parallel()
	net := 60 * time.Second
	start := time.Unix(1_700_000_000, 0)
	lastProbe := start
	var noWAL time.Time

	probes := 0
	// Poll every 500 ms (active cadence) for 50 s — all within the net window.
	for elapsed := time.Duration(0); elapsed < 50*time.Second; elapsed += 500 * time.Millisecond {
		now := start.Add(elapsed)
		if shouldProbeTimeUpdated(now, noWAL, lastProbe, net) {
			probes++
			lastProbe = now
		}
	}
	if probes != 0 {
		t.Errorf("idle polls within the safety-net window issued %d MAX(time_updated) probes, want 0", probes)
	}
}

// TestPollStateNextInterval pins the cadence state machine: idle 2 s, active
// 500 ms, and the 250 ms floor while the WAL-event window is open.
func TestPollStateNextInterval(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)

	t.Run("idle is 2s", func(t *testing.T) {
		t.Parallel()
		st := newPollState(false)
		if d := st.nextInterval(now); d != idlePollInterval {
			t.Errorf("idle interval = %v, want %v", d, idlePollInterval)
		}
	})

	t.Run("active is 500ms", func(t *testing.T) {
		t.Parallel()
		st := newPollState(false)
		st.markCycle(true, now)
		if d := st.nextInterval(now); d != activePollInterval {
			t.Errorf("active interval = %v, want %v", d, activePollInterval)
		}
	})

	t.Run("wal floor overrides idle within window", func(t *testing.T) {
		t.Parallel()
		st := newPollState(false)
		st.markWALEvent(now)
		if d := st.nextInterval(now.Add(1 * time.Second)); d != walFloorInterval {
			t.Errorf("interval within WAL window = %v, want %v (floor)", d, walFloorInterval)
		}
	})

	t.Run("wal floor expires after window", func(t *testing.T) {
		t.Parallel()
		st := newPollState(false)
		st.markWALEvent(now)
		// After the 5 s window, idle cadence resumes.
		if d := st.nextInterval(now.Add(walFloorWindow + time.Second)); d != idlePollInterval {
			t.Errorf("interval after WAL window = %v, want %v (idle)", d, idlePollInterval)
		}
	})

	t.Run("active still floored within wal window", func(t *testing.T) {
		t.Parallel()
		st := newPollState(false)
		st.markCycle(true, now)
		st.markWALEvent(now)
		if d := st.nextInterval(now.Add(time.Second)); d != walFloorInterval {
			t.Errorf("active+WAL interval = %v, want %v (floor below active)", d, walFloorInterval)
		}
	})
}

// TestPollStateFirstProbeGateOpen asserts the INITIAL state opens the probe gate
// on the first poll (lastProbe zero ⇒ the net is immediately due), so a tail that
// starts after in-place mutations reconciles them on the first cycle.
func TestPollStateFirstProbeGateOpen(t *testing.T) {
	t.Parallel()
	st := newPollState(false)
	now := time.Unix(1_700_000_000, 0)
	if !shouldProbeTimeUpdated(now, st.lastWALEvent, st.lastProbe, timeUpdatedSafetyNet) {
		t.Error("first poll gate should be OPEN (zero lastProbe ⇒ net immediately due)")
	}
}
