package presenter

import (
	"reflect"
	"strings"
	"testing"
)

func TestCrossAgentsSelectBindsFiltersAndLimit(t *testing.T) {
	t.Parallel()

	from := int64(100)
	to := int64(200)
	f := sessionFilter{
		from:   &from,
		to:     &to,
		agents: []string{"nedi"},
		source: []string{"src1"},
		group:  groupAll,
	}

	query, args := crossAgentsSelect(f, metricTokens, 3)

	if !strings.Contains(query, "(s.tokens_in + s.tokens_out) AS size_metric") {
		t.Fatalf("query missing tokens size metric:\n%s", query)
	}
	for _, fragment := range []string{
		"s.agent_name IN (?)",
		"s.source_id IN (?)",
		"s.start_ts >= ?",
		"s.start_ts <= ?",
		"ORDER BY size_metric DESC, s.id ASC",
		"LIMIT ?",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query missing %q:\n%s", fragment, query)
		}
	}
	for _, rawValue := range []string{"nedi", "src1"} {
		if strings.Contains(query, rawValue) {
			t.Fatalf("query interpolated %q instead of binding it:\n%s", rawValue, query)
		}
	}
	wantArgs := []any{"nedi", "src1", from, to, 4}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestCrossAgentsSelectDurationProjectsZeroButOrdersByIndexedColumn(t *testing.T) {
	t.Parallel()

	query, args := crossAgentsSelect(sessionFilter{group: groupAll}, metricDuration, 10)

	for _, fragment := range []string{
		"COALESCE(s.duration_us, 0) AS size_metric",
		"ORDER BY s.duration_us DESC, s.id ASC",
		"LIMIT ?",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("duration query missing %q:\n%s", fragment, query)
		}
	}
	wantArgs := []any{11}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestCrossAgentRowAgentMapsLabelAndMetrics(t *testing.T) {
	t.Parallel()

	row := crossAgentRow{
		id:           "rootA",
		parentID:     "parentA",
		agentName:    "",
		kind:         "root",
		rootID:       "rootA",
		sizeMetric:   12.5,
		failureRatio: 0.25,
	}

	got := row.agent()
	want := crossAgent{
		id:           "rootA",
		parentID:     "parentA",
		label:        "root (root)",
		sizeMetric:   12.5,
		failureRatio: 0.25,
	}
	if got != want {
		t.Fatalf("agent = %+v, want %+v", got, want)
	}
}

func TestTrimCrossAgentsToLimit(t *testing.T) {
	t.Parallel()

	agents := []crossAgent{{id: "a"}, {id: "b"}, {id: "c"}}
	got, truncated := trimCrossAgentsToLimit(agents, 2)
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	if len(got) != 2 || got[0].id != "a" || got[1].id != "b" {
		t.Fatalf("kept agents = %+v, want first two", got)
	}

	got, truncated = trimCrossAgentsToLimit(agents[:2], 2)
	if truncated {
		t.Fatal("equal-to-limit truncated = true, want false")
	}
	if len(got) != 2 {
		t.Fatalf("equal-to-limit agents = %d, want 2", len(got))
	}
}
