package aiagent_v2

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/netdata/ai-viewer/internal/adapters"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// Format is the stable identifier registered with the adapter registry
// and written into sources.format.
const Format = "aiagent_v2"

// sourceIDPrefix is prepended to the configured filesystem root to
// produce the canonical events' SourceID
// (e.g. "aiagent_v2:/home/op/.ai-agent/sessions"). Identical to the
// v3 adapter's convention so logs and dedup keys stay grep-coherent.
const sourceIDPrefix = Format + ":"

// Adapter is the ai-agent v2 source adapter. Construction is via New
// (typed callers) or Factory (registry callers).
//
// One Adapter instance corresponds to one filesystem root configured
// on the ingester. The instance is safe for sequential Scan then
// Tail; concurrent Scan+Tail on the same instance is not part of the
// contract (see specs/adapter-contract.md).
type Adapter struct {
	root     string
	sourceID string
	logger   *slog.Logger
	// onError surfaces non-fatal per-record parse errors. Never nil
	// after construction; New and Factory substitute a no-op when the
	// caller passes nil so adapter code can call it unconditionally.
	onError func(error)
}

// Compile-time conformance: breaks the build if canonical.Adapter
// drifts.
var _ canonical.Adapter = (*Adapter)(nil)

// New constructs an Adapter rooted at the given filesystem path with
// the shared canonical.AdapterOptions bundle. The root must be a
// non-empty path; an empty root is rejected so misconfigured
// ingesters fail fast.
func New(root string, opts canonical.AdapterOptions) (*Adapter, error) {
	if root == "" {
		return nil, errors.New("aiagent_v2: root must be non-empty")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("adapter", Format, "root", root)
	onError := opts.OnError
	if onError == nil {
		onError = func(error) {}
	}
	return &Adapter{
		root:     root,
		sourceID: sourceIDPrefix + root,
		logger:   logger,
		onError:  onError,
	}, nil
}

// Name implements canonical.Adapter.
func (a *Adapter) Name() string { return Format }

// Format implements canonical.Adapter.
func (a *Adapter) Format() string { return Format }

// Scan implements canonical.Adapter. Walks <root>/*.json.gz
// non-recursively, decompresses + parses each snapshot whose
// content hash differs from the cursor, and emits canonical events
// to `out`. Returns when caught up to the current state of the
// source or when ctx is cancelled. The caller owns `out`; Scan never
// closes it.
func (a *Adapter) Scan(ctx context.Context, since canonical.Cursor, out chan<- canonical.Event) error {
	start := a.coerceCursor(since)
	_, sErr := scanAll(ctx, a.root, a.sourceID, start, out, a.onError)
	if sErr != nil {
		if errors.Is(sErr, context.Canceled) || errors.Is(sErr, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("aiagent_v2: scan: %w", sErr)
	}
	return nil
}

// Tail implements canonical.Adapter. Subscribes to fsnotify events on
// <root> and emits canonical events as snapshots are
// created/rewritten. Returns when ctx is cancelled.
func (a *Adapter) Tail(ctx context.Context, out chan<- canonical.Event) error {
	cur, err := a.snapshotCursor()
	if err != nil {
		return fmt.Errorf("aiagent_v2: tail snapshot: %w", err)
	}
	return tailLoop(ctx, a.root, a.sourceID, cur, out, a.onError)
}

// ParseCursor implements canonical.Adapter. Empty input yields the
// zero Cursor for first-run callers; non-empty input is decoded as
// JSON.
func (a *Adapter) ParseCursor(stored string) (canonical.Cursor, error) {
	c, err := ParseCursor(stored)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// coerceCursor accepts either a Cursor produced by this adapter, a
// nil canonical.Cursor (first run), or any other concrete type — in
// the latter case it returns an empty cursor so the ingester's "I
// lost track" path simply re-scans from scratch.
func (a *Adapter) coerceCursor(c canonical.Cursor) Cursor {
	if c == nil {
		return newCursor()
	}
	if typed, ok := c.(Cursor); ok {
		if typed.Files == nil {
			typed.Files = map[string]FileCursor{}
		}
		if typed.Version == 0 {
			typed.Version = cursorVersion
		}
		return typed
	}
	return newCursor()
}

// snapshotCursor builds a cursor from current on-disk file mtimes so
// Tail (without a preceding Scan) does not re-emit historical
// snapshots. Each entry's content hash is left empty; the first
// post-Tail change triggers a real read that fills it in.
func (a *Adapter) snapshotCursor() (Cursor, error) {
	files, err := listSnapshots(a.root)
	if err != nil {
		return Cursor{}, err
	}
	cur := newCursor()
	for _, name := range files {
		info, sErr := os.Stat(filepath.Join(a.root, name))
		if sErr != nil {
			if os.IsNotExist(sErr) {
				continue
			}
			a.onError(fmt.Errorf("aiagent_v2: snapshot stat %s: %w", name, sErr))
			continue
		}
		cur = cur.withFile(name, FileCursor{
			LastMtime: info.ModTime().UnixNano(),
			LastSize:  info.Size(),
		})
	}
	return cur, nil
}

// Factory adapts New to canonical.AdapterFactory so the registry can
// construct an Adapter from the generic (location, opts) pair.
func Factory(location string, opts canonical.AdapterOptions) (canonical.Adapter, error) {
	a, err := New(location, opts)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func init() {
	adapters.Register(Format, Factory)
}
