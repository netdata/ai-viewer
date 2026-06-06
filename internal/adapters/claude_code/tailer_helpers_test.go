package claude_code

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fsnotify/fsnotify"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestHandleEvent_CreateDirectoryMarksExistingTranscriptsAndMetasDirty(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	newProject := filepath.Join(root, "-home-user-new")
	transcriptRel := "-home-user-new/sess-1.jsonl"
	metaRel := "-home-user-new/sess-1/subagents/agent-abc.meta.json"
	writeFileBytes(t, filepath.Join(root, filepath.FromSlash(transcriptRel)), []byte("{}\n"))
	writeFileBytes(t, filepath.Join(root, filepath.FromSlash(metaRel)), []byte(`{"agentType":"Explore"}`))
	writeFileBytes(t, filepath.Join(newProject, "notes.txt"), []byte("ignore me"))

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	watched := map[string]struct{}{}
	dirty := map[string]struct{}{}
	metaDirty := map[string]struct{}{}
	var errs []string
	handleEvent(watcher, resolvedRoot, root, fsnotify.Event{Name: newProject, Op: fsnotify.Create}, watched, dirty, metaDirty, func(err error) {
		errs = append(errs, err.Error())
	})

	if len(errs) != 0 {
		t.Fatalf("handleEvent surfaced unexpected errors: %v", errs)
	}
	if _, ok := dirty[transcriptRel]; !ok {
		t.Fatalf("create-dir race did not mark existing transcript dirty: %v", dirty)
	}
	if _, ok := metaDirty[metaRel]; !ok {
		t.Fatalf("create-dir race did not mark existing meta dirty: %v", metaDirty)
	}
	if _, ok := dirty["-home-user-new/notes.txt"]; ok {
		t.Fatalf("unrelated file was marked dirty: %v", dirty)
	}
}

func TestHandleEvent_RemoveRenameSurfacesTranscriptAndMetaErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	dirty := map[string]struct{}{}
	metaDirty := map[string]struct{}{}
	var errs []string
	onError := func(err error) { errs = append(errs, err.Error()) }

	handleEvent(watcher, resolvedRoot, root, fsnotify.Event{Name: filepath.Join(root, "-home-user-x", "sess-1.jsonl"), Op: fsnotify.Remove}, map[string]struct{}{}, dirty, metaDirty, onError)
	handleEvent(watcher, resolvedRoot, root, fsnotify.Event{Name: filepath.Join(root, "-home-user-x", "sess-1", "subagents", "agent-abc.meta.json"), Op: fsnotify.Rename}, map[string]struct{}{}, dirty, metaDirty, onError)
	handleEvent(watcher, resolvedRoot, root, fsnotify.Event{Name: filepath.Join(root, "-home-user-x", "notes.txt"), Op: fsnotify.Remove}, map[string]struct{}{}, dirty, metaDirty, onError)

	if len(errs) != 2 {
		t.Fatalf("remove/rename errors = %d, want 2; errs=%v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "-home-user-x/sess-1.jsonl removed/renamed") {
		t.Fatalf("transcript remove error text = %q", errs[0])
	}
	if !strings.Contains(errs[1], "-home-user-x/sess-1/subagents/agent-abc.meta.json removed/renamed") {
		t.Fatalf("meta rename error text = %q", errs[1])
	}
	if len(dirty) != 0 || len(metaDirty) != 0 {
		t.Fatalf("remove/rename should not mark dirty files; dirty=%v metaDirty=%v", dirty, metaDirty)
	}
}

func TestHandleEvent_UnrelatedFilesIgnored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	dirty := map[string]struct{}{}
	metaDirty := map[string]struct{}{}
	var errs []string
	handleEvent(watcher, resolvedRoot, root, fsnotify.Event{Name: filepath.Join(root, "-home-user-x", "notes.txt"), Op: fsnotify.Write}, map[string]struct{}{}, dirty, metaDirty, func(err error) {
		errs = append(errs, err.Error())
	})

	if len(errs) != 0 {
		t.Fatalf("unrelated file surfaced errors: %v", errs)
	}
	if len(dirty) != 0 || len(metaDirty) != 0 {
		t.Fatalf("unrelated file was marked dirty; dirty=%v metaDirty=%v", dirty, metaDirty)
	}
}

func TestFlushDirty_EmptyDirtyEmitsNothing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	cur := newCursor()
	out := make(chan canonical.Event, 8)
	flush := newTailFlush(context.Background(), resolvedRoot, root, sourceIDPrefix+root, &cur, newTailDeferral(), out, func(error) {})
	if err := flush.flushDirty(map[string]struct{}{}, map[string]struct{}{}); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}
	if got := drainBuffered(out); len(got) != 0 {
		t.Fatalf("empty dirty flush emitted %d events, want 0: %#v", len(got), got)
	}
}

func TestFlushDirty_MetaOnlyEmitsRepairThenProgress(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	metaRel := "-home-user-x/sess-1/subagents/agent-abc.meta.json"
	metaRaw := []byte(`{"agentType":"Explore","toolUseId":"toolu_meta_only"}`)
	writeFileBytes(t, filepath.Join(root, filepath.FromSlash(metaRel)), metaRaw)

	cur := newCursor()
	out := make(chan canonical.Event, 8)
	flush := newTailFlush(context.Background(), resolvedRoot, root, sourceIDPrefix+root, &cur, newTailDeferral(), out, func(error) {})
	if err := flush.flushDirty(map[string]struct{}{}, map[string]struct{}{metaRel: {}}); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}

	assertMetaOnlyFlushEvents(t, drainBuffered(out), metaRel, metaRaw)
}

