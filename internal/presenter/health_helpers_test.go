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
		ID:          "src-null",
		Format:      "aiagent_v3",
		Location:    "/tmp/null",
		Enabled:     true,
		ParseErrors: 2,
		LastSeq:     17,
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
		ID:         "src-future",
		Format:     "codex",
		Location:   "/tmp/future",
		Enabled:    true,
		LastSeenAt: ptrInt64(nowUS + 10),
		LagUS:      0,
		LastSeq:    23,
	})
	if gotDegraded {
		t.Fatal("degraded = true, want false")
	}
}

func TestHealthBuildSource_EnabledStaleSourceDegrades(t *testing.T) {
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
		ID:         "src-stale",
		Format:     "opencode",
		Location:   "/tmp/stale",
		Enabled:    true,
		LastSeenAt: ptrInt64(nowUS - degradedLagThresholdUS - 1),
		LagUS:      degradedLagThresholdUS + 1,
		LastSeq:    31,
	})
	if !gotDegraded {
		t.Fatal("degraded = false, want true")
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
		ID:         "src-disabled",
		Format:     "claude_code",
		Location:   "/tmp/disabled",
		Enabled:    false,
		LastSeenAt: ptrInt64(nowUS - degradedLagThresholdUS - 1),
		LagUS:      degradedLagThresholdUS + 1,
		LastSeq:    43,
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
		lagDegraded       bool
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
			name:         "lag signal is degraded",
			totalQueries: 2,
			lagDegraded:  true,
			want:         healthStatusDegraded,
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
			got := healthStatusFromSignals(tt.queriesFailed, tt.totalQueries, tt.lagDegraded, tt.recentParseErrors)
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
SELECT 'src-ok', 'aiagent_v3', '/tmp/ok', 1, ?, 0, 100, NULL
UNION ALL
SELECT 'src-bad', 'codex', '/tmp/bad', 'not-enabled', NULL, 0, 101, NULL
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
		t.Fatal("lagDegraded = false, want true from already-scanned stale source")
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
