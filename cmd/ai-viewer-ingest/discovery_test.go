// Tests for the codex auto-discovery probe and its observability counters
// (SOW-0004 acceptance #8), plus the shared discovery-helper counters. Split out
// of sources_test.go so each test file stays under the 400-line budget, mirroring
// the discovery.go / sources.go production split. They pin:
//
//   - the probe registers a source at $CODEX_HOME/sessions (default
//     ~/.codex/sessions) when the directory exists, with location = the
//     walked sessions dir;
//   - $CODEX_HOME overrides the default location;
//   - an absent sessions dir registers no codex source;
//   - countRolloutFiles / countLegacyJSON report the modern (sharded .jsonl)
//     and legacy (root .json) volumes SEPARATELY;
//   - the discovery log line carries both counts as distinct keys.
package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/adapters"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// plantCodexLayout writes a sessions tree under root with `modern` sharded
// rollout-*.jsonl files (in a YYYY/MM/DD shard), `legacy` root rollout-*.json
// files, and a couple of decoys that must NOT be counted (an archived_sessions
// shard, a non-rollout file, a .jsonl outside the rollout prefix).
func plantCodexLayout(t *testing.T, root string, modern, legacy int) {
	t.Helper()
	shard := filepath.Join(root, "2025", "11", "20")
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatalf("mkdir shard: %v", err)
	}
	for i := 0; i < modern; i++ {
		name := filepath.Join(shard, "rollout-2025-11-20T10-00-0"+itoa(i)+"-uuid.jsonl")
		if err := os.WriteFile(name, []byte(`{"type":"session_meta"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write modern rollout: %v", err)
		}
	}
	for i := 0; i < legacy; i++ {
		name := filepath.Join(root, "rollout-2025-06-0"+itoa(i)+"-uuid.json")
		if err := os.WriteFile(name, []byte(`{}`), 0o644); err != nil {
			t.Fatalf("write legacy rollout: %v", err)
		}
	}
	// Decoys: an archived shard rollout (pruned), a non-rollout .jsonl, a
	// non-rollout file at the root, AND a rollout-*.jsonl at the WRONG depth
	// (directly under the sessions root, not in a YYYY/MM/DD shard — F8). None of
	// these must be counted.
	arch := filepath.Join(root, "archived_sessions", "2025", "11", "20")
	if err := os.MkdirAll(arch, 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(arch, "rollout-archived-uuid.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write archived: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shard, "not-a-rollout.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write decoy jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "history.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write decoy root file: %v", err)
	}
	// A rollout-*.jsonl placed directly under the sessions root (wrong shard
	// depth) must NOT be counted as a modern rollout (F8).
	if err := os.WriteFile(filepath.Join(root, "rollout-2025-11-20T10-00-09-strayroot.jsonl"), []byte(`{"type":"session_meta"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write stray-root rollout: %v", err)
	}
}

// itoa is a tiny single-digit int→string helper so plantCodexLayout stays free
// of strconv for the small counts the tests use.
func itoa(i int) string { return string(rune('0' + i)) }

// TestAutoDiscover_CodexProbe verifies acceptance #8: a tmpdir
// ~/.codex/sessions tree with modern sharded rollouts is auto-discovered as a
// codex source whose location is the sessions root, and the registered factory
// can construct it.
func TestAutoDiscover_CodexProbe(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide HOME / CODEX_HOME.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CODEX_HOME", "")
	sessions := filepath.Join(tmp, ".codex", "sessions")
	plantCodexLayout(t, sessions, 2, 3)

	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	var cdx *configuredSource
	for i := range got {
		if got[i].format == "codex" {
			cdx = &got[i]
		}
	}
	if cdx == nil {
		t.Fatalf("codex source not auto-discovered; got %+v", got)
	}
	if cdx.location != sessions {
		t.Fatalf("codex location = %q, want %q", cdx.location, sessions)
	}
	// The discovered source must be constructable via the registry, proving the
	// adapter's init() ran (acceptance #1).
	factory, ok := adapters.Get("codex")
	if !ok {
		t.Fatal("codex factory not registered")
	}
	if _, err := factory(cdx.location, canonical.AdapterOptions{Logger: silentLogger()}); err != nil {
		t.Fatalf("codex factory(%q): %v", cdx.location, err)
	}
}

// TestAutoDiscover_CodexHomeOverride verifies the probe honors $CODEX_HOME
// (SOW-0004 C#3): the sessions root is "$CODEX_HOME/sessions", not ~/.codex.
func TestAutoDiscover_CodexHomeOverride(t *testing.T) {
	// Not parallel: mutates process-wide env.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp) // no ~/.codex here
	codexHome := filepath.Join(tmp, "custom-codex")
	t.Setenv("CODEX_HOME", codexHome)
	sessions := filepath.Join(codexHome, "sessions")
	plantCodexLayout(t, sessions, 1, 0)

	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	var loc string
	for _, s := range got {
		if s.format == "codex" {
			loc = s.location
		}
	}
	if loc != sessions {
		t.Fatalf("codex location = %q, want %q (CODEX_HOME honored)", loc, sessions)
	}
}

// TestAutoDiscover_NoCodexWhenAbsent verifies a workstation without
// ~/.codex/sessions does not register a codex source.
func TestAutoDiscover_NoCodexWhenAbsent(t *testing.T) {
	// Not parallel: mutates process-wide env.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CODEX_HOME", "")

	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	for _, s := range got {
		if s.format == "codex" {
			t.Fatalf("codex registered with no sessions dir present: %+v", got)
		}
	}
}

// TestAutoDiscover_CodexProbeLogsBothCountsSeparately verifies the probe's
// discovery log line carries the modern and legacy volumes as DISTINCT keys
// (acceptance #8: "/api/sources reports both counts separately" — the structured
// log is the operator-facing surface at discovery time).
func TestAutoDiscover_CodexProbeLogsBothCountsSeparately(t *testing.T) {
	// Not parallel: mutates process-wide env.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CODEX_HOME", "")
	sessions := filepath.Join(tmp, ".codex", "sessions")
	plantCodexLayout(t, sessions, 2, 3)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, err := resolveSources(nil, logger); err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("modern_rollouts=2")) {
		t.Errorf("discovery log missing modern_rollouts=2; got:\n%s", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("legacy_json=3")) {
		t.Errorf("discovery log missing legacy_json=3; got:\n%s", out)
	}
}

// TestCountRolloutFiles verifies the modern-rollout counter mirrors discovery.go's
// match: rollout-*.jsonl under YYYY/MM/DD shards, archived_sessions pruned,
// non-rollout .jsonl, root non-rollout files, AND a rollout-*.jsonl at the wrong
// shard depth (directly under the root) all ignored (F8).
func TestCountRolloutFiles(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	plantCodexLayout(t, tmp, 4, 2)
	if n := countRolloutFiles(tmp); n != 4 {
		t.Fatalf("countRolloutFiles = %d, want 4 (archived + decoys + wrong-depth stray excluded)", n)
	}
	if n := countRolloutFiles(filepath.Join(tmp, "missing")); n != 0 {
		t.Fatalf("countRolloutFiles(missing) = %d, want 0", n)
	}
}

// TestCountLegacyJSON verifies the legacy counter mirrors discovery.go's match:
// rollout-*.json directly under the root only (not in shards), non-rollout root
// files ignored.
func TestCountLegacyJSON(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	plantCodexLayout(t, tmp, 4, 2)
	if n := countLegacyJSON(tmp); n != 2 {
		t.Fatalf("countLegacyJSON = %d, want 2", n)
	}
	if n := countLegacyJSON(filepath.Join(tmp, "missing")); n != 0 {
		t.Fatalf("countLegacyJSON(missing) = %d, want 0", n)
	}
}
