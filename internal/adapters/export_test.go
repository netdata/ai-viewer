package adapters

import (
	"maps"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// resetForTest clears the package-level registry. Tests that mutate the
// registry call this from an isolated subtest, snapshot first via
// snapshotForTest, and restore via restoreForTest in t.Cleanup so that
// init-time registrations performed by sibling _test.go files in the
// same test binary are not lost.
func resetForTest() {
	mu.Lock()
	defer mu.Unlock()
	clear(factories)
}

// snapshotForTest returns a copy of the current registry contents.
func snapshotForTest() map[string]canonical.AdapterFactory {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]canonical.AdapterFactory, len(factories))
	maps.Copy(out, factories)
	return out
}

// restoreForTest replaces the registry contents with snap. Used by tests
// to undo their mutations after a resetForTest call.
func restoreForTest(snap map[string]canonical.AdapterFactory) {
	mu.Lock()
	defer mu.Unlock()
	clear(factories)
	maps.Copy(factories, snap)
}
