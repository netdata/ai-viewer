package canonical

import (
	"errors"
	"fmt"
	"testing"
)

func TestAdapterOptionsTailHeartbeatIsNilSafe(t *testing.T) {
	t.Parallel()

	AdapterOptions{}.TailHeartbeat()

	calls := 0
	opts := AdapterOptions{OnTailHeartbeat: func() { calls++ }}
	opts.TailHeartbeat()
	if calls != 1 {
		t.Fatalf("TailHeartbeat calls = %d, want 1", calls)
	}
}

func TestFatalScanErrorMarker(t *testing.T) {
	t.Parallel()

	root := errors.New("source schema is unusable")
	err := fmt.Errorf("scan failed: %w", NewFatalScanError(root))
	if !IsFatalScanError(err) {
		t.Fatal("IsFatalScanError = false, want true")
	}
	if !errors.Is(err, root) {
		t.Fatal("FatalScanError did not preserve wrapped cause")
	}
	if IsFatalScanError(errors.New("ordinary scan error")) {
		t.Fatal("IsFatalScanError = true for ordinary error")
	}
}
