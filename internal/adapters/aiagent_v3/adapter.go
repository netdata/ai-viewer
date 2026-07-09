package aiagent_v3

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/netdata/ai-viewer/internal/adapters"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// Format is the stable identifier registered with the adapter registry
// and written into sources.format. Mirrored by the package directory name
// to keep grep-discoverability sharp.
const Format = "aiagent_v3"

// sourceIDPrefix is prepended to the configured filesystem root to
// produce the canonical events' SourceID (e.g.
// "aiagent_v3:/home/op/.ai-agent/sessions"). The ingester later
// composes its own sources.id; the SourceID we emit is used only for
// log attribution; idempotency is a SQL-layer guarantee keyed on each
// table's natural identity (not SourceSeq).
const sourceIDPrefix = Format + ":"

// Adapter is the ai-agent v3 source adapter. Construction is via New
// (typed callers) or Factory (registry callers).
//
// One Adapter instance corresponds to one filesystem root configured on
// the ingester. The instance is safe for use by a single Scan goroutine
// followed by a single Tail goroutine; concurrent Scan+Tail on the same
// instance is not part of the contract (see specs/adapter-contract.md).
type Adapter struct {
	root          string
	sourceID      string
	logger        *slog.Logger
	tailHeartbeat func()
	// onError surfaces non-fatal per-record parse errors. Never nil after
	// construction; New and Factory substitute a no-op when the caller
	// passes nil so adapter code can call it unconditionally.
	onError func(error)
	// scanCursor holds the final per-file offsets recorded by the most recent
	// Scan, so a following Tail on the same instance catches up from the Scan
	// boundary instead of snapshotting current disk state.
	scanCursor *Cursor
}

// Compile-time conformance: this is the canonical "var _ = …" idiom that
// breaks the build the moment the canonical.Adapter interface drifts
// away from this skeleton.
var _ canonical.Adapter = (*Adapter)(nil)

// New constructs an Adapter rooted at the given filesystem path with the
// shared canonical.AdapterOptions bundle. The root must be a non-empty
// path; an empty root is rejected so misconfigured ingesters fail fast.
func New(root string, opts canonical.AdapterOptions) (*Adapter, error) {
	if root == "" {
		return nil, errors.New("aiagent_v3: root must be non-empty")
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
	sourceID := opts.SourceID
	if sourceID == "" {
		sourceID = sourceIDPrefix + root
	}
	return &Adapter{
		root:          root,
		sourceID:      sourceID,
		logger:        logger,
		tailHeartbeat: opts.TailHeartbeat,
		onError:       onError,
	}, nil
}

// Name implements canonical.Adapter. v3 returns the format constant
// directly; the source identity is composed by the ingester from format
// and location and is not the adapter's concern.
func (a *Adapter) Name() string { return Format }

// Format implements canonical.Adapter.
func (a *Adapter) Format() string { return Format }

// Scan implements canonical.Adapter. Walks <root>/session/*.jsonl,
// reads each file from its cursor offset to EOF, and emits canonical
// events to `out`. Returns when caught up to the current state of the
// source or when ctx is cancelled. The caller owns `out`; Scan never
// closes it.
func (a *Adapter) Scan(ctx context.Context, since canonical.Cursor, out chan<- canonical.Event) error {
	start := a.coerceCursor(since)
	final, sErr := scanAll(ctx, a.root, a.sourceID, start, out, a.onError)
	cursorCopy := final
	a.scanCursor = &cursorCopy
	if sErr != nil {
		if errors.Is(sErr, context.Canceled) || errors.Is(sErr, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("aiagent_v3: scan: %w", sErr)
	}
	return nil
}

// Tail implements canonical.Adapter. Subscribes to fsnotify events on
// <root>/session/ and emits canonical events as new records appear.
// Returns when ctx is cancelled.
func (a *Adapter) Tail(ctx context.Context, out chan<- canonical.Event) error {
	var cur Cursor
	warmStart := a.scanCursor != nil
	if warmStart {
		cur = a.coerceCursor(*a.scanCursor)
	} else {
		snap, err := a.snapshotCursor()
		if err != nil {
			return fmt.Errorf("aiagent_v3: tail snapshot: %w", err)
		}
		cur = snap
	}
	return tailLoopWithHeartbeat(ctx, a.root, a.sourceID, cur, out, a.onError, a.tailHeartbeat, warmStart)
}

// ParseCursor implements canonical.Adapter. Empty input yields the
// zero Cursor for first-run callers; non-empty input is decoded as
// JSON. Per the contract, the returned Cursor is opaque to the
// ingester and used only via Cursor.String() and Cursor.After().
func (a *Adapter) ParseCursor(stored string) (canonical.Cursor, error) {
	c, err := ParseCursor(stored)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// coerceCursor accepts either a Cursor produced by this adapter, a nil
// canonical.Cursor (first run), or any other concrete type — in the
// latter case it returns an empty cursor so the ingester's "I lost
// track" path simply re-scans from offset 0. Per the contract, the
// returned cursor is never nil.
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
	// Be lenient with cursors from a different adapter — start fresh
	// rather than fail Scan outright. This matches the contract's
	// "empty MUST yield zero Cursor" semantics extended to "alien
	// cursors are treated as empty".
	return newCursor()
}

// snapshotCursor builds a cursor from current on-disk file sizes so
// Tail (without a preceding Scan) does not re-emit historical events.
// Per spec §6.1 Tail subscribes to changes from now on; existing
// content is the responsibility of Scan.
func (a *Adapter) snapshotCursor() (Cursor, error) {
	files, err := listLedgers(a.root)
	if err != nil {
		return Cursor{}, err
	}
	cur := newCursor()
	for _, name := range files {
		fc := FileCursor{}
		size, sErr := fileSize(a.root, name)
		if sErr != nil {
			a.onError(fmt.Errorf("aiagent_v3: snapshot size %s: %w", name, sErr))
			continue
		}
		fc.Offset = size
		fc.Size = size
		cur.withFile(name, fc)
	}
	return cur, nil
}

// Factory adapts New to canonical.AdapterFactory so the registry can
// construct an Adapter from the generic (location, opts) pair the
// ingester passes. The location is treated as the filesystem root.
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
