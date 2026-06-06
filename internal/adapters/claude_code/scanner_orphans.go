package claude_code

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"sort"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// emitOrphanRoots emits a synthetic root SessionStartedEvent for every parent
// sessionId that has subagent transcripts but no own root transcript
// (spec §10.1).
func emitOrphanRoots(ctx context.Context, resolvedRoot, sourceID string, transcripts []transcript, out chan<- canonical.Event) error {
	orphans := orphanRootTimestamps(resolvedRoot, transcripts)
	for _, parentID := range sortedOrphanIDs(orphans) {
		if err := emitOrphanRoot(ctx, sourceID, parentID, orphans[parentID], out); err != nil {
			return err
		}
	}
	return nil
}

func orphanRootTimestamps(resolvedRoot string, transcripts []transcript) map[string]int64 {
	haveRoot := rootSessionSet(transcripts)
	orphans := map[string]int64{}
	for _, tr := range transcripts {
		if !isOrphanChild(tr, haveRoot) {
			continue
		}
		ts := earliestTs(resolvedRoot, tr.abs)
		orphans[tr.parentNativeID] = chooseOrphanTs(orphans[tr.parentNativeID], ts)
	}
	return orphans
}

func rootSessionSet(transcripts []transcript) map[string]struct{} {
	haveRoot := map[string]struct{}{}
	for _, tr := range transcripts {
		if tr.kind == canonical.KindRoot {
			haveRoot[tr.nativeID] = struct{}{}
		}
	}
	return haveRoot
}

func isOrphanChild(tr transcript, haveRoot map[string]struct{}) bool {
	if tr.kind != canonical.KindSubAgent {
		return false
	}
	_, hasParentRoot := haveRoot[tr.parentNativeID]
	return !hasParentRoot
}

func chooseOrphanTs(existing, candidate int64) int64 {
	if existing == 0 || (candidate > 0 && candidate < existing) {
		return candidate
	}
	return existing
}

func sortedOrphanIDs(orphans map[string]int64) []string {
	ids := make([]string, 0, len(orphans))
	for id := range orphans {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func emitOrphanRoot(ctx context.Context, sourceID, parentID string, ts int64, out chan<- canonical.Event) error {
	ev := canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{
			SourceID:  sourceID,
			SourceSeq: 0,
			Ts:        ts,
		},
		NativeID:     parentID,
		RootNativeID: parentID,
		Kind:         canonical.KindRoot,
		Extras:       map[string]any{"orphanRoot": true},
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case out <- ev:
		return nil
	}
}

// earliestTs returns the first parseable record timestamp in a transcript, or
// 0 when none is found. It opens the symlink-resolved path within resolvedRoot.
func earliestTs(resolvedRoot, abs string) int64 {
	f, ok := openEarliestTsTranscript(resolvedRoot, abs)
	if !ok {
		return 0
	}
	defer func() { _ = f.Close() }()
	return scanEarliestTs(f)
}

func openEarliestTsTranscript(resolvedRoot, abs string) (*os.File, bool) {
	resolvedAbs, ok, err := withinResolvedRoot(resolvedRoot, abs)
	if err != nil || !ok {
		return nil, false
	}
	f, openErr := os.Open(resolvedAbs) // #nosec G304 -- opening the containment-checked RESOLVED path (withinResolvedRoot) from a filtered scan under the configured root
	if openErr != nil {
		return nil, false
	}
	return f, true
}

func scanEarliestTs(r io.Reader) int64 {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, _, err := readOneLine(br)
		if err != nil {
			if errors.Is(err, errLineTooLong) {
				continue
			}
			return 0
		}
		if len(line) == 0 {
			return 0
		}
		if ts, ok := parseRecordTs(line[:len(line)-1]); ok {
			return ts
		}
	}
}

func parseRecordTs(line []byte) (int64, bool) {
	rec, skip, err := parseLine(line)
	if err != nil || skip || rec.Env.Timestamp == "" {
		return 0, false
	}
	us, err := parseTsToMicros(rec.Env.Timestamp)
	if err != nil || us <= 0 {
		return 0, false
	}
	return us, true
}
