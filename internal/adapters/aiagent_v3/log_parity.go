package aiagent_v3

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
)

func v3LogParityExtras(sessionRoot string, rec record, pointer string) map[string]any {
	return map[string]any{
		"aiViewer": map[string]any{
			"parity": map[string]any{
				"nativeArtifactId": v3LogNativeArtifactID(rec.Common.Seq, pointer),
				"selectorURI":      v3LogSelectorURI(sessionRoot, rec.Common.SessionID, rec.Common.Seq),
				"jsonPointer":      pointer,
			},
		},
	}
}

func v3LogNativeArtifactID(seq uint64, pointer string) string {
	return fmt.Sprintf("seq:%d:%s", seq, pointer)
}

func v3LogSelectorURI(sessionRoot string, sessionID string, seq uint64) string {
	return (&url.URL{
		Scheme:   "file",
		Path:     filepath.Join(sessionRoot, "session", sessionID+".jsonl"),
		RawQuery: "seq=" + strconv.FormatUint(seq, 10),
	}).String()
}
