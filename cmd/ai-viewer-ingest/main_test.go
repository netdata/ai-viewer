// Tests for the ai-viewer-ingest CLI surface. End-to-end ingest path
// is covered by the integration smoke in scripts/; this file pins:
//
//   - parseFlags exit-code contract (--version → 0, --help → 0,
//     bad/empty --source → 2).
//   - repeatableFlag semantics.
//   - resolveSources: explicit --source replaces auto-discovery; dedup
//     works on the explicit path.
//   - autoDiscoverSources: probes the deployment.md paths under a
//     temp $HOME and returns the right format set.
//   - parseSourceFlag: positive + negative cases.
//   - loadSourceCursor: nil-lookup short-circuit, empty stored,
//     lookup-error fallback, and round-trip via the real aiagent_v3
//     adapter — pinning the cursor-resume path required by ingester.md
//     §17.
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters"
	// Side-effect imports to populate the registry for the cursor-load and
	// probe tests.
	_ "github.com/netdata/ai-viewer/internal/adapters/aiagent_v3"
	_ "github.com/netdata/ai-viewer/internal/adapters/claude_code"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// captureStderr returns a *os.File whose writes we collect for the
// test. Used so parseFlags' error path is observable.
func captureStderr(t *testing.T) (*os.File, func() string) {
	t.Helper()
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "stderr-*.log")
	if err != nil {
		t.Fatalf("create stderr capture: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, func() string {
		_ = f.Sync()
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatalf("read stderr capture: %v", err)
		}
		return string(b)
	}
}

// silentLogger keeps the test output uncluttered.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseFlags_VersionExitsZero(t *testing.T) {
	t.Parallel()
	stderr, read := captureStderr(t)
	_, code, ok := parseFlags([]string{"--version"}, stderr)
	if ok {
		t.Fatal("parseFlags: want early-exit on --version, got ok=true")
	}
	if code != 0 {
		t.Fatalf("parseFlags exit code = %d, want 0; stderr=%q", code, read())
	}
	if !strings.Contains(read(), "ai-viewer-ingest") {
		t.Fatalf("version output missing binary name: %q", read())
	}
}

func TestParseFlags_HelpExitsZero(t *testing.T) {
	t.Parallel()
	stderr, _ := captureStderr(t)
	_, code, ok := parseFlags([]string{"-h"}, stderr)
	if ok {
		t.Fatal("parseFlags: want early-exit on -h, got ok=true")
	}
	if code != 0 {
		t.Fatalf("parseFlags exit code = %d, want 0", code)
	}
}

func TestParseFlags_RepeatableSource(t *testing.T) {
	t.Parallel()
	stderr, _ := captureStderr(t)
	cfg, code, ok := parseFlags(
		[]string{"--source", "aiagent_v3:/tmp/a", "--source", "aiagent_v2:/tmp/b"},
		stderr,
	)
	if !ok || code != 0 {
		t.Fatalf("parseFlags(2 --source) ok=%v code=%d", ok, code)
	}
	if len(cfg.sources) != 2 {
		t.Fatalf("sources len = %d, want 2", len(cfg.sources))
	}
	if cfg.sources[0] != "aiagent_v3:/tmp/a" || cfg.sources[1] != "aiagent_v2:/tmp/b" {
		t.Fatalf("sources = %v, want [aiagent_v3:/tmp/a, aiagent_v2:/tmp/b]", cfg.sources)
	}
}

func TestParseFlags_EmptySourceRejected(t *testing.T) {
	t.Parallel()
	stderr, _ := captureStderr(t)
	_, code, ok := parseFlags([]string{"--source", ""}, stderr)
	if ok {
		t.Fatal("parseFlags accepted empty --source; want exit 2")
	}
	if code != 2 {
		t.Fatalf("parseFlags exit code = %d, want 2", code)
	}
}

func TestParseSourceFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw      string
		format   string
		location string
		wantErr  bool
	}{
		{"aiagent_v3:/home/u/.ai-agent/sessions", "aiagent_v3", "/home/u/.ai-agent/sessions", false},
		{"aiagent_v2:relative/path", "aiagent_v2", "relative/path", false},
		// Multi-colon: only the FIRST ':' is the separator.
		{"format:host:port", "format", "host:port", false},
		// Errors:
		{"noformat", "", "", true},
		{":noformat", "", "", true},
		{"format:", "", "", true},
		{"", "", "", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			format, location, err := parseSourceFlag(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSourceFlag(%q) = nil, want error", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSourceFlag(%q) = %v, want nil", tc.raw, err)
			}
			if format != tc.format || location != tc.location {
				t.Fatalf("parseSourceFlag(%q) = (%q, %q), want (%q, %q)",
					tc.raw, format, location, tc.format, tc.location)
			}
		})
	}
}

func TestResolveSources_ExplicitReplacesAutoDiscovery(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide HOME.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CLAUDE_CONFIG_DIR", "") // hermetic: ignore any real claude config
	if err := os.MkdirAll(filepath.Join(tmp, ".ai-agent", "sessions", "session"), 0o755); err != nil {
		t.Fatalf("plant home: %v", err)
	}

	got, err := resolveSources(
		[]string{"aiagent_v3:/custom/path-a"},
		silentLogger(),
	)
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (explicit replaces implicit)", len(got))
	}
	if got[0].id != "aiagent_v3:/custom/path-a" {
		t.Fatalf("got[0].id = %q, want aiagent_v3:/custom/path-a", got[0].id)
	}
}

func TestResolveSources_ExplicitDedup(t *testing.T) {
	t.Parallel()
	got, err := resolveSources(
		[]string{
			"aiagent_v3:/same/path",
			"aiagent_v3:/same/path", // duplicate
			"aiagent_v2:/other",
		},
		silentLogger(),
	)
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 after dedup", len(got))
	}
}

func TestResolveSources_AutoDiscoveryWhenNoFlags(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide HOME.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)
	if err := os.MkdirAll(filepath.Join(tmp, ".ai-agent", "sessions", "session"), 0o755); err != nil {
		t.Fatalf("plant home: %v", err)
	}

	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	// Both v3 and v2 should be discovered — v3's probe is the
	// `session` subdir, v2's probe is the parent dir (deployment.md). No
	// .claude/projects here, so claude-code is not discovered.
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (v3 + v2 share location); got=%+v", len(got), got)
	}
	formats := map[string]bool{}
	for _, s := range got {
		formats[s.format] = true
	}
	if !formats["aiagent_v3"] || !formats["aiagent_v2"] {
		t.Fatalf("missing format(s): %+v", formats)
	}
}

func TestResolveSources_AutoDiscoveryEmptyOnMissingTree(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide HOME.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)
	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 for empty home", len(got))
	}
}

func TestResolveSources_MalformedSourceBubblesError(t *testing.T) {
	t.Parallel()
	_, err := resolveSources([]string{"not-a-format-location-pair"}, silentLogger())
	if err == nil {
		t.Fatal("resolveSources accepted malformed --source; want error")
	}
}

// TestAutoDiscover_ClaudeCodeProbe verifies acceptance #8: a tmpdir layout
// containing ~/.claude/projects/<one project dir> is auto-discovered as a
// claude-code source pointing at the projects root.
func TestAutoDiscover_ClaudeCodeProbe(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide HOME / CLAUDE_CONFIG_DIR.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)
	projects := filepath.Join(tmp, ".claude", "projects", "-home-user-x")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatalf("plant claude projects: %v", err)
	}

	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	var cc *configuredSource
	for i := range got {
		if got[i].format == "claude-code" {
			cc = &got[i]
		}
	}
	if cc == nil {
		t.Fatalf("claude-code source not auto-discovered; got %+v", got)
	}
	wantLoc := filepath.Join(tmp, ".claude", "projects")
	if cc.location != wantLoc {
		t.Fatalf("claude-code location = %q, want %q", cc.location, wantLoc)
	}
	// The registered format must construct via the registry.
	if _, ok := adapters.Get("claude-code"); !ok {
		t.Fatal("claude-code factory not registered")
	}
}

