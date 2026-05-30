package opencode

import (
	"net/url"
	"reflect"
	"sort"
	"testing"
)

// This file pins the read-only DSN ALLOWLIST policy (SOW-0005 P1.2): every
// caller-supplied _pragma is dropped and the query is rebuilt from the fixed
// read-only set + mode=ro + _txlock=deferred, so no path-string vector can reach
// a write-path pragma or an exclusive (write-lock) BEGIN. Split out of
// conn_test.go to keep each file ≤400 lines.

// TestBuildReadOnlyDSN_AllowlistDropsAllCallerPragmas asserts the ALLOWLIST
// policy: EVERY caller-supplied _pragma is dropped — both the ones that collide
// with the read-only set (query_only, busy_timeout, including a schema-qualified
// form) AND any other (cache_size). The built DSN's _pragma set is EXACTLY the
// read-only set, nothing else. This is the inversion of the old denylist that let
// non-colliding pragmas pass through.
func TestBuildReadOnlyDSN_AllowlistDropsAllCallerPragmas(t *testing.T) {
	t.Parallel()
	in := "file:/db/opencode.db?_pragma=query_only(false)&_pragma=main.busy_timeout(1)&_pragma=cache_size(-2000)"
	dsn, err := buildReadOnlyDSN(in)
	if err != nil {
		t.Fatalf("buildReadOnlyDSN: %v", err)
	}
	_, query := splitQuery(dsn)
	params, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	got := append([]string(nil), params["_pragma"]...)
	sort.Strings(got)
	want := append([]string(nil), readOnlyPragmas...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("_pragma set = %v, want EXACTLY the read-only set %v (allowlist drops all caller pragmas)", got, want)
	}
}

// TestBuildReadOnlyDSN_MaliciousDSNNeutralised is the P1.2 read-safety proof: a
// path string crafted with write-path pragmas (wal_checkpoint(TRUNCATE),
// foreign_keys(on)) and _txlock=exclusive yields a DSN that carries ONLY the
// read-only pragmas, mode=ro, and _txlock=deferred — none of the injected write
// vectors survive. The old denylist would have let the non-colliding pragmas and
// the exclusive txlock through.
func TestBuildReadOnlyDSN_MaliciousDSNNeutralised(t *testing.T) {
	t.Parallel()
	in := "file:/x/opencode.db?_pragma=wal_checkpoint(TRUNCATE)&_txlock=exclusive&_pragma=foreign_keys(on)"
	dsn, err := buildReadOnlyDSN(in)
	if err != nil {
		t.Fatalf("buildReadOnlyDSN: %v", err)
	}
	_, query := splitQuery(dsn)
	params, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}

	// _pragma must be EXACTLY the read-only set — no wal_checkpoint, no foreign_keys.
	got := append([]string(nil), params["_pragma"]...)
	sort.Strings(got)
	want := append([]string(nil), readOnlyPragmas...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("_pragma = %v, want EXACTLY %v (no injected write-path pragma may survive)", got, want)
	}
	for _, p := range params["_pragma"] {
		switch pragmaName(p) {
		case "wal_checkpoint", "foreign_keys", "optimize":
			t.Errorf("injected write-path pragma survived: %q", p)
		}
	}
	if got := params.Get("_txlock"); got != txlockDeferred {
		t.Errorf("_txlock = %q, want %q (injected exclusive must be replaced)", got, txlockDeferred)
	}
	if got := params.Get("mode"); got != "ro" {
		t.Errorf("mode = %q, want ro", got)
	}
}
