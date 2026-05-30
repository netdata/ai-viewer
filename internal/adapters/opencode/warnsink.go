package opencode

// This file holds warnSink — the in-memory warning buffer that defers all
// warning/error EMISSION until AFTER a source-DB read transaction is committed or
// rolled back (SOW-0005 round-5 P2-1).
//
// The problem: the delta scan (store_query.go scanOnePage / tailer_boundary.go
// scanBoundaryBucket) and the full-session-tree load (tailer_changes.go
// loadAndMapSession) used to pass the adapter's live onError straight into the
// row scanners and tree loaders, which invoke it SYNCHRONOUSLY for a corrupt-cell
// WARN, an unknown-session_message-type WARN, an oversized-session WARN, or a
// root-chain WARN — WHILE the read transaction is still open. onError ultimately
// sends on the adapter's out channel (adapter.go OnError → SourceErrorEvent). If a
// slow ingester backpressures that channel, the send BLOCKS, and the read tx is
// held open across the block — pinning the WAL snapshot on the live multi-GB
// opencode database and delaying opencode's own checkpoint.
//
// The fix: during a read tx, scanners/loaders write warnings into a warnSink
// (a plain slice append, never blocking); the tx-owning function commits/rolls
// back the tx FIRST, then flushes the buffered warnings through the real onError.
// A blocking onError can then only stall AFTER the snapshot is released. Content
// events are likewise emitted only after the tx closes (loadAndMapSession maps +
// the caller emits post-commit). A warnSink is single-goroutine (the poll loop
// owns one per tx scope); it needs no synchronisation.

// warnSink buffers warnings/errors raised while a source-DB read transaction is
// open, for flushing after the tx closes (P2-1). The zero value is ready to use.
type warnSink struct {
	errs []error
}

// collect appends one warning/error to the buffer. It is the func(error) the
// scanners and tree loaders receive IN PLACE of the live onError while a read tx
// is open; it only appends (never blocks, never touches the out channel), so it is
// safe to call with the snapshot held. A nil error is ignored.
func (s *warnSink) collect(err error) {
	if err != nil {
		s.errs = append(s.errs, err)
	}
}

// flush emits every buffered warning through onError (in collection order) and
// resets the buffer, so the same sink can be reused for the next tx scope (e.g.
// the next delta page). It MUST be called only AFTER the read tx is committed or
// rolled back (P2-1), so a backpressured onError can no longer pin the WAL
// snapshot. A nil onError drops the buffer (defensive; production always wires a
// real onError via orNoop). Returns the number of warnings flushed.
func (s *warnSink) flush(onError func(error)) int {
	n := len(s.errs)
	if onError != nil {
		for _, e := range s.errs {
			onError(e)
		}
	}
	s.errs = s.errs[:0]
	return n
}

// len reports how many warnings are currently buffered (used by tests asserting
// the tx-closed-before-flush ordering).
func (s *warnSink) len() int { return len(s.errs) }
