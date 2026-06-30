package presenter

import (
	"database/sql"
	"testing"

	"github.com/google/go-cmp/cmp"

	_ "modernc.org/sqlite"
)

func TestHealthBuildSource_NullLastSeen(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	gotSource, gotDegraded := buildHealthSource(healthSourceRow{
		id:          "src-null",
		format:      "aiagent_v3",
		location:    "/tmp/null",
		enabled:     1,
		parseErrors: 2,
		lastSeq:     17,
	}, nowUS)

	assertHealthSourceEqual(t, gotSource, healthSource{
		ID:             "src-null",
		Format:         "aiagent_v3",
		Location:       "/tmp/null",
		Enabled:        true,
		ParseErrors:    2,
		LastSeq:        17,
		LifecycleState: "unknown",
		ReadModelState: "unknown",
	})
	if gotDegraded {
		t.Fatal("degraded = true, want false")
	}
}

func TestHealthBuildSource_FutureLastSeenClampsLag(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	gotSource, gotDegraded := buildHealthSource(healthSourceRow{
		id:         "src-future",
		format:     "codex",
		location:   "/tmp/future",
		enabled:    1,
		lastSeenAt: sql.NullInt64{Int64: nowUS + 10, Valid: true},
		lastSeq:    23,
	}, nowUS)

	assertHealthSourceEqual(t, gotSource, healthSource{
		ID:             "src-future",
		Format:         "codex",
		Location:       "/tmp/future",
		Enabled:        true,
		LastSeenAt:     ptrInt64(nowUS + 10),
		LagUS:          0,
		LastSeq:        23,
		LifecycleState: "unknown",
		ReadModelState: "unknown",
	})
	if gotDegraded {
		t.Fatal("degraded = true, want false")
	}
}

func TestHealthBuildSource_LegacyLastSeenLagDoesNotDegrade(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	gotSource, gotDegraded := buildHealthSource(healthSourceRow{
		id:         "src-stale",
		format:     "opencode",
		location:   "/tmp/stale",
		enabled:    1,
		lastSeenAt: sql.NullInt64{Int64: nowUS - degradedLagThresholdUS - 1, Valid: true},
		lastSeq:    31,
	}, nowUS)

	assertHealthSourceEqual(t, gotSource, healthSource{
		ID:             "src-stale",
		Format:         "opencode",
		Location:       "/tmp/stale",
		Enabled:        true,
		LastSeenAt:     ptrInt64(nowUS - degradedLagThresholdUS - 1),
		LagUS:          degradedLagThresholdUS + 1,
		LastSeq:        31,
		LifecycleState: "unknown",
		ReadModelState: "unknown",
	})
	if gotDegraded {
		t.Fatal("degraded = true, want false; lifecycle state drives freshness after SOW-0114")
	}
}

func TestHealthBuildSource_TailingWithStaleHeartbeatIsEffectiveTailStale(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	gotSource, gotDegraded := buildHealthSource(healthSourceRow{
		id:               "src-tail-stale",
		format:           "codex",
		location:         "/tmp/codex",
		enabled:          1,
		lastSeq:          88,
		lifecycleState:   "tailing",
		lifecycleStateAt: sql.NullInt64{Int64: nowUS - tailStaleThresholdUS - 1, Valid: true},
		tailStartedAt:    sql.NullInt64{Int64: nowUS - tailStaleThresholdUS - 1, Valid: true},
		tailHeartbeatAt:  sql.NullInt64{Int64: nowUS - tailStaleThresholdUS - 1, Valid: true},
		readModelState:   "ready",
	}, nowUS)

	if gotSource.LifecycleState != "tail_stale" {
		t.Fatalf("lifecycle_state = %q, want effective tail_stale", gotSource.LifecycleState)
	}
	if !gotDegraded {
		t.Fatal("degraded = false, want true for stale tail heartbeat")
	}
}

func TestHealthBuildSource_TailingWithinHeartbeatGraceIsHealthy(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	gotSource, gotDegraded := buildHealthSource(healthSourceRow{
		id:               "src-tail-healthy",
		format:           "codex",
		location:         "/tmp/codex",
		enabled:          1,
		lastSeq:          89,
		lifecycleState:   "tailing",
		lifecycleStateAt: sql.NullInt64{Int64: nowUS - tailStaleThresholdUS + 1, Valid: true},
		tailStartedAt:    sql.NullInt64{Int64: nowUS - tailStaleThresholdUS + 1, Valid: true},
		tailHeartbeatAt:  sql.NullInt64{Int64: nowUS - tailStaleThresholdUS + 1, Valid: true},
		readModelState:   "ready",
	}, nowUS)

	if gotSource.LifecycleState != "tailing" {
		t.Fatalf("lifecycle_state = %q, want tailing", gotSource.LifecycleState)
	}
	if gotDegraded {
		t.Fatal("degraded = true, want false inside heartbeat grace")
	}
}

