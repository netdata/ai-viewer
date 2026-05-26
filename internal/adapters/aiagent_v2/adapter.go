package aiagent_v2

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/netdata/ai-viewer/internal/adapters"
	"github.com/netdata/ai-viewer/internal/canonical"
)

// Format is the stable identifier registered with the adapter registry
// and written into sources.format.
const Format = "aiagent_v2"

// errNotImplemented is the sentinel returned by Scan, Tail, and
// ParseCursor in this skeleton chunk. The real parser lands in SOW-0001
// Chunk 8.
var errNotImplemented = errors.New("aiagent_v2 adapter: not implemented (lands in SOW-0001 Chunk 8)")

// Adapter is the ai-agent v2 source adapter. Construction is via New
// (typed callers) or Factory (registry callers).
//
// One Adapter instance corresponds to one filesystem root configured on
// the ingester. Concurrent Scan+Tail on the same instance is not part of
// the contract (see specs/adapter-contract.md).
type Adapter struct {
	root    string
	logger  *slog.Logger
	onError func(error)
}

// Compile-time conformance: breaks the build if canonical.Adapter drifts.
var _ canonical.Adapter = (*Adapter)(nil)

// New constructs an Adapter rooted at the given filesystem path with the
// shared canonical.AdapterOptions bundle. The root must be a non-empty
// path; an empty root is rejected so misconfigured ingesters fail fast.
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
	return &Adapter{root: root, logger: logger, onError: onError}, nil
}

// Name implements canonical.Adapter.
func (a *Adapter) Name() string { return Format }

// Format implements canonical.Adapter.
func (a *Adapter) Format() string { return Format }

// Scan implements canonical.Adapter. Real implementation lands in
// SOW-0001 Chunk 8.
func (a *Adapter) Scan(_ context.Context, _ canonical.Cursor, _ chan<- canonical.Event) error {
	return fmt.Errorf("aiagent_v2: Scan: %w", errNotImplemented)
}

// Tail implements canonical.Adapter. Lands in SOW-0001 Chunk 8.
func (a *Adapter) Tail(_ context.Context, _ chan<- canonical.Event) error {
	return fmt.Errorf("aiagent_v2: Tail: %w", errNotImplemented)
}

// ParseCursor implements canonical.Adapter. Lands in SOW-0001 Chunk 8.
func (a *Adapter) ParseCursor(stored string) (canonical.Cursor, error) {
	_ = stored
	return nil, fmt.Errorf("aiagent_v2: ParseCursor: %w", errNotImplemented)
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
