package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file is the GOLDEN-TEST HARNESS for the opencode adapter (SOW-0005 chunk
// E). It mirrors codex/golden_test.go's auto-discovering shape — the
// -update-golden flag, the testdata/<adapter>/<scenario>/ loop, SourceProgress
// filtering, the goldenEvent{kind,payload} JSONL wire shape, and the <ROOT>
// placeholder substitution — adapting only the FIXTURE LOADING. Codex walks a
// $CODEX_HOME directory of rollout JSONL files; opencode reads a single SQLite
// database, which git cannot carry as a binary blob. So each scenario commits a
// human-reviewable fixture.sql (CREATE TABLE + INSERTs) and the harness builds a
// throwaway SQLite DB from it at run time via a SEPARATE read-write connection;
// the ADAPTER under test still opens that path strictly read-only via New →
// openReadOnly (the read-only contract is unchanged).
//
// The only embedding of an absolute path in a non-SourceProgress event is the
// SourceID ("opencode:<dbPath>"); it is rewritten to "opencode:<ROOT>" so the
// golden is portable and carries no operator filesystem path. The
// opencode-sqlite://?part_id=&field= PayloadRef URIs are DB-relative (no path,
// no basename — see payloads.go) and therefore already portable; they need no
// substitution.

var updateGolden = flag.Bool("update-golden", false, "rewrite golden expected.jsonl files for the opencode adapter")

// goldenEvent is the wire shape written into expected.jsonl: the kind
// discriminator plus the concrete payload. Resilient to field additions on the
// canonical types — updating with -update-golden picks up new fields. Mirrors
// codex/golden_test.go.
type goldenEvent struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// rootPlaceholder replaces the test's absolute database path inside the
// "opencode:<path>" SourceID so golden files are portable across workstations
// and CI AND carry no operator filesystem path. Mirrors codex's <ROOT>.
const rootPlaceholder = "<ROOT>"

// TestGolden runs every scenario directory under testdata/opencode/ that
// contains a fixture.sql and asserts Scan produces the canonical events recorded
// in expected.jsonl. Run with -update-golden to refresh. Mirrors codex's
// auto-discovering harness; the opencode INPUT is a fixture.sql the harness
// loads into a fresh temp SQLite DB (see buildFixtureDB).
//
// CRITICAL (chunk brief): a golden generated with -update-golden is NOT
// self-justifying — it pins whatever the code emitted, bugs included. Every
// expected.jsonl in this suite was hand-verified line by line against the spec
// and the fixture's intent before being trusted; the per-scenario invariants are
// additionally asserted in scenario-specific tests (golden_invariants_test.go)
// so a future -update-golden cannot silently launder a regression past review.
func TestGolden(t *testing.T) {
	t.Parallel()

	base := filepath.Join("..", "..", "..", "testdata", "opencode")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("readdir %s: %v", base, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runGoldenScenario(t, filepath.Join(base, name))
		})
	}
}

// runGoldenScenario builds the scenario's SQLite DB from fixture.sql, scans it
// through the public adapter, filters out SourceProgress (a checkpoint, not
// content), encodes the remaining events with the <ROOT> placeholder, and
// compares to (or rewrites, under -update-golden) expected.jsonl.
func runGoldenScenario(t *testing.T, scenarioDir string) {
	t.Helper()
	fixturePath := filepath.Join(scenarioDir, "fixture.sql")
	if _, err := os.Stat(fixturePath); err != nil {
		t.Skipf("fixture.sql missing: %v", err)
		return
	}

	dbPath := buildFixtureDB(t, fixturePath)
	absDB, err := filepath.Abs(dbPath)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	absDB = filepath.Clean(absDB)

	events := scanScenario(t, absDB)

	filtered := make([]canonical.Event, 0, len(events))
	for _, ev := range events {
		if _, ok := ev.(canonical.SourceProgressEvent); ok {
			continue
		}
		filtered = append(filtered, ev)
	}

	encoded, err := encodeEvents(filtered, absDB)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	goldenPath := filepath.Join(scenarioDir, "expected.jsonl")
	if *updateGolden {
		if err := os.WriteFile(goldenPath, encoded, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated golden: %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath) // #nosec G304 -- goldenPath is a fixed testdata path under the scenario dir, not user input
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update-golden to create)", goldenPath, err)
	}
	if string(want) != string(encoded) {
		t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
			goldenPath, string(want), string(encoded))
	}
}

