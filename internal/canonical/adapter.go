package canonical

import (
	"context"
	"log/slog"
)

// Adapter is the contract every source-format adapter implements. One
// adapter is responsible for exactly one source format (ai-agent v2/v3,
// claude-code, codex, opencode, ...). See .agents/sow/specs/adapter-contract.md.
//
// The ingester calls Scan once (historical backfill), then Tail (realtime
// follow). New data that arrives during Scan must be picked up by Tail
// and made idempotent by the ingester via SQL-layer natural-identity
// upserts (NOT a SourceSeq dedup gate; see ingester.md §Dedup and
// Idempotency).
type Adapter interface {
	// Name returns the stable identifier for this adapter, e.g.
	// "aiagent_v3". Used in sources.id and logs.
	Name() string

	// Format returns the user-facing format string written into
	// sources.format. May equal Name or be a friendlier label.
	Format() string

	// Scan emits historical events from the source starting at `since`
	// and returns when caught up to the current state of the source.
	// The adapter MUST NOT close `out`; the ingester owns it. All file
	// I/O must respect ctx cancellation.
	Scan(ctx context.Context, since Cursor, out chan<- Event) error

	// Tail blocks emitting realtime events as the source changes,
	// returning when ctx is cancelled. Same channel-ownership and
	// cancellation rules as Scan.
	Tail(ctx context.Context, out chan<- Event) error

	// ParseCursor decodes a stored cursor JSON blob (the value of
	// source_progress.cursor) into the adapter's concrete Cursor type. An
	// empty input MUST yield a zero Cursor that compares as not-After
	// any non-zero cursor.
	ParseCursor(stored string) (Cursor, error)
}

// Cursor is the adapter-specific resume token persisted in source_progress.cursor.
// The ingester treats it as opaque; the adapter is responsible for
// String/After correctness.
type Cursor interface {
	// String returns an opaque JSON encoding for persistence.
	String() string
	// After reports whether c is strictly after other. The ingester
	// uses this for resume-ordering comparison.
	After(other Cursor) bool
}

// AdapterFactory constructs an Adapter from a location string (path or
// DSN) and the shared options bundle. Adapter packages register their
// factory in internal/adapters/registry.go (introduced in SOW-0001 Chunk 4).
type AdapterFactory func(location string, opts AdapterOptions) (Adapter, error)

// AdapterOptions bundles the cross-cutting dependencies every adapter
// receives at construction time. The struct is intentionally small;
// add fields only when a real adapter needs them.
type AdapterOptions struct {
	// Logger is the structured logger every adapter must use for
	// observability. Subsystem context is set by the ingester before
	// the adapter is constructed.
	Logger *slog.Logger
	// SourceID is the optional canonical source identifier to stamp on
	// emitted events. When empty, adapters keep their historical
	// format:location fallback.
	SourceID string
	// OnError is invoked for non-fatal per-record parse errors. The
	// adapter continues processing after calling OnError. Fatal errors
	// (source unreachable, schema completely wrong) are returned from
	// Scan / Tail instead.
	OnError func(err error)
	// OnTailHeartbeat is invoked by Tail implementations from their real
	// watch/poll loop during idle ticks and after emitted events. Adapters
	// call it through AdapterOptions.TailHeartbeat so an omitted callback is
	// safe.
	OnTailHeartbeat func()
}

// TailHeartbeat invokes the optional tail heartbeat callback. It is nil-safe
// so adapters can call it unconditionally from Tail loops.
func (o AdapterOptions) TailHeartbeat() {
	if o.OnTailHeartbeat != nil {
		o.OnTailHeartbeat()
	}
}
