package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/netdata/ai-viewer/internal/adapters"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// Format is declared in mapper.go (const Format = "codex"); it is the single
// stable identifier shared by the mapper (which stamps it onto LogEntry.Source)
// and this file (which registers it). Defining it once mirrors claude_code,
// where one Format const is shared by mapper.go and adapter.go.

// sourceIDPrefix is prepended to the configured sessions root to produce the
// canonical events' SourceID. Used only for log attribution; idempotency is a
// SQL-layer guarantee keyed on each table's natural identity (not SourceSeq).
// Mirrors claude_code.
const sourceIDPrefix = Format + ":"

// Adapter is the codex source adapter. One instance corresponds to one sessions
// root ($CODEX_HOME/sessions, default ~/.codex/sessions). The instance is safe
// for a single Scan goroutine followed by a single Tail goroutine; concurrent
// Scan+Tail on one instance is not part of the contract (see
// specs/adapter-contract.md). Mirrors claude_code.Adapter.
type Adapter struct {
	root     string
	sourceID string
	logger   *slog.Logger
	// onError surfaces non-fatal per-record parse errors. Never nil after
	// construction; New and Factory substitute a no-op when nil so adapter code
	// can call it unconditionally.
	onError func(error)
	// scanCursor holds the final per-file offsets recorded by the most recent
	// Scan, so a following Tail on the SAME instance resumes from where Scan left
	// off rather than snapshotting current EOF (closing the Scan→Tail data-loss
	// window). Nil until Scan runs (a cold Tail then falls back to
	// snapshotCursor). The ingester drives Scan→Tail on one instance
	// (cmd/ai-viewer-ingest/sources.go runAdapter), single-threaded, so a plain
	// field needs no synchronisation. Mirrors claude_code.
	scanCursor *Cursor
}

// Compile-time conformance to the canonical.Adapter interface.
var _ canonical.Adapter = (*Adapter)(nil)

// New constructs an Adapter rooted at the given sessions directory with the
// shared canonical.AdapterOptions bundle. An empty root is rejected so
// misconfigured ingesters fail fast. Mirrors claude_code.New.
func New(root string, opts canonical.AdapterOptions) (*Adapter, error) {
	if root == "" {
		return nil, errors.New("codex: root must be non-empty")
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

// Scan implements canonical.Adapter. Walks the sessions root, reads each modern
// rollout from its cursor offset to EOF, and emits canonical events. Returns
// when caught up or when ctx is cancelled. The caller owns `out`; Scan never
// closes it. Mirrors claude_code.Scan: the final offsets are recorded on the
// instance even on cancellation so a following Tail resumes from completed work.
func (a *Adapter) Scan(ctx context.Context, since canonical.Cursor, out chan<- canonical.Event) error {
	start := a.coerceCursor(since)
	final, sErr := scanAll(ctx, a.root, a.sourceID, start, out, a.onError)
	// Record the final offsets even on cancellation so a Tail that follows a
	// context-cancelled Scan still resumes from the work that was completed (the
	// cursor reflects only fully-consumed lines). On a hard error the cursor is
	// still the best resume point we have.
	cursorCopy := final
	a.scanCursor = &cursorCopy
	if sErr != nil {
		if errors.Is(sErr, context.Canceled) || errors.Is(sErr, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("codex: scan: %w", sErr)
	}
	return nil
}

// Tail implements canonical.Adapter. Subscribes to fsnotify events on the
// sessions tree and emits canonical events as rollouts grow. Returns when ctx is
// cancelled. Same channel-ownership and cancellation rules as Scan. Tail resumes
// from the per-file offsets the preceding Scan recorded on this instance,
// closing the data-loss window where records appended BETWEEN Scan finishing and
// Tail starting would be skipped if Tail snapshotted current EOF. Any re-emission
// of an already-seen line is absorbed by the ingester's SQL-layer idempotent
// upserts. A cold Tail with no preceding Scan falls back to current file sizes so
// it follows from now rather than replaying full history. Mirrors
// claude_code.Tail.
func (a *Adapter) Tail(ctx context.Context, out chan<- canonical.Event) error {
	var cur Cursor
	if a.scanCursor != nil {
		cur = a.coerceCursor(*a.scanCursor)
	} else {
		snap, err := a.snapshotCursor()
		if err != nil {
			return fmt.Errorf("codex: tail snapshot: %w", err)
		}
		cur = snap
	}
	return tailLoop(ctx, a.root, a.sourceID, cur, out, a.onError)
}

// ParseCursor implements canonical.Adapter. Empty input yields the zero Cursor;
// non-empty input is decoded as JSON. The returned Cursor is opaque to the
// ingester and used only via Cursor.String() and Cursor.After(). Mirrors
// claude_code.ParseCursor.
func (a *Adapter) ParseCursor(stored string) (canonical.Cursor, error) {
	c, err := ParseCursor(stored)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// coerceCursor accepts a Cursor produced by this adapter, a nil Cursor (first
// run), or an alien cursor type (treated as empty so the ingester's "I lost
// track" path re-scans from offset 0). Never returns nil. Mirrors
// claude_code.coerceCursor; codex's cursor carries LegacyJSON (not MetaSeen), so
// that is the map normalized here.
func (a *Adapter) coerceCursor(c canonical.Cursor) Cursor {
	if c == nil {
		return newCursor()
	}
	if typed, ok := c.(Cursor); ok {
		if typed.Files == nil {
			typed.Files = map[string]FileCursor{}
		}
		if typed.LegacyJSON == nil {
			typed.LegacyJSON = map[string]LegacyFile{}
		}
		if typed.Version == 0 {
			typed.Version = cursorVersion
		}
		return typed
	}
	return newCursor()
}

// snapshotCursor builds a cursor from current on-disk rollout sizes so a cold
// Tail does not re-emit historical events (Tail follows changes from now on;
// existing content is Scan's job). Legacy flat .json files are not stat-tracked
// here — they are not ingested in v1 and the cursor's LegacyJSON suppression is
// Scan's concern. Mirrors claude_code.snapshotCursor.
func (a *Adapter) snapshotCursor() (Cursor, error) {
	disc, err := discoverRollouts(a.root, a.onError)
	if err != nil {
		return Cursor{}, err
	}
	cur := newCursor()
	for _, r := range disc.modern {
		info, sErr := os.Stat(r.abs)
		if sErr != nil {
			a.onError(fmt.Errorf("codex: snapshot size %s: %w", r.rel, sErr))
			continue
		}
		size := info.Size()
		cur = cur.withFile(r.rel, FileCursor{
			Offset:  size,
			Size:    size,
			MtimeUs: info.ModTime().UnixMicro(),
		})
	}
	return cur, nil
}

// Factory adapts New to canonical.AdapterFactory so the registry can construct
// an Adapter from the generic (location, opts) pair. The location is treated as
// the sessions root ($CODEX_HOME/sessions, default ~/.codex/sessions; SOW C#3).
// Mirrors claude_code.Factory.
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
