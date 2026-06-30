package presenter

import (
	"database/sql"
	"testing"

	"github.com/google/go-cmp/cmp"

	_ "modernc.org/sqlite"
)

func TestSourcesBuildItem_NullableProgressFieldsAbsent(t *testing.T) {
	t.Parallel()

	got := buildSourceItem(sourceItemRow{
		id:        "src-empty",
		format:    "aiagent_v3",
		location:  "/tmp/empty",
		enabled:   1,
		createdAt: 100,
		cursor:    "",
		lastSeq:   0,
	}, fixedTime.UnixMicro())

	assertSourceItemEqual(t, got, sourceItem{
		ID:             "src-empty",
		Format:         "aiagent_v3",
		Location:       "/tmp/empty",
		Enabled:        true,
		CreatedAt:      100,
		Cursor:         "",
		LastSeq:        0,
		LifecycleState: "unknown",
		ReadModelState: "unknown",
	})
}

func TestSourcesBuildItem_ValidNullableFields(t *testing.T) {
	t.Parallel()

	got := buildSourceItem(sourceItemRow{
		id:          "src-full",
		format:      "codex",
		location:    "/tmp/full",
		enabled:     0,
		parseErrors: 3,
		lastSeenAt:  sql.NullInt64{Int64: 101, Valid: true},
		createdAt:   100,
		cursor:      `{"offset":42}`,
		lastSeq:     999,
		lastTsUS:    sql.NullInt64{Int64: 102, Valid: true},
		updatedAt:   sql.NullInt64{Int64: 103, Valid: true},
	}, fixedTime.UnixMicro())

	assertSourceItemEqual(t, got, sourceItem{
		ID:                "src-full",
		Format:            "codex",
		Location:          "/tmp/full",
		Enabled:           false,
		ParseErrors:       3,
		LastSeenAt:        ptrInt64(101),
		CreatedAt:         100,
		Cursor:            `{"offset":42}`,
		LastSeq:           999,
		LastTsUS:          ptrInt64(102),
		UpdatedAt:         ptrInt64(103),
		ProgressUpdatedAt: ptrInt64(103),
		LifecycleState:    "unknown",
		ReadModelState:    "unknown",
	})
}

func TestSourcesBuildItem_TailingWithStaleHeartbeatIsEffectiveTailStale(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	got := buildSourceItem(sourceItemRow{
		id:               "src-tail-stale",
		format:           "codex",
		location:         "/tmp/codex",
		enabled:          1,
		createdAt:        100,
		lifecycleState:   "tailing",
		lifecycleStateAt: sql.NullInt64{Int64: nowUS - tailStaleThresholdUS - 1, Valid: true},
		tailStartedAt:    sql.NullInt64{Int64: nowUS - tailStaleThresholdUS - 1, Valid: true},
		tailHeartbeatAt:  sql.NullInt64{Int64: nowUS - tailStaleThresholdUS - 1, Valid: true},
		readModelState:   "ready",
	}, nowUS)

	if got.LifecycleState != "tail_stale" {
		t.Fatalf("lifecycle_state = %q, want effective tail_stale", got.LifecycleState)
	}
}

func TestSourcesBuildItem_TailingFirstHeartbeatGrace(t *testing.T) {
	t.Parallel()

	nowUS := fixedTime.UnixMicro()
	tests := []struct {
		name      string
		startedAt sql.NullInt64
		want      string
	}{
		{
			name:      "recent start without heartbeat stays tailing",
			startedAt: sql.NullInt64{Int64: nowUS - tailStaleThresholdUS + 1, Valid: true},
			want:      "tailing",
		},
		{
			name:      "stale start without heartbeat becomes tail_stale",
			startedAt: sql.NullInt64{Int64: nowUS - tailStaleThresholdUS - 1, Valid: true},
			want:      "tail_stale",
		},
		{
			name:      "missing start and heartbeat becomes tail_stale",
			startedAt: sql.NullInt64{},
			want:      "tail_stale",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildSourceItem(sourceItemRow{
				id:              "src-first-heartbeat",
				format:          "codex",
				location:        "/tmp/codex",
				enabled:         1,
				createdAt:       100,
				lifecycleState:  "tailing",
				tailStartedAt:   tt.startedAt,
				tailHeartbeatAt: sql.NullInt64{},
				readModelState:  "ready",
			}, nowUS)
			if got.LifecycleState != tt.want {
				t.Fatalf("lifecycle_state = %q, want %q", got.LifecycleState, tt.want)
			}
		})
	}
}

func TestReadSourceItemRows_ScanErrorReturnsNilItems(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	rows, err := db.Query(`
SELECT 'src-ok', 'aiagent_v3', '/tmp/ok', 1, 0, NULL, 100, '', 0, NULL, NULL, NULL
UNION ALL
SELECT 'src-bad', 'codex', '/tmp/bad', 'not-enabled', 0, NULL, 101, '', 0, NULL, NULL, NULL
`)
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	defer func() { _ = rows.Close() }()

	items, failure := readSourceItemRows(rows, nil, fixedTime.UnixMicro())
	if failure.err == nil {
		t.Fatal("failure.err = nil, want scan error")
	}
	if failure.kind != sourceItemsFailureScan {
		t.Fatalf("failure.kind = %d, want %d", failure.kind, sourceItemsFailureScan)
	}
	if items != nil {
		t.Fatalf("items = %#v, want nil on scan error", items)
	}
}

func assertSourceItemEqual(t *testing.T, got, want sourceItem) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("source item mismatch (-want +got):\n%s", diff)
	}
}