// TestAutoDiscover_ClaudeConfigDirOverride verifies the probe honors
// $CLAUDE_CONFIG_DIR (spec adapter-claude-code.md §2.1).
func TestAutoDiscover_ClaudeConfigDirOverride(t *testing.T) {
	// Not parallel: mutates process-wide env.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp) // no ~/.claude here
	clearOtherAdapterEnv(t)
	cfg := filepath.Join(tmp, "custom-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	projects := filepath.Join(cfg, "projects", "-home-user-y")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatalf("plant custom claude projects: %v", err)
	}

	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	var loc string
	for _, s := range got {
		if s.format == "claude-code" {
			loc = s.location
		}
	}
	want := filepath.Join(cfg, "projects")
	if loc != want {
		t.Fatalf("claude-code location = %q, want %q (CLAUDE_CONFIG_DIR honored)", loc, want)
	}
}

// TestAutoDiscover_NoClaudeCodeWhenAbsent verifies a workstation without
// ~/.claude/projects does not register a claude-code source.
func TestAutoDiscover_NoClaudeCodeWhenAbsent(t *testing.T) {
	// Not parallel: mutates process-wide env.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)

	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	for _, s := range got {
		if s.format == "claude-code" {
			t.Fatalf("claude-code registered with no projects dir present: %+v", got)
		}
	}
}

