package aiagent_v3

import (
	"os"
	"path/filepath"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// drainBuffered collects all events currently available on ch in a
// single non-blocking round. Test-only helper used by multiple scanner
// and tailer tests.
func drainBuffered(ch chan canonical.Event) []canonical.Event {
	out := make([]canonical.Event, 0, cap(ch))
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// mkdirAll is a thin wrapper used by tests so we don't import os in
// every _test.go file. Returns the error from os.MkdirAll verbatim.
func mkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}

// writeFileBytes writes b to path with 0o644 permissions, creating
// parent directories as needed. Test-only helper.
func writeFileBytes(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// appendFileBytes opens path in append mode and writes b. Used by the
// Tail tests to simulate a producer appending to an existing ledger.
func appendFileBytes(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(b)
	return err
}
