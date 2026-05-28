package presenter

import (
	"strings"
	"testing"
)

// TestFingerprint_IsCanonicalStringNotHash pins codex iter-5: the cursor FP
// carries the canonical length-prefixed STRING itself, not a fixed-width
// digest. A 64-bit hash is finite, so distinct canonical strings could collide
// — the fingerprint therefore returns b.String() directly. The assertions here
// fail against the old hashKey(b.String()) form because a hex digest contains
// none of the human-readable tokens and is far shorter than the encoded query.
func TestFingerprint_IsCanonicalStringNotHash(t *testing.T) {
	t.Parallel()
	f := sessionFilter{group: groupRoot, sort: sortStartTS, order: "desc",
		models: []string{"alpha", "beta"}}
	fp := f.fingerprint()
	// The canonical string embeds the dimension names verbatim.
	for _, tok := range []string{"models", "agents", "status", "sort", "order"} {
		if !strings.Contains(fp, tok) {
			t.Fatalf("fingerprint %q missing canonical token %q (looks hashed)", fp, tok)
		}
	}
	// It embeds the supplied values verbatim, length-prefixed (`5:alpha`).
	if !strings.Contains(fp, "5:alpha") || !strings.Contains(fp, "4:beta") {
		t.Fatalf("fingerprint %q missing length-prefixed values; not the canonical string", fp)
	}
	// A 64-bit FNV hex digest is at most 16 chars; the canonical string for a
	// populated query is much longer. Pin "not a short hex digest".
	if len(fp) <= 16 {
		t.Fatalf("fingerprint %q is %d bytes — looks like a fixed-width hash, not the canonical string", fp, len(fp))
	}
}

// TestLogFingerprint_IsCanonicalStringNotHash mirrors the sessions assertion
// for the logs fingerprint: it must carry the canonical length-prefixed string
// (path id + severity set + fixed ordering), not a hashed digest.
func TestLogFingerprint_IsCanonicalStringNotHash(t *testing.T) {
	t.Parallel()
	lf := logFilter{id: "sess-1", severities: []string{"ERR", "WRN"}}
	fp := lf.fingerprint()
	for _, tok := range []string{"id", "severity", "sort", "order"} {
		if !strings.Contains(fp, tok) {
			t.Fatalf("logs fingerprint %q missing canonical token %q (looks hashed)", fp, tok)
		}
	}
	if !strings.Contains(fp, "6:sess-1") {
		t.Fatalf("logs fingerprint %q missing length-prefixed id; not the canonical string", fp)
	}
	if len(fp) <= 16 {
		t.Fatalf("logs fingerprint %q is %d bytes — looks hashed, not the canonical string", fp, len(fp))
	}
}

// TestFingerprint_SeparatorCollisionResolved pins codex iter-4 P2: under the
// old separator-join encoding two DIFFERENT array sets whose values embedded
// the record separator (\x1e) could serialize to the identical byte stream
// and therefore hash identically, letting a changed filter pass cursor
// validation. The length-prefixed encoding (writeLP / writeSortedDim) makes
// every token self-delimiting, so the two sets below — which collided under
// the old scheme — now produce DISTINCT fingerprints. The values are exercised
// directly at the fingerprint layer (the parser additionally rejects control
// chars as defense in depth; see TestParseSessionFilter_ControlCharsRejected).
func TestFingerprint_SeparatorCollisionResolved(t *testing.T) {
	t.Parallel()
	// {"a\x1eb", "c"} vs {"a", "b\x1ec"}: under separator-join both render the
	// elements as a<RS>b<RS>c and collide; length-prefixing keeps them apart.
	left := sessionFilter{group: groupRoot, sort: sortStartTS, order: "desc",
		models: []string{"a\x1eb", "c"}}
	right := sessionFilter{group: groupRoot, sort: sortStartTS, order: "desc",
		models: []string{"a", "b\x1ec"}}
	if left.fingerprint() == right.fingerprint() {
		t.Fatalf("crafted-collision sets share a fingerprint %q; length-prefixing failed",
			left.fingerprint())
	}
}

// TestFingerprint_CrossDimensionNoBleed asserts a value cannot impersonate a
// different array dimension by smuggling the field name into its content. The
// length-prefix on the dimension NAME and on the element COUNT make the stream
// unambiguous, so {agents:["models"]} and {models:["agents"]} differ.
func TestFingerprint_CrossDimensionNoBleed(t *testing.T) {
	t.Parallel()
	a := sessionFilter{group: groupRoot, sort: sortStartTS, order: "desc",
		agents: []string{"models"}}
	b := sessionFilter{group: groupRoot, sort: sortStartTS, order: "desc",
		models: []string{"agents"}}
	if a.fingerprint() == b.fingerprint() {
		t.Fatalf("cross-dimension values collide on fingerprint %q", a.fingerprint())
	}
}

// TestFingerprint_EmptyVsSingleEmptyElement asserts an absent dimension and a
// dimension carrying a single empty element do NOT collide: writeSortedDim
// encodes the element count so [] and [""] are distinguishable byte streams.
// (The parser rejects the all-empty case before this layer, but the encoding
// must still be unambiguous on its own.)
func TestFingerprint_EmptyVsSingleEmptyElement(t *testing.T) {
	t.Parallel()
	none := sessionFilter{group: groupRoot, sort: sortStartTS, order: "desc"}
	oneEmpty := sessionFilter{group: groupRoot, sort: sortStartTS, order: "desc",
		models: []string{""}}
	if none.fingerprint() == oneEmpty.fingerprint() {
		t.Fatalf("absent dimension collides with single-empty-element dimension")
	}
}

// TestFingerprint_OrderInsensitive is a pure-layer regression of the iter-3
// property the handler test (TestSessions_CursorFingerprintOrderInsensitive)
// also covers: each array dimension is sorted before encoding, so the SAME SET
// in a different order produces the SAME fingerprint even though the parser
// preserves first-appearance order for the SQL IN(...) list.
func TestFingerprint_OrderInsensitive(t *testing.T) {
	t.Parallel()
	ab := sessionFilter{group: groupRoot, sort: sortStartTS, order: "desc",
		models: []string{"a", "b"}, agents: []string{"x", "y"}}
	ba := sessionFilter{group: groupRoot, sort: sortStartTS, order: "desc",
		models: []string{"b", "a"}, agents: []string{"y", "x"}}
	if ab.fingerprint() != ba.fingerprint() {
		t.Fatalf("reordered same-set fingerprints differ: %q != %q",
			ab.fingerprint(), ba.fingerprint())
	}
}

// TestLogFingerprint_SeverityOrderInsensitive mirrors the sessions property
// for the logs fingerprint: the severity set is sorted before encoding, so a
// reordered same-set produces the SAME fingerprint (now via the shared
// writeSortedDim helper rather than the old inline separator join).
func TestLogFingerprint_SeverityOrderInsensitive(t *testing.T) {
	t.Parallel()
	a := logFilter{id: "s1", severities: []string{"ERR", "WRN"}}
	b := logFilter{id: "s1", severities: []string{"WRN", "ERR"}}
	if a.fingerprint() != b.fingerprint() {
		t.Fatalf("reordered severity set fingerprints differ: %q != %q",
			a.fingerprint(), b.fingerprint())
	}
	// A different id must NOT collide even with the same severity set.
	c := logFilter{id: "s2", severities: []string{"ERR", "WRN"}}
	if a.fingerprint() == c.fingerprint() {
		t.Fatalf("distinct session ids share a logs fingerprint %q", a.fingerprint())
	}
}
