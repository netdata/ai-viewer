package store

import (
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestBuildDSN_WriterAppendsRequiredPragmas verifies the writer DSN
// carries every store-required pragma in the encoded query string. We
// parse the encoded query so this test is order-independent against
// operator-supplied parameters; the override-precedence tests below
// pin the ordering separately.
func TestBuildDSN_WriterAppendsRequiredPragmas(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("/tmp/x.db", false)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)

	want := []string{
		"busy_timeout(5000)",
		"foreign_keys(on)",
		"journal_mode(wal)",
		"synchronous(normal)",
	}
	for _, p := range want {
		if !slices.Contains(pragmas, p) {
			t.Errorf("writer DSN missing pragma %q in %v", p, pragmas)
		}
	}
}

// TestBuildDSN_ReaderSkipsWALPragmas asserts the reader DSN does not
// try to set journal_mode or synchronous — those are write-only.
func TestBuildDSN_ReaderSkipsWALPragmas(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("/tmp/x.db", true)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	if !strings.Contains(out, "mode=ro") {
		t.Errorf("reader DSN missing mode=ro: %q", out)
	}
	pragmas := extractPragmas(t, out)
	for _, banned := range []string{"journal_mode", "synchronous"} {
		for _, p := range pragmas {
			if strings.HasPrefix(strings.ToLower(p), banned) {
				t.Errorf("reader DSN must not carry %s pragma; got %q", banned, p)
			}
		}
	}
	if !slices.Contains(pragmas, "query_only(true)") {
		t.Errorf("reader DSN missing query_only(true): %v", pragmas)
	}
}

// TestBuildDSN_MemoryDSNSkipsWAL ensures the in-memory case omits the
// WAL pragmas SQLite would reject anyway.
func TestBuildDSN_MemoryDSNSkipsWAL(t *testing.T) {
	t.Parallel()

	out, err := buildDSN(":memory:", false)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)
	for _, p := range pragmas {
		if strings.HasPrefix(strings.ToLower(p), "journal_mode") {
			t.Errorf(":memory: writer DSN must not request WAL; got %q", p)
		}
	}
	if !slices.Contains(pragmas, "foreign_keys(on)") {
		t.Errorf(":memory: writer DSN missing foreign_keys(on): %v", pragmas)
	}
}

// TestBuildDSN_OperatorCustomPragmaPreserved confirms the caller's own
// _pragma for a non-store-mandated name (e.g. cache_size) survives the
// buildDSN pass intact. Only the four mandatory writer pragmas
// (foreign_keys, busy_timeout, journal_mode, synchronous) and the one
// reader pragma (query_only) are non-overridable; everything else is
// the operator's prerogative.
func TestBuildDSN_OperatorCustomPragmaPreserved(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("file:/tmp/x.db?_pragma=cache_size(-2000)", false)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)
	if !slices.Contains(pragmas, "cache_size(-2000)") {
		t.Errorf("operator cache_size(-2000) lost: %v", pragmas)
	}
}

// TestBuildDSN_OperatorCannotOverrideForeignKeys pins the contract
// that foreign_keys cannot be turned off via an operator-supplied
// _pragma. buildDSN strips operator-supplied values for the
// store-mandated pragma names BEFORE appending the store's value, so
// the final DSN carries `foreign_keys(on)` once and never carries the
// operator's `(off)` at all. This is required because the driver
// sorts the _pragma slice alphabetically — "appending last" would
// not have worked since `(off)` sorts after `(on)`.
func TestBuildDSN_OperatorCannotOverrideForeignKeys(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("file:/tmp/x.db?_pragma=foreign_keys(off)", false)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)
	got := pragmasForName(pragmas, "foreign_keys")
	want := []string{"foreign_keys(on)"}
	if !slices.Equal(got, want) {
		t.Errorf("foreign_keys: want exactly %v, got %v (full DSN %s)", want, got, out)
	}
}

