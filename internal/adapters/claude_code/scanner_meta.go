package claude_code

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// metaMap is the toolUseId→agentId map recovered from a session dir's
// sidecar .meta.json files, plus the per-agent agentType and the inverse
// agentId→toolUseId map.
type metaMap struct {
	toolUseToAgent map[string]string
	agentType      map[string]string
	agentToolUse   map[string]string
}

func newMetaMap() metaMap {
	return metaMap{
		toolUseToAgent: map[string]string{},
		agentType:      map[string]string{},
		agentToolUse:   map[string]string{},
	}
}

// readSessionMetas reads every agent-*.meta.json under a session dir's
// subagents/ and returns the toolUseId→agentId map plus per-agent agentType.
// Missing dirs/files are benign; present-but-broken metas surface SourceError.
func readSessionMetas(resolvedRoot, sessionDir string, onError func(error)) metaMap {
	mm := newMetaMap()
	if sessionDir == "" {
		return mm
	}
	subDir := filepath.Join(sessionDir, subagentsDir)
	for _, path := range collectMetaPaths(subDir, onError) {
		addSessionMeta(mm, resolvedRoot, path, onError)
	}
	return mm
}

func addSessionMeta(mm metaMap, resolvedRoot, path string, onError func(error)) {
	resolvedPath, ok := resolveMetaPath(resolvedRoot, path, onError)
	if !ok {
		return
	}
	raw, readErr := readMetaCapped(resolvedPath)
	if readErr != nil {
		onError(fmt.Errorf("claude_code: read meta %s: %w", path, readErr))
		return
	}
	meta, err := parseMeta(raw)
	if err != nil {
		onError(fmt.Errorf("claude_code: parse meta %s: %w", path, err))
		return
	}
	recordSessionMeta(mm, path, meta)
}

func resolveMetaPath(resolvedRoot, path string, onError func(error)) (string, bool) {
	resolvedPath, ok, err := withinResolvedRoot(resolvedRoot, path)
	if err != nil {
		onError(fmt.Errorf("claude_code: cannot resolve meta %s for containment; skipping: %w", path, err))
		return "", false
	}
	if !ok {
		onError(fmt.Errorf("claude_code: meta %s resolves outside the projects root; skipping (symlink escape)", path))
		return "", false
	}
	return resolvedPath, true
}

func recordSessionMeta(mm metaMap, path string, meta sidecarMeta) {
	agentID := strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), metaExt), "agent-")
	if meta.AgentType != "" {
		mm.agentType[agentID] = meta.AgentType
	}
	if meta.ToolUseID != "" {
		mm.toolUseToAgent[meta.ToolUseID] = agentID
		mm.agentToolUse[agentID] = meta.ToolUseID
	}
}

// sidecarMeta is the subset of a subagent `.meta.json` the adapter consumes.
type sidecarMeta struct {
	AgentType string `json:"agentType"`
	ToolUseID string `json:"toolUseId"`
}

// parseMeta decodes a `.meta.json` body into the consumed fields.
func parseMeta(raw []byte) (sidecarMeta, error) {
	var meta sidecarMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return sidecarMeta{}, err
	}
	return meta, nil
}

// readMetaCapped reads a `.meta.json` sidecar bounded at metaReadMax (spec
// §6.1, P2.6b). The path MUST be containment-checked and symlink-resolved.
func readMetaCapped(resolvedAbs string) ([]byte, error) {
	f, err := os.Open(resolvedAbs) // #nosec G304 -- reading the containment-checked RESOLVED path (withinResolvedRoot) from the configured read-only projects root
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if info, statErr := f.Stat(); statErr == nil && info.Size() > metaReadMax {
		return nil, fmt.Errorf("%w (%d bytes > %d)", errMetaTooLarge, info.Size(), metaReadMax)
	}
	return readMetaLimited(f)
}

func readMetaLimited(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, metaReadMax+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > metaReadMax {
		return nil, fmt.Errorf("%w (exceeds %d)", errMetaTooLarge, metaReadMax)
	}
	return raw, nil
}

// errMetaTooLarge signals a `.meta.json` sidecar exceeding metaReadMax (P2.6b).
var errMetaTooLarge = errors.New("claude_code: meta sidecar too large")

// collectMetaPaths walks dir and returns the absolute paths of every
// agent-*.meta.json found, sorted for determinism. A missing dir
// (os.IsNotExist) yields an empty slice and is NOT an error.
func collectMetaPaths(dir string, onError func(error)) []string {
	onError = ensureOnError(onError)
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return handleMetaWalkError(path, d, err, onError)
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), metaExt) {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths
}

func handleMetaWalkError(path string, d os.DirEntry, err error, onError func(error)) error {
	if os.IsNotExist(err) {
		return filepath.SkipDir
	}
	onError(fmt.Errorf("claude_code: walk metas under %s: %w", path, err))
	if d != nil && d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

// metaHashes returns a map of meta-file relative path → sha256 of content for
// every .meta.json under the projects root. Used by scan/tail to detect
// sidecar rewrites (spec §7 step 4).
func metaHashes(root, resolvedRoot string, onError func(error)) map[string]string {
	_ = root // rel keys are computed against resolvedRoot; see adapter spec §6.1.
	out := map[string]string{}
	for _, path := range collectMetaPaths(resolvedRoot, onError) {
		recordMetaHash(out, resolvedRoot, path, onError)
	}
	return out
}

func recordMetaHash(out map[string]string, resolvedRoot, path string, onError func(error)) {
	resolvedPath, ok := resolveMetaPath(resolvedRoot, path, onError)
	if !ok {
		return
	}
	rel, err := relPath(resolvedRoot, path)
	if err != nil {
		onError(fmt.Errorf("claude_code: relpath meta %s: %w", path, err))
		return
	}
	raw, readErr := readMetaCapped(resolvedPath)
	if readErr != nil {
		onError(fmt.Errorf("claude_code: read meta %s: %w", path, readErr))
		return
	}
	out[rel] = hashBytes(raw)
}

// hashBytes returns the hex-encoded sha256 of b. Shared by the scan-time
// meta-hash walk and the tail-time per-file meta checkpoint.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
