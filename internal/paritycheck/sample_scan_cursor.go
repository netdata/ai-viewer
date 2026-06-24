package paritycheck

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/netdata/ai-viewer/internal/adapters/aiagent_v2"
	"github.com/netdata/ai-viewer/internal/adapters/codex"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/parity"
)

const sampledAIAgentV2SkipHash = "ai-viewer-parity-sampled-skip"

func sampledTempCanonicalScanCursor(ctx context.Context, source Source, sampled []parity.Artifact) (canonical.Cursor, bool, error) {
	switch source.Format {
	case "aiagent_v2":
		return sampledAIAgentV2TempCanonicalScanCursor(ctx, source.Location, sampled)
	case "codex":
		return sampledCodexTempCanonicalScanCursor(ctx, source.Location, sampled)
	default:
		return nil, false, nil
	}
}

func sampledAIAgentV2TempCanonicalScanCursor(ctx context.Context, root string, sampled []parity.Artifact) (canonical.Cursor, bool, error) {
	selected := sampledSourceFileKeys(sampled)
	if len(selected) == 0 {
		return nil, false, nil
	}

	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}

	entries, err := os.ReadDir(resolvedRoot)
	if err != nil {
		return nil, false, err
	}
	cursor, err := aiagent_v2.ParseCursor("")
	if err != nil {
		return nil, false, err
	}
	matchedSample := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if entry.IsDir() || !sampledAIAgentV2SnapshotName(entry.Name()) {
			continue
		}
		path := filepath.Join(resolvedRoot, entry.Name())
		if selectedSourceFile(selected, path) {
			matchedSample = true
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, false, err
		}
		cursor.Files[entry.Name()] = consumedAIAgentV2FileCursor(info)
	}
	if !matchedSample {
		return nil, false, nil
	}
	return cursor, true, nil
}

func sampledAIAgentV2SnapshotName(name string) bool {
	return strings.HasSuffix(name, ".json.gz") && !strings.Contains(name, ".tmp-")
}

func consumedAIAgentV2FileCursor(info os.FileInfo) aiagent_v2.FileCursor {
	return aiagent_v2.FileCursor{
		ContentHash: sampledAIAgentV2SkipHash,
		LastMtime:   info.ModTime().UnixNano(),
		LastSize:    info.Size(),
	}
}

func sampledCodexTempCanonicalScanCursor(ctx context.Context, root string, sampled []parity.Artifact) (canonical.Cursor, bool, error) {
	selected := sampledSourceFileKeys(sampled)
	if len(selected) == 0 {
		return nil, false, nil
	}

	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}

	cursor := codex.Cursor{
		Files:      map[string]codex.FileCursor{},
		LegacyJSON: map[string]codex.LegacyFile{},
	}
	matchedSample := false
	err = filepath.WalkDir(resolvedRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "archived_sessions" && path != resolvedRoot {
				return filepath.SkipDir
			}
			return nil
		}

		selectedFile := selectedSourceFile(selected, path)
		if rel, ok := sampledCodexModernRel(resolvedRoot, path, entry.Name()); ok {
			if selectedFile {
				matchedSample = true
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			cursor.Files[rel] = consumedCodexFileCursor(info)
			return nil
		}
		if sampledCodexLegacyRootFile(resolvedRoot, path, entry.Name()) {
			if selectedFile {
				matchedSample = true
				return nil
			}
			cursor.LegacyJSON[entry.Name()] = codex.LegacyFile{Ingested: true}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if !matchedSample {
		return nil, false, nil
	}
	return cursor, true, nil
}

func sampledSourceFileKeys(sampled []parity.Artifact) map[string]struct{} {
	keys := map[string]struct{}{}
	for _, artifact := range sampled {
		if artifact.SourceFile == "" {
			continue
		}
		if abs, err := filepath.Abs(artifact.SourceFile); err == nil {
			keys[filepath.Clean(abs)] = struct{}{}
			if resolved, err := filepath.EvalSymlinks(abs); err == nil {
				keys[filepath.Clean(resolved)] = struct{}{}
			}
		}
	}
	return keys
}

func selectedSourceFile(selected map[string]struct{}, path string) bool {
	_, ok := selected[filepath.Clean(path)]
	return ok
}

func sampledCodexModernRel(root string, path string, name string) (string, bool) {
	if !strings.HasPrefix(name, "rollout-") || filepath.Ext(name) != ".jsonl" {
		return "", false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if !sampledCodexShardDepth(rel) {
		return "", false
	}
	return rel, true
}

func sampledCodexShardDepth(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts[:3] {
		if !sampledCodexNumeric(part) {
			return false
		}
	}
	return true
}

func sampledCodexNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func sampledCodexLegacyRootFile(root string, path string, name string) bool {
	return filepath.Dir(path) == root && strings.HasPrefix(name, "rollout-") && filepath.Ext(name) == ".json"
}

func consumedCodexFileCursor(info os.FileInfo) codex.FileCursor {
	size := info.Size()
	return codex.FileCursor{
		Offset:           size,
		Size:             size,
		MtimeUs:          info.ModTime().UnixMicro(),
		EOFFinalizedSize: size,
	}
}