// TestBuildDSN_OperatorCannotOverrideQueryOnly pins the same contract
// for the reader's query_only(true) — defence in depth against
// accidental writes from the server. Operator `query_only(false)` is
// stripped; only the store's `query_only(true)` reaches the driver.
func TestBuildDSN_OperatorCannotOverrideQueryOnly(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("file:/tmp/x.db?_pragma=query_only(false)", true)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)
	got := pragmasForName(pragmas, "query_only")
	want := []string{"query_only(true)"}
	if !slices.Equal(got, want) {
		t.Errorf("query_only: want exactly %v, got %v (full DSN %s)", want, got, out)
	}
}

// TestBuildDSN_OperatorCannotOverrideBusyTimeout asserts the writer's
// busy_timeout cannot be lowered by the operator. Lower values risk
// SQLITE_BUSY at ingest time; 5000ms is the contract.
func TestBuildDSN_OperatorCannotOverrideBusyTimeout(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("file:/tmp/x.db?_pragma=busy_timeout(100)", false)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)
	got := pragmasForName(pragmas, "busy_timeout")
	want := []string{"busy_timeout(5000)"}
	if !slices.Equal(got, want) {
		t.Errorf("busy_timeout: want exactly %v, got %v (full DSN %s)", want, got, out)
	}
}

// TestBuildDSN_OperatorCannotOverrideJournalMode pins the contract
// for journal_mode on the writer path. An operator who sets
// `journal_mode(off)` (or any other value) is stripped; only the
// store's `journal_mode(wal)` reaches the driver. This test
// specifically catches the bug the driver's alphabetical sort would
// have introduced: `(off)` sorts after `(wal)` so "append last" would
// have failed silently.
func TestBuildDSN_OperatorCannotOverrideJournalMode(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("file:/tmp/x.db?_pragma=journal_mode(off)", false)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)
	got := pragmasForName(pragmas, "journal_mode")
	want := []string{"journal_mode(wal)"}
	if !slices.Equal(got, want) {
		t.Errorf("journal_mode: want exactly %v, got %v (full DSN %s)", want, got, out)
	}
}

// TestBuildDSN_OperatorCannotOverrideSynchronous pins the contract
// for synchronous on the writer path. `synchronous(off)` sorts AFTER
// `synchronous(normal)` alphabetically ('of' > 'no'), which is the
// exact case that broke the previous "append last" strategy. Verify
// the operator value is stripped and only the store's value remains.
func TestBuildDSN_OperatorCannotOverrideSynchronous(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("file:/tmp/x.db?_pragma=synchronous(off)", false)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)
	got := pragmasForName(pragmas, "synchronous")
	want := []string{"synchronous(normal)"}
	if !slices.Equal(got, want) {
		t.Errorf("synchronous: want exactly %v, got %v (full DSN %s)", want, got, out)
	}
}

// TestBuildDSN_OperatorCannotOverrideModeRO asserts the reader's
// mode=ro is enforced even when the operator tried to request
// something else.
func TestBuildDSN_OperatorCannotOverrideModeRO(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("file:/tmp/x.db?mode=rwc", true)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	_, qs, _ := strings.Cut(out, "?")
	q, parseErr := url.ParseQuery(qs)
	if parseErr != nil {
		t.Fatalf("ParseQuery %q: %v", qs, parseErr)
	}
	if got := q.Get("mode"); got != "ro" {
		t.Errorf("reader mode: want ro, got %q (full DSN %s)", got, out)
	}
}

// TestBuildDSN_MalformedQueryReturnsError exercises the fail-fast
// behaviour: a deliberately malformed query (invalid percent-encoding)
// must surface as an error rather than being silently replaced with an
// empty value set, which could turn a bad DSN into one with unintended
// pragmas.
func TestBuildDSN_MalformedQueryReturnsError(t *testing.T) {
	t.Parallel()

	cases := []string{
		"file:/tmp/x.db?broken=%zz",
		"file:/tmp/x.db?%",
	}
	for _, dsn := range cases {
		if _, err := buildDSN(dsn, false); err == nil {
			t.Errorf("buildDSN(%q): want error, got nil", dsn)
		}
	}
}

