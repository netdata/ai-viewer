package claude_code

import (
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// transcriptExt is the required extension for claude-code transcripts.
const transcriptExt = ".jsonl"

// metaExt is the suffix of a subagent metadata sidecar.
const metaExt = ".meta.json"

// subagentsDir is the directory under a session dir holding sidechains.
const subagentsDir = "subagents"

// scanBufferMax bounds a single transcript line. Real claude-code lines can
// be very large (operator-pasted content, big tool results); 8 MB is ample
// while bounding pathological allocations.
const scanBufferMax = 8 * 1024 * 1024

// metaReadMax bounds a single `.meta.json` sidecar read (spec §6.1, P2.6b). A
// sidecar is a tiny JSON object (agentType, toolUseId, description, a few
// scalars); 1 MB is far above any legitimate size while bounding a pathological
// or hostile oversized sidecar — a meta exceeding it is skipped with a
// SourceError rather than read into memory.
const metaReadMax = 1 * 1024 * 1024

// progressEveryEvents bounds how frequently SourceProgress is emitted by
// record count (spec §7 "every N lines or T ms").
const progressEveryEvents = 200

// progressEveryDuration bounds SourceProgress emission by wall-clock.
const progressEveryDuration = 5 * time.Second

// transcript describes one transcript file discovered under the root.
type transcript struct {
	// rel is the path relative to the root (the cursor key).
	rel string
	// abs is the absolute path on disk.
	abs string
	// nativeID is the canonical session id for this file.
	nativeID string
	// parentNativeID is empty for main transcripts; the parent sessionId
	// for subagents.
	parentNativeID string
	// kind is root or sub_agent.
	kind canonical.SessionKind
	// sessionDir is the absolute path of the parent <sessionId>/ dir for a
	// subagent (used to locate sibling sidecar metas), or "" for main.
	sessionDir string
}
