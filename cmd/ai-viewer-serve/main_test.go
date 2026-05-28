// Tests for the ai-viewer-serve CLI surface. The end-to-end HTTP path
// is covered by the integration smoke in scripts/; this file pins:
//
//   - parseFlags exit-code contract (--version → 0, --help → 0, bad
//     bind → 2, missing --bind value → 2).
//   - assertLocalhost: ONLY 127.0.0.1 and ::1 are accepted; "localhost"
//     is rejected by name; empty host (":7710") is rejected;
//     non-loopback IPs are rejected.
package main

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
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
	if !strings.Contains(read(), "ai-viewer-serve") {
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

func TestParseFlags_UnknownFlagExitsTwo(t *testing.T) {
	t.Parallel()
	stderr, _ := captureStderr(t)
	_, code, ok := parseFlags([]string{"--no-such-flag"}, stderr)
	if ok {
		t.Fatal("parseFlags: want failure on unknown flag, got ok=true")
	}
	if code != 2 {
		t.Fatalf("parseFlags exit code = %d, want 2", code)
	}
}

func TestParseFlags_NonLoopbackBindRejected(t *testing.T) {
	t.Parallel()
	stderr, read := captureStderr(t)
	_, code, ok := parseFlags([]string{"--bind", "10.0.0.1:7710"}, stderr)
	if ok {
		t.Fatal("parseFlags accepted non-loopback bind; want exit 2")
	}
	if code != 2 {
		t.Fatalf("parseFlags exit code = %d, want 2", code)
	}
	if !strings.Contains(read(), "security.md") {
		t.Fatalf("rejection message must cite security.md, got %q", read())
	}
}

func TestParseFlags_LocalhostStringBindRejected(t *testing.T) {
	t.Parallel()
	stderr, read := captureStderr(t)
	_, code, ok := parseFlags([]string{"--bind", "localhost:7710"}, stderr)
	if ok {
		t.Fatal("parseFlags accepted 'localhost' bind; want exit 2")
	}
	if code != 2 {
		t.Fatalf("parseFlags exit code = %d, want 2", code)
	}
	if !strings.Contains(read(), "/etc/hosts") {
		t.Fatalf("rejection message must explain /etc/hosts risk, got %q", read())
	}
}

func TestParseFlags_EmptyHostBindRejected(t *testing.T) {
	t.Parallel()
	stderr, read := captureStderr(t)
	_, code, ok := parseFlags([]string{"--bind", ":7710"}, stderr)
	if ok {
		t.Fatal("parseFlags accepted ':7710' bind; want exit 2")
	}
	if code != 2 {
		t.Fatalf("parseFlags exit code = %d, want 2", code)
	}
	if !strings.Contains(read(), "every interface") {
		t.Fatalf("rejection must explain empty-host means every interface, got %q", read())
	}
}

func TestAssertLocalhost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		addr    string
		wantErr bool
		wantMsg string
	}{
		{"loopback v4", "127.0.0.1:7710", false, ""},
		{"loopback v6", "[::1]:7710", false, ""},
		{"localhost string rejected", "localhost:7710", true, "/etc/hosts"},
		{"empty host rejected", ":7710", true, "every interface"},
		{"non-loopback rejected", "10.0.0.1:7710", true, "127.0.0.1 and ::1"},
		{"invalid host:port", "not-a-port", true, "host:port"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := assertLocalhost(tc.addr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("assertLocalhost(%q) = nil, want error", tc.addr)
				}
				if !strings.Contains(err.Error(), tc.wantMsg) {
					t.Fatalf("assertLocalhost(%q) message = %q, want substring %q",
						tc.addr, err.Error(), tc.wantMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("assertLocalhost(%q) = %v, want nil", tc.addr, err)
			}
		})
	}
}

func TestParseFlags_DefaultBindAccepted(t *testing.T) {
	t.Parallel()
	stderr, _ := captureStderr(t)
	parsed, code, ok := parseFlags(nil, stderr)
	if !ok {
		t.Fatalf("parseFlags(no args) ok=false code=%d, want ok=true", code)
	}
	if parsed.bind != defaultBind {
		t.Fatalf("default bind = %q, want %q", parsed.bind, defaultBind)
	}
}

// TestEmbeddedFrontend_NonFatalWithoutIndex pins the SOW-0001 Chunk 17
// (D2) startup contract: embeddedFrontend() returns a usable fs.FS even
// when the embedded frontend_dist/ has no index.html. On a clean checkout
// the embed dir holds only the tracked .gitkeep sentinel (scripts/build.sh
// writes the real index.html + assets/), so the binary must NOT refuse to
// start — it serves the not-built notice at / while /api stays live. The
// returned FS is still usable: the .gitkeep the `all:` embed captured is
// openable.
func TestEmbeddedFrontend_NonFatalWithoutIndex(t *testing.T) {
	t.Parallel()
	fsys := embeddedFrontend()
	if fsys == nil {
		t.Fatal("embeddedFrontend() returned nil fs.FS")
	}
	// On a clean checkout there is no built index.html; assert the
	// non-fatal state explicitly so a future regression that re-adds the
	// fatal check is caught.
	if _, err := fs.ReadFile(fsys, "frontend_dist/index.html"); !errors.Is(err, fs.ErrNotExist) {
		// A built tree (e.g. CI after scripts/build.sh) legitimately has
		// index.html present; only fail on an unexpected error kind.
		if err != nil {
			t.Fatalf("unexpected error reading index.html: %v", err)
		}
	}
	// The .gitkeep sentinel keeps the `all:` embed non-empty and openable.
	if _, err := fs.ReadFile(fsys, "frontend_dist/.gitkeep"); err != nil {
		t.Fatalf("embedded frontend_dist/.gitkeep not readable: %v", err)
	}
}