// TestCountProjectDirs verifies the project-dir counter used for the
// /api/health observability surface (acceptance #8).
func TestCountProjectDirs(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	for _, d := range []string{"-home-user-a", "-home-user-b", "-home-user-c"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	// A stray file must not be counted.
	if err := os.WriteFile(filepath.Join(tmp, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stray: %v", err)
	}
	if n := countProjectDirs(tmp); n != 3 {
		t.Fatalf("countProjectDirs = %d, want 3", n)
	}
	if n := countProjectDirs(filepath.Join(tmp, "missing")); n != 0 {
		t.Fatalf("countProjectDirs(missing) = %d, want 0", n)
	}
}

// fakeCursorLookup is the test double used by the loadSourceCursor
// tests below.
type fakeCursorLookup struct {
	stored string
	err    error
}

func (f fakeCursorLookup) LookupCursor(_ context.Context, _ string) (string, error) {
	return f.stored, f.err
}

// realV3Adapter returns the live aiagent_v3 adapter from the registry.
// The adapter's location does not have to exist for ParseCursor to
// succeed — the v3 ParseCursor is pure and only inspects the JSON.
func realV3Adapter(t *testing.T) canonical.Adapter {
	t.Helper()
	factory, ok := adapters.Get("aiagent_v3")
	if !ok {
		t.Fatalf("aiagent_v3 not registered — check side-effect imports")
	}
	a, err := factory(t.TempDir(), canonical.AdapterOptions{Logger: silentLogger()})
	if err != nil {
		t.Fatalf("construct aiagent_v3: %v", err)
	}
	return a
}

func TestLoadSourceCursor_NilLookupShortCircuits(t *testing.T) {
	t.Parallel()
	cur := loadSourceCursor(context.Background(),
		realV3Adapter(t),
		nil,
		"aiagent_v3:/tmp",
		silentLogger(),
	)
	if cur != nil {
		t.Fatalf("loadSourceCursor nil-lookup = %v, want nil", cur)
	}
}

func TestLoadSourceCursor_EmptyReturnsNilCursor(t *testing.T) {
	t.Parallel()
	cur := loadSourceCursor(context.Background(),
		realV3Adapter(t),
		fakeCursorLookup{stored: "", err: nil},
		"aiagent_v3:/tmp",
		silentLogger(),
	)
	if cur != nil {
		t.Fatalf("loadSourceCursor empty-row = %v, want nil", cur)
	}
}

func TestLoadSourceCursor_LookupErrorFallsBackToNil(t *testing.T) {
	t.Parallel()
	cur := loadSourceCursor(context.Background(),
		realV3Adapter(t),
		fakeCursorLookup{err: errors.New("db gone")},
		"aiagent_v3:/tmp",
		silentLogger(),
	)
	if cur != nil {
		t.Fatalf("loadSourceCursor lookup-err = %v, want nil (fallback to full scan)", cur)
	}
}

func TestLoadSourceCursor_CorruptStoredFallsBackToNil(t *testing.T) {
	t.Parallel()
	// aiagent_v3.ParseCursor rejects garbage JSON; the helper must
	// log WARN and fall back to nil rather than crashing.
	cur := loadSourceCursor(context.Background(),
		realV3Adapter(t),
		fakeCursorLookup{stored: "{not json"},
		"aiagent_v3:/tmp",
		silentLogger(),
	)
	if cur != nil {
		t.Fatalf("loadSourceCursor corrupt-stored = %v, want nil", cur)
	}
}

// TestOnErrorHandler_BlocksThenLandsOnceDrained asserts that the
// OnError handler returned by newOnErrorHandler is BLOCKING when the
// events channel is full. Under a
// saturated worker the adapter goroutine should pause, not silently
// drop SourceErrorEvents. We pump 100 errors into a 4-cap channel, run
// a drainer in parallel, and assert every single event makes it
// through.
func TestOnErrorHandler_BlocksThenLandsOnceDrained(t *testing.T) {
	t.Parallel()

	events := make(chan canonical.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := newOnErrorHandler(ctx, "test-source", events, silentLogger())

	const total = 100
	produced := make(chan struct{})
	go func() {
		for i := 0; i < total; i++ {
			handler(errors.New("synthetic parse error"))
		}
		close(produced)
	}()

	// Drain concurrently. The handler must block until each slot
	// frees, never drop. Use a generous deadline so race-detector
	// schedules don't flake.
	got := 0
	deadline := time.After(5 * time.Second)
	for got < total {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("events channel closed unexpectedly after %d", got)
			}
			if _, isSE := ev.(canonical.SourceErrorEvent); !isSE {
				t.Fatalf("unexpected event type %T", ev)
			}
			got++
		case <-deadline:
			t.Fatalf("only drained %d/%d events within 5s — handler may have dropped silently", got, total)
		}
	}

	// Producer must have completed; nothing should still be blocked.
	select {
	case <-produced:
	case <-time.After(1 * time.Second):
		t.Fatal("producer goroutine did not finish after drain — handler leaked a blocked sender")
	}
}

// TestOnErrorHandler_DropsOnShutdown asserts that once the parent ctx
// is cancelled, the handler returns even if the events channel is full
// — guaranteeing no goroutine leak on ingester shutdown.
func TestOnErrorHandler_DropsOnShutdown(t *testing.T) {
	t.Parallel()

	events := make(chan canonical.Event) // unbuffered → blocks immediately
	ctx, cancel := context.WithCancel(context.Background())

	handler := newOnErrorHandler(ctx, "test-source", events, silentLogger())

	done := make(chan struct{})
	go func() {
		handler(errors.New("synthetic error"))
		close(done)
	}()

	// Confirm the handler is blocked (no drainer is running).
	select {
	case <-done:
		t.Fatal("handler returned before cancel — should have blocked on full channel")
	case <-time.After(50 * time.Millisecond):
	}

	// Cancel: handler must unblock and return.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return within 2s of ctx cancel — goroutine leak")
	}
}

func TestLoadSourceCursor_DecodeRoundTripV3(t *testing.T) {
	t.Parallel()
	// aiagent_v3.ParseCursor("{}") returns a real zero Cursor; this
	// pins the lookup→ParseCursor wiring through the production
	// adapter, not a stub.
	adapter := realV3Adapter(t)
	cur := loadSourceCursor(context.Background(),
		adapter,
		fakeCursorLookup{stored: "{}"},
		"aiagent_v3:/tmp",
		silentLogger(),
	)
	if cur == nil {
		t.Fatal("loadSourceCursor(stored=\"{}\") = nil, want non-nil cursor")
	}
}
