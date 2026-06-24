package parity

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractAIAgentV3SourceOversizedLedgerLineReturnsError(t *testing.T) {
	t.Parallel()

	const wantLimit = 4 * 1024 * 1024

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "oversized.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(sessionFile, append(bytes.Repeat([]byte("x"), wantLimit+1), '\n'), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	_, err := ExtractAIAgentV3Source(context.Background(), AIAgentV3SourceOptions{Root: root})
	if err == nil {
		t.Fatalf("ExtractAIAgentV3Source unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "line exceeds 4194304 bytes") {
		t.Fatalf("error = %v, want line-size failure", err)
	}
}