func TestHealthBuildSource_TailingFirstHeartbeatGrace(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	tests := []struct {
		name        string
		startedAt   sql.NullInt64
		wantState   string
		wantDegrade bool
	}{
		{
			name:        "recent start without heartbeat stays tailing",
			startedAt:   sql.NullInt64{Int64: nowUS - tailStaleThresholdUS + 1, Valid: true},
			wantState:   "tailing",
			wantDegrade: false,
		},
		{
			name:        "stale start without heartbeat becomes tail_stale",
			startedAt:   sql.NullInt64{Int64: nowUS - tailStaleThresholdUS - 1, Valid: true},
			wantState:   "tail_stale",
			wantDegrade: true,
		},
		{
			name:        "missing start and heartbeat becomes tail_stale",
			startedAt:   sql.NullInt64{},
			wantState:   "tail_stale",
			wantDegrade: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotSource, gotDegraded := buildHealthSource(healthSourceRow{
				id:              "src-first-heartbeat",
				format:          "codex",
				location:        "/tmp/codex",
				enabled:         1,
				lifecycleState:  "tailing",
				tailStartedAt:   tt.startedAt,
				tailHeartbeatAt: sql.NullInt64{},
				readModelState:  "ready",
			}, nowUS)
			if gotSource.LifecycleState != tt.wantState {
				t.Fatalf("lifecycle_state = %q, want %q", gotSource.LifecycleState, tt.wantState)
			}
			if gotDegraded != tt.wantDegrade {
				t.Fatalf("degraded = %v, want %v", gotDegraded, tt.wantDegrade)
			}
		})
	}
}

func TestHealthBuildSource_PreTailAndLongScanAgeDegrade(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	tests := []struct {
		name  string
		row   healthSourceRow
		state string
	}{
		{
			name: "starting beyond pre-tail grace",
			row: healthSourceRow{
				lifecycleState:   "starting",
				lifecycleStateAt: sql.NullInt64{Int64: nowUS - preTailGraceThresholdUS - 1, Valid: true},
			},
			state: "starting",
		},
		{
			name: "tail_starting beyond pre-tail grace",
			row: healthSourceRow{
				lifecycleState:   "tail_starting",
				lifecycleStateAt: sql.NullInt64{Int64: nowUS - preTailGraceThresholdUS - 1, Valid: true},
			},
			state: "tail_starting",
		},
		{
			name: "scanning beyond long-scan threshold",
			row: healthSourceRow{
				lifecycleState: "scanning",
				scanStartedAt:  sql.NullInt64{Int64: nowUS - longScanThresholdUS - 1, Valid: true},
			},
			state: "scanning",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.row.id = "src-age"
			tt.row.format = "aiagent_v3"
			tt.row.location = "/tmp/age"
			tt.row.enabled = 1
			tt.row.readModelState = "ready"

			gotSource, gotDegraded := buildHealthSource(tt.row, nowUS)
			if gotSource.LifecycleState != tt.state {
				t.Fatalf("lifecycle_state = %q, want %q", gotSource.LifecycleState, tt.state)
			}
			if !gotDegraded {
				t.Fatal("degraded = false, want true")
			}
		})
	}
}

func TestHealthBuildSource_ReadModelRepairGrace(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	tests := []struct {
		name        string
		stateAt     int64
		wantDegrade bool
	}{
		{name: "within grace", stateAt: nowUS - readModelRepairGraceThresholdUS + 1, wantDegrade: false},
		{name: "beyond grace", stateAt: nowUS - readModelRepairGraceThresholdUS - 1, wantDegrade: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, gotDegraded := buildHealthSource(healthSourceRow{
				id:               "src-repair",
				format:           "opencode",
				location:         "/tmp/opencode",
				enabled:          1,
				lifecycleState:   "tailing",
				tailStartedAt:    sql.NullInt64{Int64: nowUS, Valid: true},
				tailHeartbeatAt:  sql.NullInt64{Int64: nowUS, Valid: true},
				readModelState:   "repair_pending",
				readModelStateAt: sql.NullInt64{Int64: tt.stateAt, Valid: true},
			}, nowUS)
			if gotDegraded != tt.wantDegrade {
				t.Fatalf("degraded = %v, want %v", gotDegraded, tt.wantDegrade)
			}
		})
	}
}

func TestHealthBuildSource_TailRestartingGrace(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	tests := []struct {
		name             string
		stateAt          int64
		tailRestartCount int64
		wantDegrade      bool
	}{
		{
			name:             "single restart within long scan grace",
			stateAt:          nowUS - longScanThresholdUS + 1,
			tailRestartCount: 1,
			wantDegrade:      false,
		},
		{
			name:             "single restart beyond long scan grace",
			stateAt:          nowUS - longScanThresholdUS - 1,
			tailRestartCount: 1,
			wantDegrade:      true,
		},
		{
			name:             "repeated restart degrades immediately",
			stateAt:          nowUS,
			tailRestartCount: 2,
			wantDegrade:      true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, gotDegraded := buildHealthSource(healthSourceRow{
				id:               "src-restart",
				format:           "codex",
				location:         "/tmp/codex",
				enabled:          1,
				lifecycleState:   "tail_restarting",
				lifecycleStateAt: sql.NullInt64{Int64: tt.stateAt, Valid: true},
				tailRestartCount: tt.tailRestartCount,
				readModelState:   "ready",
			}, nowUS)
			if gotDegraded != tt.wantDegrade {
				t.Fatalf("degraded = %v, want %v", gotDegraded, tt.wantDegrade)
			}
		})
	}
}

