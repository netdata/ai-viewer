package presenter

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFilterScalarHelpers(t *testing.T) {
	t.Parallel()

	t.Run("group", func(t *testing.T) {
		t.Parallel()
		f := sessionFilter{group: groupRoot}
		if err := applyGroupScalar("", &f); err != nil {
			t.Fatalf("empty group: %v", err)
		}
		if f.group != groupRoot {
			t.Fatalf("empty group changed group to %q", f.group)
		}
		if err := applyGroupScalar(groupAll, &f); err != nil {
			t.Fatalf("group=all: %v", err)
		}
		if f.group != groupAll {
			t.Fatalf("group = %q, want %q", f.group, groupAll)
		}
		if err := applyGroupScalar("many", &f); err == nil {
			t.Fatal("invalid group: want error")
		}
	})

	t.Run("sort", func(t *testing.T) {
		t.Parallel()
		for _, value := range []string{"", sortStartTS} {
			if err := validateSortScalar(value); err != nil {
				t.Fatalf("sort %q: %v", value, err)
			}
		}
		if err := validateSortScalar("cost"); err == nil {
			t.Fatal("invalid sort: want error")
		}
	})

	t.Run("order", func(t *testing.T) {
		t.Parallel()
		f := sessionFilter{order: "desc"}
		if err := applyOrderScalar("", &f); err != nil {
			t.Fatalf("empty order: %v", err)
		}
		if f.order != "desc" {
			t.Fatalf("empty order changed order to %q", f.order)
		}
		if err := applyOrderScalar("asc", &f); err != nil {
			t.Fatalf("order=asc: %v", err)
		}
		if f.order != "asc" {
			t.Fatalf("order = %q, want asc", f.order)
		}
		if err := applyOrderScalar("up", &f); err == nil {
			t.Fatal("invalid order: want error")
		}
	})

	t.Run("limit", func(t *testing.T) {
		t.Parallel()
		f := sessionFilter{limit: defaultLimit}
		if err := applyLimitScalar("", &f); err != nil {
			t.Fatalf("empty limit: %v", err)
		}
		if f.limit != defaultLimit {
			t.Fatalf("empty limit changed limit to %d", f.limit)
		}
		if err := applyLimitScalar("999", &f); err != nil {
			t.Fatalf("limit=999: %v", err)
		}
		if f.limit != 999 {
			t.Fatalf("limit = %d, want 999", f.limit)
		}
		for _, value := range []string{"abc", "0", "-1", "1001"} {
			if err := applyLimitScalar(value, &f); err == nil {
				t.Fatalf("limit=%q: want error", value)
			}
		}
	})
}

func TestDimensionBuilderHelpers(t *testing.T) {
	t.Parallel()

	builder := sessionDimensionBuilder{alias: "s"}
	builder.addRootGroup(groupAll)
	if diff := cmp.Diff([]string(nil), builder.conds); diff != "" {
		t.Fatalf("group=all conds mismatch (-want +got):\n%s", diff)
	}

	builder.addRootGroup(groupRoot)
	builder.addIn("s.agent_name", []string{"agent-a"})
	builder.addSearch(`a%b_c\`)
	builder.addTools([]string{"bash", "read"})

	wantConds := []string{
		"s.kind = ?",
		"s.agent_name IN (?)",
		"s.agent_name LIKE ? ESCAPE '\\'",
		"EXISTS (SELECT 1 FROM ops o WHERE o.session_id = s.id AND o.kind = 'tool' AND o.name IN (?,?))",
	}
	if diff := cmp.Diff(wantConds, builder.conds); diff != "" {
		t.Fatalf("conds mismatch (-want +got):\n%s", diff)
	}

	wantArgs := []any{"root", "agent-a", `%` + escapeLike(`a%b_c\`) + `%`, "bash", "read"}
	if diff := cmp.Diff(wantArgs, builder.args); diff != "" {
		t.Fatalf("args mismatch (-want +got):\n%s", diff)
	}
}

func TestDimensionConds_ConditionAndArgOrder(t *testing.T) {
	t.Parallel()

	f := sessionFilter{
		group:  groupRoot,
		agents: []string{"agent-a"},
		models: []string{"model-a", "model-b"},
		status: []string{"running"},
		source: []string{"source-a"},
		q:      `needle_%\`,
		tools:  []string{"bash", "read"},
	}

	conds, args := f.dimensionConds("s")
	wantConds := []string{
		"s.kind = ?",
		"s.agent_name IN (?)",
		"s.model IN (?,?)",
		"s.status IN (?)",
		"s.source_id IN (?)",
		"s.agent_name LIKE ? ESCAPE '\\'",
		"EXISTS (SELECT 1 FROM ops o WHERE o.session_id = s.id AND o.kind = 'tool' AND o.name IN (?,?))",
	}
	if diff := cmp.Diff(wantConds, conds); diff != "" {
		t.Fatalf("conds mismatch (-want +got):\n%s", diff)
	}

	wantArgs := []any{
		"root",
		"agent-a",
		"model-a",
		"model-b",
		"running",
		"source-a",
		"%" + escapeLike(`needle_%\`) + "%",
		"bash",
		"read",
	}
	if diff := cmp.Diff(wantArgs, args); diff != "" {
		t.Fatalf("args mismatch (-want +got):\n%s", diff)
	}
}
