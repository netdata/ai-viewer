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

// errNotImplemented is the sentinel returned by Scan, Tail, and
// ParseCursor in this skeleton chunk. Callers detect "real behaviour has
// not landed yet" with errors.Is. The real parser lands in SOW-0001
// Chunk 6.
var errNotImplemented = errors.New("aiagent_v3 adapter: not implemented (lands in SOW-0001 Chunk 6)")

// Adapter is the ai-agent v3 source adapter. Construction is via New
// (typed callers) or Factory (registry callers).
//
// One Adapter instance corresponds to one filesystem root configured on
// the ingester. The instance is safe for use by a single Scan goroutine
// followed by a single Tail goroutine; concurrent Scan+Tail on the same
// instance is not part of the contract (see specs/adapter-contract.md).
type Adapter struct {
	root   string
	logger *slog.Logger
	// onError surfaces non-fatal per-record parse errors. Never nil after
	// construction; New and Factory substitute a no-op when the caller
	// passes nil so adapter code can call it unconditionally.
	onError func(error)
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
	return &Adapter{root: root, logger: logger, onError: onError}, nil
}

// Name implements canonical.Adapter. v3 returns the format constant
// directly; the source identity is composed by the ingester from format
// and location and is not the adapter's concern.
func (a *Adapter) Name() string { return Format }

// Format implements canonical.Adapter.
func (a *Adapter) Format() string { return Format }

// Scan implements canonical.Adapter. Real implementation lands in
// SOW-0001 Chunk 6. The current skeleton returns the sentinel
// errNotImplemented so the ingester can detect that this adapter is
// wired but not yet functional.
func (a *Adapter) Scan(_ context.Context, _ canonical.Cursor, _ chan<- canonical.Event) error {
	return fmt.Errorf("aiagent_v3: Scan: %w", errNotImplemented)
}

// Tail implements canonical.Adapter. Lands in SOW-0001 Chunk 6.
func (a *Adapter) Tail(_ context.Context, _ chan<- canonical.Event) error {
	return fmt.Errorf("aiagent_v3: Tail: %w", errNotImplemented)
}

// ParseCursor implements canonical.Adapter. Lands in SOW-0001 Chunk 6.
// The real implementation will return a zero Cursor for the empty string
// (first-run case); the skeleton always errors so callers cannot
// accidentally treat the placeholder as a working resume point.
func (a *Adapter) ParseCursor(stored string) (canonical.Cursor, error) {
	_ = stored
	return nil, fmt.Errorf("aiagent_v3: ParseCursor: %w", errNotImplemented)
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