func TestHealthBuildSource_StoppedIgnoresReadModelDegradation(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	gotSource, gotDegraded := buildHealthSource(healthSourceRow{
		id:               "src-stopped",
		format:           "codex",
		location:         "/tmp/stopped",
		enabled:          1,
		lifecycleState:   "stopped",
		lifecycleStateAt: sql.NullInt64{Int64: nowUS, Valid: true},
		readModelState:   "repair_failed",
		readModelStateAt: sql.NullInt64{Int64: nowUS - readModelRepairGraceThresholdUS - 1, Valid: true},
		readModelError:   sql.NullString{String: "historical repair failure", Valid: true},
	}, nowUS)

	if gotSource.LifecycleState != "stopped" {
		t.Fatalf("lifecycle_state = %q, want stopped", gotSource.LifecycleState)
	}
	if gotSource.ReadModelState != "repair_failed" {
		t.Fatalf("read_model_state = %q, want repair_failed", gotSource.ReadModelState)
	}
	if gotDegraded {
		t.Fatal("degraded = true, want false for stopped source with historical read-model failure")
	}
}

func TestHealthBuildSource_DisabledStaleSourceDoesNotDegrade(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	gotSource, gotDegraded := buildHealthSource(healthSourceRow{
		id:         "src-disabled",
		format:     "claude_code",
		location:   "/tmp/disabled",
		enabled:    0,
		lastSeenAt: sql.NullInt64{Int64: nowUS - degradedLagThresholdUS - 1, Valid: true},
		lastSeq:    43,
	}, nowUS)

	assertHealthSourceEqual(t, gotSource, healthSource{
		ID:             "src-disabled",
		Format:         "claude_code",
		Location:       "/tmp/disabled",
		Enabled:        false,
		LastSeenAt:     ptrInt64(nowUS - degradedLagThresholdUS - 1),
		LagUS:          degradedLagThresholdUS + 1,
		LastSeq:        43,
		LifecycleState: "unknown",
		ReadModelState: "unknown",
	})
	if gotDegraded {
		t.Fatal("degraded = true, want false")
	}
}

func TestHealthStatusFromSignals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		queriesFailed     int
		totalQueries      int
		sourceDegraded    bool
		recentParseErrors int64
		want              string
	}{
		{
			name:          "all core queries failed is down",
			queriesFailed: 2,
			totalQueries:  2,
			want:          healthStatusDown,
		},
		{
			name:          "partial query failure without degraded signals stays ok",
			queriesFailed: 1,
			totalQueries:  2,
			want:          healthStatusOK,
		},
		{
			name:           "source lifecycle signal is degraded",
			totalQueries:   2,
			sourceDegraded: true,
			want:           healthStatusDegraded,
		},
		{
			name:              "recent parse errors are degraded",
			totalQueries:      2,
			recentParseErrors: 1,
			want:              healthStatusDegraded,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := healthStatusFromSignals(tt.queriesFailed, tt.totalQueries, tt.sourceDegraded, tt.recentParseErrors)
			if got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadHealthSourceRows_ScanErrorReturnsNilSources(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	nowUS := fixedTime.UnixMicro()
	rows, err := db.Query(`
SELECT 'src-ok', 'aiagent_v3', '/tmp/ok', 1, ?, 0, 100,
       1000, 'tail_failed', 1000, NULL, NULL, NULL, NULL, 1000, 0, NULL,
       'unknown', NULL, NULL, NULL, NULL, 0, NULL, NULL
UNION ALL
SELECT 'src-bad', 'codex', '/tmp/bad', 'not-enabled', NULL, 0, 101,
       NULL, 'unknown', NULL, NULL, NULL, NULL, NULL, NULL, 0, NULL,
       'unknown', NULL, NULL, NULL, NULL, 0, NULL, NULL
`, nowUS-degradedLagThresholdUS-1)
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	defer func() { _ = rows.Close() }()

	sources, lagDegraded, scanErr := readHealthSourceRows(rows, nil, nowUS)
	if scanErr == nil {
		t.Fatal("err = nil, want scan error")
	}
	if sources != nil {
		t.Fatalf("sources = %#v, want nil on scan error", sources)
	}
	if !lagDegraded {
		t.Fatal("sourceDegraded = false, want true from already-scanned failed source")
	}
}

func ptrInt64(v int64) *int64 {
	return &v
}

func assertHealthSourceEqual(t *testing.T, got, want healthSource) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("source mismatch (-want +got):\n%s", diff)
	}
}
