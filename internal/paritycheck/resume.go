package paritycheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/netdata/ai-viewer/internal/parity"
)

const resumeCursorVersion = 1

type resumeState struct {
	path   string
	mu     sync.Mutex
	cursor resumeCursorFile
}

type resumeCursorFile struct {
	Version int                          `json:"version"`
	Sources map[string]resumeCursorEntry `json:"sources"`
}

type resumeCursorEntry struct {
	Source   Source         `json:"source"`
	Snapshot sourceSnapshot `json:"snapshot"`
	Result   SourceResult   `json:"result"`
}

func openResumeState(path string) (*resumeState, error) {
	if path == "" {
		return nil, nil
	}
	cursor := resumeCursorFile{
		Version: resumeCursorVersion,
		Sources: map[string]resumeCursorEntry{},
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		if os.IsNotExist(err) {
			return &resumeState{path: path, cursor: cursor}, nil
		}
		return nil, fmt.Errorf("open resume cursor directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		if os.IsNotExist(err) {
			return &resumeState{path: path, cursor: cursor}, nil
		}
		return nil, fmt.Errorf("read resume cursor: %w", err)
	}
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, fmt.Errorf("decode resume cursor: %w", err)
	}
	if cursor.Version != resumeCursorVersion {
		return nil, fmt.Errorf("unsupported resume cursor version %d", cursor.Version)
	}
	if cursor.Sources == nil {
		cursor.Sources = map[string]resumeCursorEntry{}
	}
	return &resumeState{path: path, cursor: cursor}, nil
}

func (r *resumeState) lookup(source Source, snapshot sourceSnapshot) (SourceResult, bool) {
	if r == nil {
		return SourceResult{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cursor.Sources[source.SourceID]
	if !ok {
		return SourceResult{}, false
	}
	if !resumeSourceMatches(entry.Source, source) {
		return SourceResult{}, false
	}
	if !sourceSnapshotsEqual(entry.Snapshot, snapshot) {
		return SourceResult{}, false
	}
	result := entry.Result
	result.Skipped = true
	result.SkipReason = "resume cursor source snapshot matched"
	return result, true
}

func (r *resumeState) hasMatchingSourceSnapshot(source Source, snapshot sourceSnapshot) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cursor.Sources[source.SourceID]
	if !ok {
		return false
	}
	return resumeSourceMatches(entry.Source, source) && sourceSnapshotsEqual(entry.Snapshot, snapshot)
}

func (r *resumeState) record(ctx context.Context, source Source, snapshot sourceSnapshot, result SourceResult) error {
	if r == nil || result.Skipped || result.State == parity.StateIncomplete || result.State == parity.StateSampleOnly {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stored := result
	stored.Skipped = false
	stored.SkipReason = ""

	r.mu.Lock()
	defer r.mu.Unlock()
	r.cursor.Sources[source.SourceID] = resumeCursorEntry{
		Source:   source,
		Snapshot: snapshot,
		Result:   stored,
	}
	return r.saveLocked()
}

func (r *resumeState) saveLocked() error {
	raw, err := json.MarshalIndent(r.cursor, "", "  ")
	if err != nil {
		return fmt.Errorf("encode resume cursor: %w", err)
	}
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create resume cursor directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".resume-*.tmp")
	if err != nil {
		return fmt.Errorf("create resume cursor temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod resume cursor temp file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write resume cursor: %w", err)
	}
	if _, err := tmp.WriteString("\n"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write resume cursor newline: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close resume cursor temp file: %w", err)
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return fmt.Errorf("replace resume cursor: %w", err)
	}
	return nil
}

func resumeSourceMatches(left Source, right Source) bool {
	return left.Format == right.Format && left.Location == right.Location && left.SourceID == right.SourceID
}

func sourceSnapshotsEqual(left sourceSnapshot, right sourceSnapshot) bool {
	if left.Root != right.Root || len(left.Files) != len(right.Files) {
		return false
	}
	for path, leftFile := range left.Files {
		rightFile, ok := right.Files[path]
		if !ok || leftFile != rightFile {
			return false
		}
	}
	return true
}
