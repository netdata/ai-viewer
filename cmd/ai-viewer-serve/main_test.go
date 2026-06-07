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
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
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

func TestHTTPServerConfigPinsSSESafeTimeouts(t *testing.T) {
	t.Parallel()
	handler := http.NewServeMux()
	srv := newHTTPServer("127.0.0.1:0", handler)

	if srv.Addr != "127.0.0.1:0" {
		t.Fatalf("server addr = %q, want 127.0.0.1:0", srv.Addr)
	}
	if srv.Handler != handler {
		t.Fatal("server handler does not match supplied handler")
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %s, want 60s", srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %s, want 0 for SSE streams", srv.WriteTimeout)
	}
}

func TestHTTPServerNormalizesClosedServerError(t *testing.T) {
	t.Parallel()
	if err := normalizeServerError(http.ErrServerClosed); err != nil {
		t.Fatalf("normalizeServerError(http.ErrServerClosed) = %v, want nil", err)
	}

	want := errors.New("listen failed")
	if got := normalizeServerError(want); !errors.Is(got, want) {
		t.Fatalf("normalizeServerError(custom) = %v, want %v", got, want)
	}
}

func TestServeNotifyPollerStopCancelsAndDrains(t *testing.T) {
	t.Parallel()
	started := make(chan context.Context, 1)
	returned := make(chan struct{})

	poller := startNotifyPoller(context.Background(), func(ctx context.Context) {
		started <- ctx
		<-ctx.Done()
		close(returned)
	})

	pollerCtx := receiveContext(t, started)
	poller.stopAndWait()

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("notify poller did not return after stopAndWait")
	}
	if !errors.Is(pollerCtx.Err(), context.Canceled) {
		t.Fatalf("poller context err = %v, want context.Canceled", pollerCtx.Err())
	}
}

func TestServeGracefulShutdownOrder(t *testing.T) {
	t.Parallel()
	listenerErr := errors.New("listener failed")
	var steps []string

	err := runGracefulShutdown(testLogger(), time.Second, serveShutdownHooks{
		stopPoller: func() {
			steps = append(steps, "stop-poller")
		},
		waitPoller: func() {
			steps = append(steps, "wait-poller")
		},
		shutdownSSE: func() {
			steps = append(steps, "shutdown-sse")
		},
		shutdownServer: func(ctx context.Context) error {
			steps = append(steps, "http-shutdown")
			if _, ok := ctx.Deadline(); !ok {
				t.Error("shutdown context has no deadline")
			}
			return errors.New("graceful shutdown warning")
		},
		waitListener: func() error {
			steps = append(steps, "wait-listener")
			return listenerErr
		},
	})
	if !errors.Is(err, listenerErr) {
		t.Fatalf("runGracefulShutdown error = %v, want %v", err, listenerErr)
	}

	want := []string{"stop-poller", "wait-poller", "shutdown-sse", "http-shutdown", "wait-listener"}
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("shutdown order = %v, want %v", steps, want)
	}
}

func receiveContext(t *testing.T, ch <-chan context.Context) context.Context {
	t.Helper()
	select {
	case ctx := <-ch:
		return ctx
	case <-time.After(time.Second):
		t.Fatal("notify poller did not start")
	}
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
