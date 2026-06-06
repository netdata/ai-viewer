package claude_code

import "testing"

// TestRestoreParked verifies the cursor-park restore logic (P2.4d): a nil
// deferral is a no-op; an already-finalized child is not re-restored; an entry
// already present in completed is left untouched; a fresh entry is added.
func TestRestoreParked(t *testing.T) {
	t.Parallel()
	// nil receiver must not panic.
	var nilDef *tailDeferral
	nilDef.restoreParked(map[string]int64{"x": 1})

	d := newTailDeferral()
	d.finalized["already-done"] = struct{}{}
	d.completed["already-parked"] = completionState{tsUs: 111}

	d.restoreParked(map[string]int64{
		"already-done":   999, // finalized -> must NOT be restored
		"already-parked": 222, // present -> must keep its existing ts (111)
		"fresh":          333, // new -> added
	})

	if _, ok := d.completed["already-done"]; ok {
		t.Error("restoreParked re-restored an already-finalized child")
	}
	if st := d.completed["already-parked"]; st.tsUs != 111 {
		t.Errorf("restoreParked overwrote an existing parked entry: ts=%d, want 111", st.tsUs)
	}
	if st, ok := d.completed["fresh"]; !ok || st.tsUs != 333 {
		t.Errorf("restoreParked did not add the fresh entry: %+v ok=%v", st, ok)
	}
}

// TestParkedSnapshot verifies parkedSnapshot projects completed -> child->ts
// and that a nil deferral returns nil.
func TestParkedSnapshot(t *testing.T) {
	t.Parallel()
	var nilDef *tailDeferral
	if nilDef.parkedSnapshot() != nil {
		t.Error("parkedSnapshot on nil deferral should be nil")
	}
	d := newTailDeferral()
	d.completed["c1"] = completionState{tsUs: 42}
	d.completed["c2"] = completionState{tsUs: 7}
	snap := d.parkedSnapshot()
	if snap["c1"] != 42 || snap["c2"] != 7 || len(snap) != 2 {
		t.Errorf("parkedSnapshot = %v, want {c1:42,c2:7}", snap)
	}
}

// TestRestoreFinalized verifies restoreFinalized seeds the deferral's finalized
// set additively and tolerates a nil receiver (P2.5c).
func TestRestoreFinalized(t *testing.T) {
	t.Parallel()
	var nilDef *tailDeferral
	nilDef.restoreFinalized(map[string]struct{}{"x": {}})

	d := newTailDeferral()
	d.finalized["existing"] = struct{}{}
	d.restoreFinalized(map[string]struct{}{"existing": {}, "fresh": {}})
	if _, ok := d.finalized["existing"]; !ok {
		t.Error("restoreFinalized dropped an existing entry")
	}
	if _, ok := d.finalized["fresh"]; !ok {
		t.Error("restoreFinalized did not add the fresh entry")
	}
}

// TestFinalizedSnapshot verifies finalizedSnapshot copies the finalized set and
// that a nil deferral returns nil (P2.5c).
func TestFinalizedSnapshot(t *testing.T) {
	t.Parallel()
	var nilDef *tailDeferral
	if nilDef.finalizedSnapshot() != nil {
		t.Error("finalizedSnapshot on nil deferral should be nil")
	}
	d := newTailDeferral()
	d.finalized["c1"] = struct{}{}
	d.finalized["c2"] = struct{}{}
	snap := d.finalizedSnapshot()
	if _, ok := snap["c1"]; !ok {
		t.Error("finalizedSnapshot missing c1")
	}
	if _, ok := snap["c2"]; !ok {
		t.Error("finalizedSnapshot missing c2")
	}
	if len(snap) != 2 {
		t.Errorf("finalizedSnapshot len = %d, want 2", len(snap))
	}
	// Mutating the snapshot must not affect the live set.
	delete(snap, "c1")
	if _, ok := d.finalized["c1"]; !ok {
		t.Error("finalizedSnapshot returned an aliased map (mutation leaked)")
	}
}
