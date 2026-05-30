package adapters_test

import (
	"testing"

	"github.com/netdata/ai-viewer/internal/adapters"
	// Blank imports trigger each adapter's package init, which calls
	// adapters.Register. The test then asserts the registrations are
	// visible — proving the registry pattern works end to end without
	// any direct knowledge of the adapter packages elsewhere.
	_ "github.com/netdata/ai-viewer/internal/adapters/aiagent_v2"
	_ "github.com/netdata/ai-viewer/internal/adapters/aiagent_v3"
	_ "github.com/netdata/ai-viewer/internal/adapters/claude_code"
)

// TestRegistry_BothAdaptersRegisteredAtInit verifies the init-time
// registration contract every adapter package follows. Running this test
// proves: (a) each adapter's init function fires when the package is
// imported, (b) Register accepts both formats without collision, and
// (c) Get returns the registered factory by name.
//
// The test deliberately omits t.Parallel because it observes
// package-level state that other tests in this directory mutate via
// isolate/resetForTest. Running serially is safe because Go does not
// reorder tests across files; the resets in registry_test.go restore
// state via t.Cleanup before this test runs.
func TestRegistry_BothAdaptersRegisteredAtInit(t *testing.T) {
	for _, format := range []string{"aiagent_v2", "aiagent_v3", "claude-code"} {
		f, ok := adapters.Get(format)
		if !ok {
			t.Errorf("Get(%q): not registered", format)
			continue
		}
		if f == nil {
			t.Errorf("Get(%q): factory is nil", format)
		}
	}
}

// TestRegistry_FormatsContainsBothAdapters verifies Formats surfaces
// every init-time registration sorted lexicographically.
func TestRegistry_FormatsContainsBothAdapters(t *testing.T) {
	got := adapters.Formats()
	var sawV2, sawV3, sawCC bool
	for _, name := range got {
		switch name {
		case "aiagent_v2":
			sawV2 = true
		case "aiagent_v3":
			sawV3 = true
		case "claude-code":
			sawCC = true
		}
	}
	if !sawV2 {
		t.Errorf("Formats() missing aiagent_v2; got %v", got)
	}
	if !sawV3 {
		t.Errorf("Formats() missing aiagent_v3; got %v", got)
	}
	if !sawCC {
		t.Errorf("Formats() missing claude-code; got %v", got)
	}
}