// TestPathToFileURI_PassesThroughFileURI confirms that DSNs already in
// "file:" URI form are returned unchanged so callers do not double-wrap.
func TestPathToFileURI_PassesThroughFileURI(t *testing.T) {
	t.Parallel()

	cases := []string{
		"file:/tmp/x.db",
		"file:/tmp/x.db?cache=shared",
		"file::memory:",
		"file::memory:?cache=shared",
	}
	for _, in := range cases {
		got, err := pathToFileURI(in)
		if err != nil {
			t.Errorf("pathToFileURI(%q): %v", in, err)
			continue
		}
		if got != in {
			t.Errorf("pathToFileURI(%q): want passthrough, got %q", in, got)
		}
	}
}

// TestPathToFileURI_PassesThroughMemory keeps the in-memory bare form
// untouched so OpenWriter's pool-pinning logic (which inspects the
// original DSN, not the rewritten one) continues to see :memory:.
func TestPathToFileURI_PassesThroughMemory(t *testing.T) {
	t.Parallel()

	got, err := pathToFileURI(":memory:")
	if err != nil {
		t.Fatalf("pathToFileURI(:memory:): %v", err)
	}
	if got != ":memory:" {
		t.Errorf("pathToFileURI(:memory:): want passthrough, got %q", got)
	}
}

// TestPathToFileURI_AbsolutizesRelative verifies that a relative path
// is resolved against the current working directory and rewritten in
// "file:/abs/path" form.
func TestPathToFileURI_AbsolutizesRelative(t *testing.T) {
	t.Parallel()

	got, err := pathToFileURI("relative.db")
	if err != nil {
		t.Fatalf("pathToFileURI: %v", err)
	}
	if !strings.HasPrefix(got, "file:/") {
		t.Errorf("relative DSN should produce file:/ URI; got %q", got)
	}
	if !strings.HasSuffix(got, "/relative.db") {
		t.Errorf("relative DSN should retain basename; got %q", got)
	}
	// And it must be absolute — i.e. roundtrips through filepath.IsAbs.
	pathPart := strings.TrimPrefix(got, "file:")
	if !filepath.IsAbs(pathPart) {
		t.Errorf("file: URI body should be absolute, got %q", pathPart)
	}
}

// TestPathToFileURI_AbsolutePathRoundTrips checks that POSIX absolute
// paths produce the expected "file:/abs/path" form.
func TestPathToFileURI_AbsolutePathRoundTrips(t *testing.T) {
	t.Parallel()

	got, err := pathToFileURI("/tmp/x.db")
	if err != nil {
		t.Fatalf("pathToFileURI: %v", err)
	}
	if got != "file:/tmp/x.db" {
		t.Errorf("pathToFileURI(/tmp/x.db): want %q, got %q", "file:/tmp/x.db", got)
	}
}

// TestPathToFileURI_PreservesExistingQuery keeps any operator-supplied
// query string attached after the rewrite, so values like
// `_pragma=cache_size(-2000)` survive into the wrapped URI.
func TestPathToFileURI_PreservesExistingQuery(t *testing.T) {
	t.Parallel()

	got, err := pathToFileURI("/tmp/x.db?_pragma=cache_size(-2000)")
	if err != nil {
		t.Fatalf("pathToFileURI: %v", err)
	}
	if got != "file:/tmp/x.db?_pragma=cache_size(-2000)" {
		t.Errorf("pathToFileURI: query lost; got %q", got)
	}
}

// TestPathToFileURI_RejectsEmpty pins the contract that an empty DSN
// is an error, not a silent wrap to "file:/cwd".
func TestPathToFileURI_RejectsEmpty(t *testing.T) {
	t.Parallel()

	if _, err := pathToFileURI(""); err == nil {
		t.Fatal("pathToFileURI(\"\"): want error, got nil")
	}
}

