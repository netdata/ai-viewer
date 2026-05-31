package opencode

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

// --- P3-2: a bare filesystem path is opaque (not split on '?') -----------------

// TestBuildReadOnlyDSN_BarePathWithQuestionMarkIsOpaque pins SOW-0005 round-4 P3-2:
// a BARE filesystem path containing '?' must be treated as the LITERAL path — the
// '?' is part of the filename (POSIX allows it), NOT a DSN query delimiter. The
// built DSN therefore percent-escapes the '?' (as %3F) into the file: path and
// carries ONLY the forced read-only query (mode=ro, _txlock, the read-only
// pragmas), with no fragment of the path leaking into the query.
func TestBuildReadOnlyDSN_BarePathWithQuestionMarkIsOpaque(t *testing.T) {
	t.Parallel()
	bare := "/data/oc?weird/opencode.db"
	dsn, err := buildReadOnlyDSN(bare)
	if err != nil {
		t.Fatalf("buildReadOnlyDSN(bare-with-?): %v", err)
	}
	prefix, query := splitQuery(dsn)
	// The path portion must contain the escaped '?', proving it was NOT split off.
	if !strings.Contains(prefix, "%3F") {
		t.Errorf("bare-path DSN prefix %q lost the literal '?' (should be %%3F-escaped, not split)", prefix)
	}
	// The query must be EXACTLY the forced read-only set — none of the path's
	// "weird/opencode.db" tail may appear as a query param.
	params, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if params.Get("mode") != "ro" {
		t.Errorf("mode = %q, want ro", params.Get("mode"))
	}
	if strings.Contains(query, "weird") || strings.Contains(query, "opencode.db") {
		t.Errorf("part of the bare path leaked into the query: %q", query)
	}
}

// TestOpenReadOnly_BarePathWithQuestionMarkOpensLiteralFile is the end-to-end proof:
// an opencode DB created at a path whose DIRECTORY name contains '?' is opened
// read-only via the literal path (round-4 P3-2). Before the fix the '?' split the
// DSN and the OS opened a different (non-existent) path, failing the ping.
func TestOpenReadOnly_BarePathWithQuestionMarkOpensLiteralFile(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	weird := filepath.Join(base, "q?dir")
	if err := os.MkdirAll(weird, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", weird, err)
	}
	// newEmptyDB creates the schema at weird/<name>; its rwDSNFor escapes the path,
	// so the file is created literally inside the '?'-named directory.
	path, rw := newEmptyDB(t, weird, "opencode.db")
	if !strings.Contains(path, "?") {
		t.Fatalf("test path %q unexpectedly has no '?'", path)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, err := openReadOnly(ctxBG(), path)
	if err != nil {
		t.Fatalf("openReadOnly(bare path with '?') failed — the '?' was misparsed as a query: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// A trivial query confirms the connection is live (the literal file opened).
	if _, err := introspectAll(ctxBG(), db); err != nil {
		t.Fatalf("introspect over the '?'-path DB: %v", err)
	}
}

// TestBuildReadOnlyDSN_FileURIFormStillStripsQuery guards the OTHER side of the
// round-4 P3-2 scoping: the URI forms (file:) STILL split + rebuild the query, so a
// caller-supplied query on a file: DSN is parsed and replaced by the read-only set.
func TestBuildReadOnlyDSN_FileURIFormStillStripsQuery(t *testing.T) {
	t.Parallel()
	in := "file:/db/opencode.db?_pragma=query_only(false)&mode=rwc"
	dsn, err := buildReadOnlyDSN(in)
	if err != nil {
		t.Fatalf("buildReadOnlyDSN(file: form): %v", err)
	}
	_, query := splitQuery(dsn)
	params, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if params.Get("mode") != "ro" {
		t.Errorf("file: form mode = %q, want ro (caller mode=rwc must be replaced)", params.Get("mode"))
	}
	got := append([]string(nil), params["_pragma"]...)
	sort.Strings(got)
	want := append([]string(nil), readOnlyPragmas...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("file: form _pragma = %v, want EXACTLY the read-only set %v", got, want)
	}
}

// TestBuildReadOnlyDSN_MalformedQueryRejectedForURIForm pins that a malformed query
// is still rejected for the URI forms (the validation path is unchanged); a bare
// path has no query to validate so it never hits this error.
func TestBuildReadOnlyDSN_MalformedQueryRejectedForURIForm(t *testing.T) {
	t.Parallel()
	if _, err := buildReadOnlyDSN("file:/db/opencode.db?%zz"); err == nil {
		t.Error("malformed query on a file: DSN should be rejected")
	}
	// The same bytes as a BARE path: '?%zz' is part of the filename, so it is
	// escaped, NOT validated as a query — no error.
	if _, err := buildReadOnlyDSN("/db/opencode.db?%zz"); err != nil {
		t.Errorf("bare path containing '?%%zz' must be opaque (no query validation), got %v", err)
	}
}
