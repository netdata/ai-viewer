package presenter

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// This file holds the Chunk-13 iteration-2 review fix for
// loggingResponseWriter: it must support http.ResponseController unwrapping
// (Unwrap) so SetWriteDeadline/SetReadDeadline reach the real underlying
// writer, and it must delegate FlushError so a real flush error propagates
// (the SSE stream-teardown path relies on this actually reaching the
// writer). See presenter.md §Middlewares.

// deadlineFlushRecorder is an http.ResponseWriter that records whether
// SetWriteDeadline and FlushError were invoked. http.NewResponseController
// reaches these only if loggingResponseWriter.Unwrap exposes this writer.
type deadlineFlushRecorder struct {
	*httptest.ResponseRecorder
	deadlineSet  bool
	flushErrored bool
}

func (d *deadlineFlushRecorder) SetWriteDeadline(time.Time) error {
	d.deadlineSet = true
	return nil
}

func (d *deadlineFlushRecorder) FlushError() error {
	d.flushErrored = true
	return nil
}

// TestLoggingResponseWriter_Unwrap pins that Unwrap returns the inner
// writer so http.ResponseController walks to it.
func TestLoggingResponseWriter_Unwrap(t *testing.T) {
	t.Parallel()
	inner := httptest.NewRecorder()
	lw := &loggingResponseWriter{ResponseWriter: inner, status: http.StatusOK}
	if got := lw.Unwrap(); got != inner {
		t.Fatalf("Unwrap() = %p, want inner %p", got, inner)
	}
}

// TestLoggingResponseWriter_ResponseControllerReachesUnderlying pins FIX 2:
// http.NewResponseController(lw) can SetWriteDeadline and FlushError without
// ErrNotSupported, i.e. the controller unwraps to the recording underlying
// writer and the calls land there.
func TestLoggingResponseWriter_ResponseControllerReachesUnderlying(t *testing.T) {
	t.Parallel()
	rec := &deadlineFlushRecorder{ResponseRecorder: httptest.NewRecorder()}
	lw := &loggingResponseWriter{ResponseWriter: rec, status: http.StatusOK}
	rc := http.NewResponseController(lw)

	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatalf("SetWriteDeadline through wrapper: %v, want nil (Unwrap must reach underlying)", err)
	}
	if !rec.deadlineSet {
		t.Fatal("SetWriteDeadline did not reach the underlying writer")
	}
	if err := rc.Flush(); err != nil {
		t.Fatalf("Flush through wrapper: %v, want nil", err)
	}
	if !rec.flushErrored {
		t.Fatal("FlushError did not reach the underlying writer")
	}
}

// TestLoggingResponseWriter_FlushErrorFallsBackToFlusher pins that when the
// underlying writer is a plain http.Flusher (no FlushError), FlushError
// delegates to Flush() and returns nil. The existing Flush() passthrough is
// unaffected.
func TestLoggingResponseWriter_FlushErrorFallsBackToFlusher(t *testing.T) {
	t.Parallel()
	rec := &flushingRecorder{ResponseRecorder: httptest.NewRecorder()}
	lw := &loggingResponseWriter{ResponseWriter: rec, status: http.StatusOK}
	if err := lw.FlushError(); err != nil {
		t.Fatalf("FlushError fallback: %v, want nil", err)
	}
	if !rec.flushed {
		t.Fatal("FlushError fallback did not invoke Flush on the underlying Flusher")
	}
}

// TestLoggingResponseWriter_FlushErrorUnsupported pins that when the
// underlying writer supports neither FlushError nor Flush, FlushError
// returns http.ErrNotSupported (so callers can detect the absence).
func TestLoggingResponseWriter_FlushErrorUnsupported(t *testing.T) {
	t.Parallel()
	lw := &loggingResponseWriter{ResponseWriter: nonFlusher{httptest.NewRecorder()}, status: http.StatusOK}
	if err := lw.FlushError(); err == nil {
		t.Fatal("FlushError on a non-flushable writer: want error, got nil")
	}
}

// nonFlusher wraps a ResponseWriter but hides Flush/FlushError so the
// wrapper sees a writer that supports neither. The embedded interface is
// promoted for Header/Write/WriteHeader but the concrete recorder's Flush
// is NOT promoted through the interface, so type assertions for
// http.Flusher / FlushError fail.
type nonFlusher struct{ http.ResponseWriter }