func TestRepairChangedMetasFiltersAndOrdersSubagentUpdates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	fixture := writeRepairChangedMetaFixture(t, root)

	out := make(chan canonical.Event, 8)
	if err := repairChangedMetas(context.Background(), sourceIDPrefix+root, root, resolvedRoot, fixture.startMetaSeen, fixture.currentHashes, out, func(error) {}); err != nil {
		t.Fatalf("repairChangedMetas: %v", err)
	}

	assertRepairUpdates(t, drainBuffered(out), fixture.wantUpdates)
}

func TestTranscriptSessionDirDoesNotMutateProjectParts(t *testing.T) {
	t.Parallel()

	backing := []string{"-home-user-x", "sentinel", "unused"}
	projParts := backing[:1]

	got := transcriptSessionDir("/root", projParts, "sess-1")
	want := filepath.Join("/root", "-home-user-x", "sess-1")
	if got != want {
		t.Fatalf("transcriptSessionDir = %q, want %q", got, want)
	}
	if backing[1] != "sentinel" {
		t.Fatalf("transcriptSessionDir mutated caller backing array: backing[1] = %q", backing[1])
	}
}

type metaRepairExpectation struct {
	nativeID  string
	agentName string
	toolUseID string
}

func assertMetaOnlyFlushEvents(t *testing.T, events []canonical.Event, metaRel string, metaRaw []byte) {
	t.Helper()
	if len(events) != 2 {
		t.Fatalf("meta-only flush emitted %d events, want repair + progress: %#v", len(events), events)
	}
	assertSessionUpdatedEvent(t, events[0], metaRepairExpectation{
		nativeID:  childNativeID("sess-1", "abc"),
		agentName: "Explore",
		toolUseID: "toolu_meta_only",
	}, 0)
	assertProgressMetaSeen(t, events[1], metaRel, metaRaw)
}

func assertProgressMetaSeen(t *testing.T, ev canonical.Event, metaRel string, metaRaw []byte) {
	t.Helper()
	progress, ok := ev.(canonical.SourceProgressEvent)
	if !ok {
		t.Fatalf("progress event = %T, want SourceProgressEvent", ev)
	}
	parsed, err := ParseCursor(progress.Cursor)
	if err != nil {
		t.Fatalf("parse progress cursor: %v", err)
	}
	if got := parsed.metaSeen(metaRel); got != hashBytes(metaRaw) {
		t.Fatalf("progress cursor metaSeen = %q, want %q", got, hashBytes(metaRaw))
	}
}

type repairChangedMetaFixture struct {
	startMetaSeen map[string]string
	currentHashes map[string]string
	wantUpdates   []metaRepairExpectation
}

func writeRepairChangedMetaFixture(t *testing.T, root string) repairChangedMetaFixture {
	t.Helper()
	metas := []struct {
		rel string
		raw []byte
	}{
		{"-home-user-x/sess-1/subagents/agent-a.meta.json", []byte(`{"agentType":"Skip","toolUseId":"toolu_skip"}`)},
		{"-home-user-x/sess-1/subagents/agent-b.meta.json", []byte(`{"agentType":"Build","toolUseId":"toolu_b"}`)},
		{"-home-user-x/sess-1/subagents/agent-c.meta.json", []byte(`{"agentType":"Explore","toolUseId":"toolu_c"}`)},
		{"-home-user-x/sess-1/session.meta.json", []byte(`{"agentType":"Ignored","toolUseId":"toolu_ignored"}`)},
	}
	currentHashes := make(map[string]string, len(metas))
	for _, meta := range metas {
		writeFileBytes(t, filepath.Join(root, filepath.FromSlash(meta.rel)), meta.raw)
		currentHashes[meta.rel] = hashBytes(meta.raw)
	}
	return repairChangedMetaFixture{
		startMetaSeen: map[string]string{metas[0].rel: hashBytes(metas[0].raw)},
		currentHashes: currentHashes,
		wantUpdates: []metaRepairExpectation{
			{nativeID: childNativeID("sess-1", "b"), agentName: "Build", toolUseID: "toolu_b"},
			{nativeID: childNativeID("sess-1", "c"), agentName: "Explore", toolUseID: "toolu_c"},
		},
	}
}

func assertRepairUpdates(t *testing.T, events []canonical.Event, want []metaRepairExpectation) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("repairChangedMetas emitted %d events, want %d changed subagents: %#v", len(events), len(want), events)
	}
	for i, ev := range events {
		assertSessionUpdatedEvent(t, ev, want[i], i)
	}
}

func assertSessionUpdatedEvent(t *testing.T, ev canonical.Event, want metaRepairExpectation, index int) {
	t.Helper()
	update, ok := ev.(canonical.SessionUpdatedEvent)
	if !ok {
		t.Fatalf("event %d = %T, want SessionUpdatedEvent", index, ev)
	}
	if update.NativeID != want.nativeID || update.AgentName != want.agentName {
		t.Fatalf("event %d update = native %q agent %q, want %q %q", index, update.NativeID, update.AgentName, want.nativeID, want.agentName)
	}
	if got := extrasToolUseID(update.Extras); got != want.toolUseID {
		t.Fatalf("event %d toolUseId = %q, want %q", index, got, want.toolUseID)
	}
}
