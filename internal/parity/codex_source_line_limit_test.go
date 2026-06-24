package parity

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractCodexSourceOversizedRolloutLineReturnsError(t *testing.T) {
	t.Parallel()

	const wantLimit = 16 * 1024 * 1024

	root := t.TempDir()
	rolloutFile := codexSourceTestRollout(root, "2026", "06", "22", "oversized")
	if err := os.MkdirAll(filepath.Dir(rolloutFile), 0o700); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	if err := os.WriteFile(rolloutFile, append(bytes.Repeat([]byte("x"), wantLimit+1), '\n'), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	_, err := ExtractCodexSource(context.Background(), CodexSourceOptions{Root: root})
	if err == nil {
		t.Fatalf("ExtractCodexSource unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "line exceeds 16777216 bytes") {
		t.Fatalf("error = %v, want line-size failure", err)
	}
}
