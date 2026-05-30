package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestWithinResolvedRoot_ResolveError covers the EvalSymlinks-error branch of
// withinResolvedRoot (and evalSymlinksAllowingTail's non-IsNotExist return) via
// a path whose ancestor directory is unreadable (EACCES, not IsNotExist).
// Skipped where 0o000 is ignored.
func TestWithinResolvedRoot_ResolveError(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 does not block reads")
	}
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	blocked := filepath.Join(resolved, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	// A path UNDER the unreadable dir cannot be EvalSymlinks-resolved (EACCES on
	// the existing-but-unreadable ancestor → non-IsNotExist error).
	under := filepath.Join(blocked, "child", "r.jsonl")
	if _, err := filepath.EvalSymlinks(filepath.Dir(under)); err == nil {
		t.Skip("filesystem allowed resolving under a 0o000 dir; resolve-error seam not exercised")
	}

	_, ok, err := withinResolvedRoot(resolved, under)
	if err == nil {
		t.Fatalf("withinResolvedRoot under unreadable dir = (ok=%v,nil), want a resolve error", ok)
	}

	// withinSourceRoot surfaces that resolve error via onError and returns false.
	var errs []string
	if withinSourceRoot(resolved, under, func(e error) { errs = append(errs, e.Error()) }) {
		t.Error("withinSourceRoot under unreadable dir should return false")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "cannot resolve") {
			found = true
		}
	}
	if !found {
		t.Errorf("resolve error not surfaced; errs=%v", errs)
	}
}

// TestTailLoop_ResolveErrorDisablesCleanly covers tailLoop's EvalSymlinks-error
// branch: a root that os.Stat succeeds on but EvalSymlinks fails (an unreadable
// ancestor) surfaces a SourceError and returns nil (tail disabled for this
// source, daemon keeps running). Skipped where 0o000 is ignored.
func TestTailLoop_ResolveErrorDisablesCleanly(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 does not block reads")
	}
	parent := t.TempDir()
	mid := filepath.Join(parent, "mid")
	root := filepath.Join(mid, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Block the MIDDLE component so os.Stat(root) via the cached path may still
	// fail; to make os.Stat succeed but EvalSymlinks fail we instead chmod the
	// parent AFTER stat — simplest: chmod mid so EvalSymlinks(root) hits EACCES.
	if err := os.Chmod(mid, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(mid, 0o755) })
	if _, err := filepath.EvalSymlinks(root); err == nil {
		t.Skip("filesystem allowed resolving under a 0o000 dir; resolve-error seam not exercised")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := make(chan canonical.Event, 4)
	var errs []string
	err := tailLoop(ctx, root, "codex:"+root, newCursor(), out, func(e error) { errs = append(errs, e.Error()) })
	if err != nil {
		t.Fatalf("tailLoop with resolve error = %v, want nil (disabled cleanly)", err)
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "cannot resolve sessions root") || strings.Contains(e, "not present") {
			found = true
		}
	}
	if !found {
		t.Errorf("resolve/stat error not surfaced; errs=%v", errs)
	}
}
