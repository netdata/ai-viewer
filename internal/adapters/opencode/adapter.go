package opencode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/netdata/ai-viewer/internal/adapters"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file is the registered canonical.Adapter for opencode (SOW-0005 chunk D).
// It mirrors codex/adapter.go exactly, substituting the SQLite specifics: the
// "location" is the opencode database file path (not a sessions directory), and
// the cursor is the per-table watermark cursor (cursor.go) rather than per-file
// byte offsets. The DB is opened ONLY through the chunk-A openReadOnly helper;
// this file never opens a write path.
//
// Format is declared in mapper.go (const Format = "opencode"); it is the single
// stable identifier shared by the mapper (which stamps it onto every LogEntry's
// Source) and this file (which registers it). Defining it once mirrors codex.

// sourceIDPrefix is prepended to the configured database path to produce the
// canonical events' SourceID. Used only for log attribution; idempotency is a
// SQL-layer guarantee keyed on each row's natural identity (not SourceSeq).
// Mirrors codex.
const sourceIDPrefix = Format + ":"

// Adapter is the opencode source adapter. One instance corresponds to one
// opencode database file (default ~/.local/share/opencode/opencode.db). The
// instance is safe for a single Scan goroutine followed by a single Tail
// goroutine; concurrent Scan+Tail on one instance is not part of the contract
// (specs/adapter-contract.md). Mirrors codex.Adapter.
type Adapter struct {
	dbPath        string
	sourceID      string
	logger        *slog.Logger
	tailHeartbeat func()
	// onError surfaces non-fatal per-record parse errors. Never nil after
	// construction; New and Factory substitute a no-op when nil so adapter code
	// can call it unconditionally.
	onError func(error)
	// scanCursor holds the final watermark cursor recorded by the most recent
	// Scan, so a following Tail on the SAME instance resumes from where Scan left
	// off rather than snapshotting current HEAD (closing the Scan→Tail data-loss
	// window). Nil until Scan runs (a cold Tail then falls back to
	// snapshotCursor). The source supervisor drives Scan→Tail on one instance,
	// single-threaded, so a plain field needs no synchronisation. Mirrors codex.
	scanCursor *Cursor
}

// Compile-time conformance to the canonical.Adapter interface.
var _ canonical.Adapter = (*Adapter)(nil)