// scanScenario opens absDB through the public adapter (New + Scan), drains the
// emitted events, and returns them. The adapter opens the database read-only via
// its own openReadOnly helper; the harness never hands it a writable handle.
func scanScenario(t *testing.T, absDB string) []canonical.Event {
	t.Helper()
	a, err := New(absDB, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 8192)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return drainAll(out)
}

// buildFixtureDB creates a fresh temp SQLite database in t.TempDir() and applies
// the scenario's fixture.sql (the human-reviewable INPUT analogue). The DB is
// built through a SEPARATE read-write database/sql connection (production NEVER
// opens opencode.db read-write; this is the test harness building the fixture);
// the connection is closed before the adapter reopens the path read-only so the
// WAL is flushed and the adapter sees a stable file. Returns the DB path.
//
// fixture.sql is executed statement-by-statement (split on ";\n") so the
// modernc.org/sqlite driver — which does not run multi-statement strings through
// database/sql's Exec — applies every CREATE/INSERT. Synthetic content only; no
// operator data ever reaches testdata/.
func buildFixtureDB(t *testing.T, fixturePath string) string {
	t.Helper()
	sqlBytes, err := os.ReadFile(fixturePath) // #nosec G304 -- fixturePath is a fixed testdata path, not user input
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}

	path := filepath.Join(t.TempDir(), "opencode.db")
	rw, err := sql.Open(driverName, rwDSNFor(path))
	if err != nil {
		t.Fatalf("open rw fixture db: %v", err)
	}
	for _, stmt := range splitSQLStatements(string(sqlBytes)) {
		if _, err := rw.Exec(stmt); err != nil {
			_ = rw.Close()
			t.Fatalf("apply fixture stmt: %v\nstmt: %s", err, stmt)
		}
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw fixture db: %v", err)
	}
	return path
}

// splitSQLStatements splits a fixture.sql blob into individual executable
// statements. opencode fixtures are simple (CREATE TABLE + INSERT, no triggers,
// no procedural blocks, no embedded ';' inside string literals beyond what the
// fixtures avoid by construction), so a split on ";" terminating a line is
// sufficient and keeps the harness dependency-free. Blank/comment-only fragments
// (-- lines) are dropped. Each returned statement is trimmed and non-empty.
func splitSQLStatements(blob string) []string {
	var out []string
	for _, raw := range strings.Split(blob, ";\n") {
		stmt := stripSQLComments(raw)
		if stmt != "" {
			out = append(out, stmt)
		}
	}
	// A trailing statement not followed by a newline (no ";\n") is caught by the
	// final fragment; strip a lone trailing ';' it may carry.
	for i, s := range out {
		out[i] = strings.TrimSuffix(strings.TrimSpace(s), ";")
	}
	cleaned := out[:0]
	for _, s := range out {
		if strings.TrimSpace(s) != "" {
			cleaned = append(cleaned, s)
		}
	}
	return cleaned
}

// stripSQLComments removes whole-line "--" comments and trims surrounding
// whitespace, returning "" for a fragment that is only comments/whitespace. It
// does not attempt to strip inline trailing comments (the fixtures keep "--"
// comments on their own lines), keeping the splitter simple and predictable.
func stripSQLComments(frag string) string {
	var b strings.Builder
	for _, line := range strings.Split(frag, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// encodeEvents serialises events one goldenEvent per line, with the absolute
// test-machine database path inside the "opencode:<path>" SourceID replaced by
// rootPlaceholder so golden files are portable AND carry no operator filesystem
// path (sensitive-data hygiene). Mirrors codex's encodeEvents; opencode embeds
// the path in exactly ONE field (SourceID) — the PayloadRef LocationURI is the
// path-free opencode-sqlite://?part_id=&field= form, so no second rewrite is
// needed.
func encodeEvents(events []canonical.Event, absDB string) ([]byte, error) {
	var b strings.Builder
	for _, ev := range events {
		payload, err := json.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("marshal %T: %w", ev, err)
		}
		s := strings.ReplaceAll(string(payload), sourceIDPrefix+absDB, sourceIDPrefix+rootPlaceholder)
		ge := goldenEvent{Kind: string(ev.EventKind()), Payload: json.RawMessage(s)}
		enc, err := json.Marshal(ge)
		if err != nil {
			return nil, err
		}
		b.Write(enc)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}
