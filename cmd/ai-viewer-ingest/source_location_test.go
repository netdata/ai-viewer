package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/ingest"
	"github.com/netdata/ai-viewer/internal/store"
)

func TestOpencodeAdapterLocation_RelativeFilePrefixPathBecomesAbsolute(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)

	const rel = "file:opencode.db"
	if err := os.WriteFile(rel, []byte("synthetic"), 0o644); err != nil {
		t.Fatalf("write %q: %v", rel, err)
	}

	got, err := adapterConstructionLocation(configuredSource{format: "opencode", location: rel})
	if err != nil {
		t.Fatalf("adapterConstructionLocation: %v", err)
	}
	want := filepath.Join(tmp, rel)
	if got != want {
		t.Fatalf("opencode adapter location = %q, want absolute filesystem path %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("opencode adapter location = %q, want absolute path", got)
	}
}

func TestAdapterLocation_NonOpencodeRelativeLocationUnchanged(t *testing.T) {
	t.Parallel()

	const rel = "relative/path"
	got, err := adapterConstructionLocation(configuredSource{format: "codex", location: rel})
	if err != nil {
		t.Fatalf("adapterConstructionLocation: %v", err)
	}
	if got != rel {
		t.Fatalf("non-opencode adapter location = %q, want unchanged %q", got, rel)
	}
}

func TestStartSource_OpencodeRelativeFilePrefixPassesAbsoluteLocationAndCanonicalSourceID(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)

	const rel = "file:opencode.db"
	if err := os.WriteFile(rel, []byte("synthetic"), 0o644); err != nil {
		t.Fatalf("write %q: %v", rel, err)
	}

	src := configuredSource{format: "opencode", id: "opencode:" + rel, location: rel}
	call := runStartSourceWithFactoryCapture(t, src)

	wantLocation := filepath.Join(tmp, rel)
	if call.location != wantLocation {
		t.Fatalf("factory location = %q, want absolute filesystem path %q", call.location, wantLocation)
	}
	if !filepath.IsAbs(call.location) {
		t.Fatalf("factory location = %q, want absolute path", call.location)
	}
	if call.opts.SourceID != src.id {
		t.Fatalf("AdapterOptions.SourceID = %q, want original source id %q", call.opts.SourceID, src.id)
	}
}

func TestStartSource_NonOpencodeRelativeLocationUnchanged(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)

	const rel = "relative-source"
	if err := os.WriteFile(rel, []byte("synthetic"), 0o644); err != nil {
		t.Fatalf("write %q: %v", rel, err)
	}

	src := configuredSource{format: "test-format", id: "test-format:" + rel, location: rel}
	call := runStartSourceWithFactoryCapture(t, src)

	if call.location != rel {
		t.Fatalf("factory location = %q, want unchanged relative location %q", call.location, rel)
	}
	if call.opts.SourceID != src.id {
		t.Fatalf("AdapterOptions.SourceID = %q, want source id %q", call.opts.SourceID, src.id)
	}
}

type capturedFactoryCall struct {
	location string
	opts     canonical.AdapterOptions
}

func runStartSourceWithFactoryCapture(t *testing.T, src configuredSource) capturedFactoryCall {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.OpenWriter(ctx, filepath.Join(t.TempDir(), "index.db"), silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	}()

	ing, err := ingest.New(st.DB(), ingest.WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("ingest.New: %v", err)
	}
	if err := ing.Start(ctx); err != nil {
		t.Fatalf("ingest.Start: %v", err)
	}

	var (
		wg   sync.WaitGroup
		call capturedFactoryCall
	)
	lookup := func(format string) (canonical.AdapterFactory, bool) {
		if format != src.format {
			return nil, false
		}
		return func(location string, opts canonical.AdapterOptions) (canonical.Adapter, error) {
			call = capturedFactoryCall{location: location, opts: opts}
			return fakeStartSourceAdapter{format: format}, nil
		}, true
	}

	if err := startSourceWithFactoryLookup(ctx, &wg, ing, nil, src, silentLogger(), lookup); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	cancel()
	wg.Wait()
	if err := ing.Stop(); err != nil {
		t.Fatalf("ingest.Stop: %v", err)
	}
	return call
}

type fakeStartSourceAdapter struct {
	format string
}

func (a fakeStartSourceAdapter) Name() string   { return a.format }
func (a fakeStartSourceAdapter) Format() string { return a.format }

func (a fakeStartSourceAdapter) Scan(context.Context, canonical.Cursor, chan<- canonical.Event) error {
	return nil
}

func (a fakeStartSourceAdapter) Tail(ctx context.Context, _ chan<- canonical.Event) error {
	<-ctx.Done()
	return ctx.Err()
}

func (a fakeStartSourceAdapter) ParseCursor(string) (canonical.Cursor, error) {
	return nil, nil
}
