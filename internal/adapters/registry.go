package adapters

import (
	"fmt"
	"sort"
	"sync"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// mu guards factories. Registration only happens at init time before any
// goroutine runs, so contention is impossible in practice; the RWMutex is
// kept to document the read/write contract for downstream callers and to
// remain correct if a future caller decides to register at runtime.
var (
	mu        sync.RWMutex
	factories = make(map[string]canonical.AdapterFactory)
)

// Register adds an adapter factory to the registry under the given format
// name. Adapters call Register from their package init function so the
// ingester can look up a factory without depending on every adapter
// package directly.
//
// Register panics if format is empty or if format is already registered.
// Both conditions are programmer errors that must be caught at process
// startup; refusing to start beats silently shadowing one adapter with
// another.
func Register(format string, f canonical.AdapterFactory) {
	if format == "" {
		panic("adapters.Register: format must be non-empty")
	}
	if f == nil {
		panic(fmt.Sprintf("adapters.Register(%q): factory must be non-nil", format))
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[format]; dup {
		panic(fmt.Sprintf("adapters.Register(%q): format already registered", format))
	}
	factories[format] = f
}

// Get returns the factory registered for format and true, or (nil, false)
// when the format is unknown. Safe for concurrent callers.
func Get(format string) (canonical.AdapterFactory, bool) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := factories[format]
	return f, ok
}

// Formats returns the names of every registered adapter sorted
// lexicographically. The returned slice is a fresh copy; the caller may
// mutate it freely.
func Formats() []string {
	mu.RLock()
	out := make([]string, 0, len(factories))
	for name := range factories {
		out = append(out, name)
	}
	mu.RUnlock()
	sort.Strings(out)
	return out
}