// New constructs an Adapter for the given opencode database file with the shared
// canonical.AdapterOptions bundle. An empty location (the DB path) is rejected so
// misconfigured ingesters fail fast. Mirrors codex.New.
func New(location string, opts canonical.AdapterOptions) (*Adapter, error) {
	if location == "" {
		return nil, errors.New("opencode: location (database path) must be non-empty")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("adapter", Format, "db", location)
	onError := opts.OnError
	if onError == nil {
		onError = func(error) {}
	}
	sourceID := opts.SourceID
	if sourceID == "" {
		sourceID = sourceIDPrefix + location
	}
	return &Adapter{
		dbPath:        location,
		sourceID:      sourceID,
		logger:        logger,
		tailHeartbeat: opts.TailHeartbeat,
		onError:       onError,
	}, nil
}

// Name implements canonical.Adapter.
func (a *Adapter) Name() string { return Format }

// Format implements canonical.Adapter.
func (a *Adapter) Format() string { return Format }

// Scan implements canonical.Adapter. Opens the database read-only, pages every
// tracked table from `since` forward, and emits the affected sessions' events.
// Returns when caught up or when ctx is cancelled. The caller owns `out`; Scan
// never closes it. Mirrors codex.Scan: the final watermark cursor is recorded on
// the instance even on cancellation so a following Tail resumes from completed
// work rather than replaying from HEAD.
func (a *Adapter) Scan(ctx context.Context, since canonical.Cursor, out chan<- canonical.Event) error {
	start := a.coerceCursor(since)
	final, sErr := scanLoop(ctx, a.dbPath, a.sourceID, start, out, a.logger, a.onError)
	// Record the final watermark even on cancellation so a Tail that follows a
	// context-cancelled Scan still resumes from the watermark reached so far (the
	// cursor reflects only fully-consumed rows). On a hard error it is still the
	// best resume point available.
	cursorCopy := final
	a.scanCursor = &cursorCopy
	if sErr != nil {
		if errors.Is(sErr, context.Canceled) || errors.Is(sErr, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("opencode: scan: %w", sErr)
	}
	return nil
}

// Tail implements canonical.Adapter. Follows the database with the poll-loop
// tailer until ctx is cancelled. Same channel-ownership and cancellation rules as
// Scan. Tail resumes from the watermark cursor the preceding Scan recorded on
// this instance, closing the data-loss window where rows committed BETWEEN Scan
// finishing and Tail starting would be skipped if Tail snapshotted current HEAD.
// Any re-emission of an already-seen session tree is absorbed by the ingester's
// SQL-layer idempotent upserts. A cold Tail with no preceding Scan falls back to a
// current-HEAD snapshot so it follows from now rather than replaying full
// history. Mirrors codex.Tail.
func (a *Adapter) Tail(ctx context.Context, out chan<- canonical.Event) error {
	var cur Cursor
	// warmStart distinguishes a Tail resumed from a Scan cursor (the boundary bucket
	// was already emitted by Scan) from a cold HEAD-snapshot Tail (follow-from-now,
	// boundary never emitted). It seeds the round-6 P1 boundaryReal gate so a cold Tail
	// never replays its snapshot boundary on the first post-snapshot forward change.
	warmStart := a.scanCursor != nil
	if warmStart {
		cur = a.coerceCursor(*a.scanCursor)
	} else {
		snap, err := a.snapshotCursor(ctx)
		if err != nil {
			return fmt.Errorf("opencode: tail snapshot: %w", err)
		}
		cur = snap
	}
	return tailLoopWithHeartbeat(ctx, a.dbPath, a.sourceID, cur, warmStart, out, a.logger, a.onError, a.tailHeartbeat)
}

// ParseCursor implements canonical.Adapter. Empty input yields the zero Cursor;
// non-empty input is decoded as JSON. The returned Cursor is opaque to the
// ingester and used only via Cursor.String() and Cursor.After(). Mirrors
// codex.ParseCursor.
func (a *Adapter) ParseCursor(stored string) (canonical.Cursor, error) {
	c, err := ParseCursor(stored)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// coerceCursor accepts a Cursor produced by this adapter, a nil Cursor (first
// run), or an alien cursor type (treated as empty so the ingester's "I lost
// track" path re-scans from the zero watermark). Never returns nil, and never
// returns a cursor that would skip data on an alien type. Mirrors
// codex.coerceCursor; opencode's cursor carries Tables (not Files), so that is
// the map normalised here.
func (a *Adapter) coerceCursor(c canonical.Cursor) Cursor {
	if c == nil {
		return newCursor()
	}
	if typed, ok := c.(Cursor); ok {
		if typed.Tables == nil {
			typed.Tables = map[string]TableWatermark{}
		}
		if typed.Version == 0 {
			typed.Version = cursorVersion
		}
		return typed
	}
	return newCursor()
}

// snapshotCursor builds a cursor at the database's current HEAD so a cold Tail
// (no preceding Scan) follows changes from now on rather than replaying historical
// events (existing content is Scan's job). It opens read-only, introspects the
// schema, sets each tracked table's watermark to its current MAX(id) +
// MAX(time_updated), and records the real __drizzle_migrations schema hash. This
// is the SQLite analogue of codex stat'ing current file sizes. At a HEAD snapshot
// the monotonic high-water (MaxIDSeen) and the (time_updated, id) paging-position
// id (MaxTimeUpdatedID) both start at the current MAX(id) — paging then follows
// strictly from NOW (SOW-0005 round-2 P1-A). A table on an old schema without
// time_updated contributes the id watermarks only (MaxTimeUpdatedMs stays 0).
func (a *Adapter) snapshotCursor(ctx context.Context) (Cursor, error) {
	db, err := openReadOnly(ctx, a.dbPath, withMaxOpenConns(2))
	if err != nil {
		return Cursor{}, err
	}
	defer func() { _ = db.Close() }()

	schema, err := introspectAll(ctx, db)
	if err != nil {
		return Cursor{}, err
	}

	cur := newCursor().withTargetHash(targetHashForDBPath(a.dbPath))
	for _, table := range trackedTables {
		mid, mErr := maxID(ctx, db, table)
		if mErr != nil {
			return Cursor{}, mErr
		}
		var mtu int64
		if schema[table].has("time_updated") {
			mtu, mErr = maxTimeUpdated(ctx, db, table)
			if mErr != nil {
				return Cursor{}, mErr
			}
		}
		cur = cur.withTable(table, TableWatermark{MaxIDSeen: mid, MaxTimeUpdatedMs: mtu, MaxTimeUpdatedID: mid})
	}
	return recordSchemaHash(ctx, db, cur, a.onError), nil
}

// Factory adapts New to canonical.AdapterFactory so the registry can construct an
// Adapter from the generic (location, opts) pair. The location is the opencode
// database file path. Mirrors codex.Factory.
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
