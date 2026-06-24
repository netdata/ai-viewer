package claude_code

import (
	"path/filepath"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// transcriptForRel reconstructs a transcript descriptor from a root-relative
// path. Main transcripts are "<proj>/<sessionId>.jsonl"; subagents are
// "<proj>/<sessionId>/subagents/.../agent-<agentId>.jsonl".
func transcriptForRel(root, rel string) (transcript, bool) {
	if !strings.HasSuffix(rel, transcriptExt) {
		return transcript{}, false
	}
	parts := strings.Split(rel, "/")
	base := parts[len(parts)-1]
	if tr, ok := subagentTranscriptForRel(root, rel, parts, base); ok {
		return tr, true
	}
	if relUnderSubagents(parts) {
		return transcript{}, false
	}
	return rootTranscriptForRel(root, rel, parts, base), true
}

func subagentTranscriptForRel(root, rel string, parts []string, base string) (transcript, bool) {
	for i, p := range parts {
		if p == subagentsDir && i >= 2 {
			if !strings.HasPrefix(base, "agent-") {
				return transcript{}, false
			}
			sessionID := parts[i-1]
			agentID := strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), transcriptExt)
			return transcript{
				rel:            rel,
				abs:            filepath.Join(root, filepath.FromSlash(rel)),
				nativeID:       childNativeID(sessionID, agentID),
				parentNativeID: sessionID,
				kind:           canonical.KindSubAgent,
				sessionDir:     transcriptSessionDir(root, parts[:i-1], sessionID),
			}, true
		}
	}
	return transcript{}, false
}

func relUnderSubagents(parts []string) bool {
	for i, p := range parts {
		if p == subagentsDir && i >= 2 {
			return true
		}
	}
	return false
}

func rootTranscriptForRel(root, rel string, parts []string, base string) transcript {
	sessionID := strings.TrimSuffix(base, transcriptExt)
	return transcript{
		rel:        rel,
		abs:        filepath.Join(root, filepath.FromSlash(rel)),
		nativeID:   sessionID,
		kind:       canonical.KindRoot,
		sessionDir: transcriptSessionDir(root, parts[:len(parts)-1], sessionID),
	}
}

func transcriptSessionDir(root string, projParts []string, sessionID string) string {
	parts := make([]string, 0, len(projParts)+2)
	parts = append(parts, root)
	parts = append(parts, projParts...)
	parts = append(parts, sessionID)
	return filepath.Join(parts...)
}
