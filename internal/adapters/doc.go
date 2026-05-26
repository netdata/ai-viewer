// Package adapters is the registration hub for ai-viewer's source-format
// adapters.
//
// This package itself contains no parsing logic. Concrete adapters live in
// subpackages (internal/adapters/aiagent_v3, internal/adapters/aiagent_v2,
// internal/adapters/claude_code, ...). Each subpackage implements
// canonical.Adapter, exports a canonical.AdapterFactory, and calls Register
// from its package init function so the ingester can look up a factory by
// format name without compile-time knowledge of which adapters exist.
//
// The contract this enables: adding a new source format never edits this
// package or the ingester. A contributor creates a new subpackage, writes
// its adapter and factory, and adds a single init-time Register call. The
// ingester pulls the factory via Get(format) and constructs the adapter
// with the location string and shared canonical.AdapterOptions bundle.
//
// Duplicate registrations panic at init so the process refuses to start
// rather than silently shadowing one factory with another. Registration
// is the only side effect permitted in adapter init functions (see
// project-coding forbidden patterns).
//
// See:
//   - .agents/sow/specs/adapter-contract.md for the Adapter interface and
//     the new-adapter checklist.
//   - internal/canonical/adapter.go for the canonical.Adapter and
//     canonical.AdapterFactory signatures every adapter implements.
package adapters
