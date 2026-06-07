package presenter

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/netdata/ai-viewer/internal/rollups"
)

func TestParseStatsTopRequestDefaultsAndForcesAllSessions_Defaults(t *testing.T) {
	t.Parallel()

	params := mustParseStatsTopRequest(t, "")
	if params.bucket != rollups.Daily {
		t.Fatalf("bucket = %v, want daily", params.bucket)
	}
	if params.dim.dimension != "model" {
		t.Fatalf("dimension = %q, want model", params.dim.dimension)
	}
	if params.metric != smCost {
		t.Fatalf("metric = %v, want cost", params.metric)
	}
	if params.n != defaultTopN {
		t.Fatalf("n = %d, want %d", params.n, defaultTopN)
	}
}

func TestParseStatsTopRequestDefaultsAndForcesAllSessions_ForcesAllScope(t *testing.T) {
	t.Parallel()

	params := mustParseStatsTopRequest(t, "group="+groupRoot)
	if params.filter.group != groupAll {
		t.Fatalf("filter.group = %q, want %q", params.filter.group, groupAll)
	}
	if params.filter.to == nil || *params.filter.to != fixedTime.UnixMicro() {
		t.Fatalf("filter.to = %v, want fixed now", params.filter.to)
	}
}

func mustParseStatsTopRequest(t *testing.T, raw string) statsTopRequest {
	t.Helper()

	params, parseErr := parseStatsTopRequest(parseValues(t, raw), fixedTime)
	if parseErr != nil {
		t.Fatalf("parseStatsTopRequest: %+v", parseErr)
	}
	return params
}

func TestParseStatsTopRequestClassifiesBadParams(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		raw           string
		wantBadEnum   string
		wantFilterErr bool
	}{
		{name: "bucket", raw: "bucket=weekly", wantBadEnum: "bucket"},
		{name: "dimension", raw: "dimension=source_format", wantBadEnum: "dimension"},
		{name: "metric", raw: "metric=bogus", wantBadEnum: "metric"},
		{name: "n", raw: "n=abc", wantFilterErr: true},
		{name: "filter", raw: "from=9&to=1", wantFilterErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, parseErr := parseStatsTopRequest(parseValues(t, tc.raw), fixedTime)
			if parseErr == nil {
				t.Fatal("parseStatsTopRequest returned nil error")
			}
			if parseErr.badEnum != tc.wantBadEnum {
				t.Fatalf("badEnum = %q, want %q", parseErr.badEnum, tc.wantBadEnum)
			}
			if tc.wantFilterErr && !errors.Is(parseErr.filterErr, errBadFilter) {
				t.Fatalf("filterErr = %v, want errBadFilter", parseErr.filterErr)
			}
			if !tc.wantFilterErr && parseErr.filterErr != nil {
				t.Fatalf("filterErr = %v, want nil", parseErr.filterErr)
			}
		})
	}
}

func TestBuildStatsTopResponseSortsLimitsAndKeepsEmptySlice(t *testing.T) {
	t.Parallel()

	resp := buildStatsTopResponse(
		map[string]float64{"beta": 2, "alpha": 2, "gamma": 1},
		rollupDimension{dimension: "agent"},
		smCalls,
		2,
	)

	if resp.Dimension != "agent" || resp.Metric != "calls" {
		t.Fatalf("echo = %q/%q, want agent/calls", resp.Dimension, resp.Metric)
	}
	want := []seriesItem{{Key: "alpha", Value: 2}, {Key: "beta", Value: 2}}
	if diff := cmp.Diff(want, resp.Items); diff != "" {
		t.Fatalf("items mismatch (-want +got):\n%s", diff)
	}

	empty := buildStatsTopResponse(nil, rollupDimension{dimension: "model"}, smCost, defaultTopN)
	if empty.Items == nil {
		t.Fatal("empty Items is nil, want empty slice")
	}
	if len(empty.Items) != 0 {
		t.Fatalf("empty Items = %+v, want empty", empty.Items)
	}
}