// TestPragmaName_StripsBareSchemaPrefix verifies the schema-qualified
// PRAGMA syntax SQLite accepts (`main.foreign_keys(off)`,
// `temp.busy_timeout=100`, etc.) is normalized to the unqualified
// pragma name. Without this strip, an operator could bypass the
// mandatory-pragma override by qualifying the name.
func TestPragmaName_StripsBareSchemaPrefix(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"main.foreign_keys(off)":   "foreign_keys",
		"temp.busy_timeout=100":    "busy_timeout",
		"custom_schema.query_only": "query_only",
		"MAIN.Foreign_Keys(off)":   "foreign_keys",
	}
	for in, want := range cases {
		if got := pragmaName(in); got != want {
			t.Errorf("pragmaName(%q): want %q, got %q", in, want, got)
		}
	}
}

// TestPragmaName_StripsQuotedSchemaPrefix covers the quoted-identifier
// forms SQLite accepts for schema names: `"main"`, `[main]`, and
// “ `main` “.
func TestPragmaName_StripsQuotedSchemaPrefix(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`"main".foreign_keys(off)`: "foreign_keys",
		`[main].query_only(false)`: "query_only",
		"`main`.foreign_keys(off)": "foreign_keys",
		`"temp".busy_timeout=100`:  "busy_timeout",
	}
	for in, want := range cases {
		if got := pragmaName(in); got != want {
			t.Errorf("pragmaName(%q): want %q, got %q", in, want, got)
		}
	}
}

// TestPragmaName_NoSchemaUnchanged confirms the strip is a no-op for
// values that lack any schema qualifier — pragmaName must not eat the
// real identifier.
func TestPragmaName_NoSchemaUnchanged(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"foreign_keys(off)":     "foreign_keys",
		"busy_timeout=100":      "busy_timeout",
		"synchronous (normal)":  "synchronous",
		"  cache_size(-2000)  ": "cache_size",
		"journal_mode":          "journal_mode",
		"   FOREIGN_KEYS  (on)": "foreign_keys",
	}
	for in, want := range cases {
		if got := pragmaName(in); got != want {
			t.Errorf("pragmaName(%q): want %q, got %q", in, want, got)
		}
	}
}

// TestPragmaName_NoValueFallthrough exercises the no-delimiter branch
// of pragmaName (the previously suppressed coverage row). A bare name
// like `foreign_keys` with no '(' or '=' is a legal _pragma value when
// the operator wants to query rather than set; the helper must return
// it unchanged (modulo lowercase + trim).
func TestPragmaName_NoValueFallthrough(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"foreign_keys":      "foreign_keys",
		"  query_only  ":    "query_only",
		"BUSY_TIMEOUT":      "busy_timeout",
		"main.foreign_keys": "foreign_keys",
		`"main".query_only`: "query_only",
	}
	for in, want := range cases {
		if got := pragmaName(in); got != want {
			t.Errorf("pragmaName(%q): want %q, got %q", in, want, got)
		}
	}
}

// TestStripSchemaPrefix_EdgeCases covers the defensive branches in
// stripSchemaPrefix that the higher-level pragmaName tests do not
// reach: empty string, an unterminated quoted identifier, and a
// quoted identifier with no following `.<name>` segment. The helper
// must return the input unchanged in each of these cases — silently
// eating characters would break legitimate _pragma values.
func TestStripSchemaPrefix_EdgeCases(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                   "",              // empty input passthrough
		`"unterminated`:      `"unterminated`, // no closing quote
		`[unterminated`:      `[unterminated`,
		"`unterminated":      "`unterminated",
		`"main"`:             `"main"`,             // quoted but no `.<name>`
		`[main]`:             `[main]`,             // bracketed but no `.<name>`
		"`main`":             "`main`",             // backticked but no `.<name>`
		`"main"foreign_keys`: `"main"foreign_keys`, // quoted, no dot after closer
	}
	for in, want := range cases {
		if got := stripSchemaPrefix(in); got != want {
			t.Errorf("stripSchemaPrefix(%q): want %q, got %q", in, want, got)
		}
	}
}

