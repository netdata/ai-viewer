package aiagent_v2

import (
	"fmt"
	"net/url"
	"path/filepath"
)

func aiAgentV2LogParityExtras(ctx *mapContext, pointer string) (map[string]any, error) {
	if ctx == nil || ctx.sessionsRoot == "" || ctx.filename == "" {
		return nil, nil
	}
	path, err := resolveSnapshotPath(ctx.sessionsRoot, ctx.filename)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path}
	return map[string]any{
		"aiViewer": map[string]any{
			"parity": map[string]any{
				"nativeArtifactId": aiAgentV2SnapshotFieldNativeID(path, pointer),
				"selectorURI":      uri.String(),
				"jsonPointer":      pointer,
			},
		},
	}, nil
}

func aiAgentV2SnapshotFieldNativeID(sourceFile string, pointer string) string {
	return fmt.Sprintf("file:%s:%s", filepath.Base(sourceFile), pointer)
}

func mergeLogExtras(base map[string]any, parity map[string]any) map[string]any {
	if len(parity) == 0 {
		return base
	}
	if base == nil {
		base = map[string]any{}
	}
	for key, value := range parity {
		base[key] = value
	}
	return base
}
