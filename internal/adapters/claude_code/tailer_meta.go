package claude_code

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// readMetaForRepair reads a dirty `.meta.json` for hashing/parsing on the
// repair path after applying containment and size limits.
func readMetaForRepair(rel, root, resolvedRoot string, onError func(error)) ([]byte, bool) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	resolvedAbs, ok, err := withinResolvedRoot(resolvedRoot, abs)
	if err != nil {
		onError(fmt.Errorf("claude_code: cannot resolve meta %s for containment; skipping: %w", rel, err))
		return nil, false
	}
	if !ok {
		onError(fmt.Errorf("claude_code: meta %s resolves outside the projects root; skipping (symlink escape)", rel))
		return nil, false
	}
	return readContainedMetaForRepair(rel, resolvedAbs, onError)
}

func readContainedMetaForRepair(rel, resolvedAbs string, onError func(error)) ([]byte, bool) {
	raw, err := readMetaCapped(resolvedAbs)
	if err != nil {
		onError(fmt.Errorf("claude_code: read meta %s: %w", rel, err))
		return nil, false
	}
	return raw, true
}

// repairChangedMetas is the shared Scan/Tail late-meta repair entry point.
func repairChangedMetas(ctx context.Context, sourceID, root, resolvedRoot string, startMetaSeen, currentHashes map[string]string, out chan<- canonical.Event, onError func(error)) error {
	repair := metaRepair{
		ctx:           ctx,
		sourceID:      sourceID,
		root:          root,
		resolvedRoot:  resolvedRoot,
		startMetaSeen: startMetaSeen,
		out:           out,
		onError:       ensureOnError(onError),
	}
	return repair.repair(currentHashes)
}

type metaRepair struct {
	ctx           context.Context
	sourceID      string
	root          string
	resolvedRoot  string
	startMetaSeen map[string]string
	out           chan<- canonical.Event
	onError       func(error)
}

func (r metaRepair) repair(currentHashes map[string]string) error {
	for _, rel := range slices.Sorted(maps.Keys(currentHashes)) {
		if err := r.repairOne(rel, currentHashes[rel]); err != nil {
			return err
		}
	}
	return nil
}

func (r metaRepair) repairOne(rel, currentHash string) error {
	if currentHash == r.startMetaSeen[rel] {
		return nil
	}
	childNative, ok := subagentMetaNative(rel)
	if !ok {
		return nil
	}
	meta, ok := r.readMeta(rel)
	if !ok || emptyMetaRepair(meta) {
		return nil
	}
	return r.emitUpdate(childNative, meta)
}

func (r metaRepair) readMeta(rel string) (sidecarMeta, bool) {
	raw, ok := readMetaForRepair(rel, r.root, r.resolvedRoot, r.onError)
	if !ok {
		return sidecarMeta{}, false
	}
	meta, err := parseMeta(raw)
	if err != nil {
		r.onError(fmt.Errorf("claude_code: parse meta %s: %w", rel, err))
		return sidecarMeta{}, false
	}
	return meta, true
}

func emptyMetaRepair(meta sidecarMeta) bool {
	return meta.AgentType == "" && meta.ToolUseID == ""
}

func (r metaRepair) emitUpdate(childNative string, meta sidecarMeta) error {
	ev := canonical.SessionUpdatedEvent{
		EventBase: canonical.EventBase{SourceID: r.sourceID, SourceSeq: 0, Ts: 0},
		NativeID:  childNative,
		AgentName: meta.AgentType,
		Extras:    sessionUpdateExtras(meta.ToolUseID),
	}
	select {
	case <-r.ctx.Done():
		return r.ctx.Err()
	case r.out <- ev:
		return nil
	}
}

func sessionUpdateExtras(toolUseID string) map[string]any {
	if toolUseID == "" {
		return nil
	}
	return map[string]any{"aiViewer": map[string]any{"toolUseId": toolUseID}}
}

// subagentMetaNative derives the child sub-agent's synthetic native id from a
// subagent meta rel path.
func subagentMetaNative(metaRel string) (string, bool) {
	if !strings.HasSuffix(metaRel, metaExt) {
		return "", false
	}
	base := metaRel[strings.LastIndex(metaRel, "/")+1:]
	if !strings.HasPrefix(base, "agent-") {
		return "", false
	}
	return subagentMetaNativeFromParts(metaRel, base)
}

func subagentMetaNativeFromParts(metaRel, base string) (string, bool) {
	agentID := strings.TrimPrefix(strings.TrimSuffix(base, metaExt), "agent-")
	parts := strings.Split(metaRel, "/")
	for i, p := range parts {
		if p == subagentsDir && i >= 2 {
			return childNativeID(parts[i-1], agentID), true
		}
	}
	return "", false
}