// TestBuildDSN_OperatorCannotOverrideForeignKeys_Qualified is the
// schema-qualified counterpart to
// TestBuildDSN_OperatorCannotOverrideForeignKeys. An operator who
// writes `_pragma=main.foreign_keys(off)` must not be able to disable
// foreign keys; the strip-list matches by unqualified name so the
// qualified value is removed before the driver sees it.
func TestBuildDSN_OperatorCannotOverrideForeignKeys_Qualified(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("file:/tmp/x.db?_pragma=main.foreign_keys(off)", false)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)
	got := pragmasForName(pragmas, "foreign_keys")
	want := []string{"foreign_keys(on)"}
	if !slices.Equal(got, want) {
		t.Errorf("foreign_keys (qualified): want exactly %v, got %v (full DSN %s)", want, got, out)
	}
	if strings.Contains(out, "main.foreign_keys") {
		t.Errorf("buildDSN leaked qualified operator pragma into DSN: %s", out)
	}
}

// TestBuildDSN_OperatorCannotOverrideQueryOnly_Qualified is the
// schema-qualified reader counterpart. `_pragma=temp.query_only(false)`
// must be stripped so only the store's `query_only(true)` survives.
func TestBuildDSN_OperatorCannotOverrideQueryOnly_Qualified(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("file:/tmp/x.db?_pragma=temp.query_only(false)", true)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)
	got := pragmasForName(pragmas, "query_only")
	want := []string{"query_only(true)"}
	if !slices.Equal(got, want) {
		t.Errorf("query_only (qualified): want exactly %v, got %v (full DSN %s)", want, got, out)
	}
	if strings.Contains(out, "temp.query_only") {
		t.Errorf("buildDSN leaked qualified operator pragma into DSN: %s", out)
	}
}

// TestBuildDSN_OperatorCannotOverrideBusyTimeout_Qualified covers the
// writer's busy_timeout via the qualified syntax.
func TestBuildDSN_OperatorCannotOverrideBusyTimeout_Qualified(t *testing.T) {
	t.Parallel()

	out, err := buildDSN("file:/tmp/x.db?_pragma=main.busy_timeout(100)", false)
	if err != nil {
		t.Fatalf("buildDSN: %v", err)
	}
	pragmas := extractPragmas(t, out)
	got := pragmasForName(pragmas, "busy_timeout")
	want := []string{"busy_timeout(5000)"}
	if !slices.Equal(got, want) {
		t.Errorf("busy_timeout (qualified): want exactly %v, got %v (full DSN %s)", want, got, out)
	}
	if strings.Contains(out, "main.busy_timeout") {
		t.Errorf("buildDSN leaked qualified operator pragma into DSN: %s", out)
	}
}

// extractPragmas pulls every _pragma value out of a buildDSN output.
func extractPragmas(t *testing.T, dsn string) []string {
	t.Helper()
	_, qs, ok := strings.Cut(dsn, "?")
	if !ok {
		return nil
	}
	q, err := url.ParseQuery(qs)
	if err != nil {
		t.Fatalf("ParseQuery %q: %v", qs, err)
	}
	return q["_pragma"]
}

// pragmasForName returns every _pragma value whose pragma identifier
// (the substring before '(' or '=') matches name, case-insensitive.
// Used by the override-precedence tests to assert that the final DSN
// carries exactly the store's value for a mandatory pragma — no
// operator-supplied entries, no duplicates.
func pragmasForName(pragmas []string, name string) []string {
	want := strings.ToLower(name)
	out := make([]string, 0)
	for _, p := range pragmas {
		if strings.ToLower(pragmaName(p)) == want {
			out = append(out, p)
		}
	}
	return out
}
