package presenter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/notify"
)

// TestAttachEventStream_RequiresFlusherBeforeAttach pins the handler's
// current ordering: a writer that cannot flush is rejected before hub.Attach,
// so it never marks the subscription busy or arms disconnect lifecycle state.
func TestAttachEventStream_RequiresFlusherBeforeAttach(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	req := httptest.NewRequest(http.MethodGet, "/api/events?sub="+id, nil)
	rr := newNonFlushingRecorder()
	if _, ok := p.attachEventStream(rr, req, id); ok {
		t.Fatal("attachEventStream succeeded with a non-flushing writer")
	}
	if rr.status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.status)
	}

	if _, _, _, st := p.hub.Attach(id, ""); st != notify.AttachOK {
		t.Fatalf("Attach after non-flusher rejection = %v, want AttachOK; helper touched lifecycle", st)
	}
	p.hub.Detach(id)
}

func TestAttachEventStream_StatusMappingUnknownSubscription(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/events?sub=sub-deadbeefdeadbeefdeadbeefdeadbeef", nil)
	rr := httptest.NewRecorder()
	if _, ok := p.attachEventStream(rr, req, "sub-deadbeefdeadbeefdeadbeefdeadbeef"); ok {
		t.Fatal("attachEventStream succeeded for an unknown subscription")
	}
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d, want 404", rr.Code)
	}
}

func TestAttachEventStream_StatusMappingBusySubscription(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	if _, _, _, st := p.hub.Attach(id, ""); st != notify.AttachOK {
		t.Fatalf("precondition Attach = %v, want AttachOK", st)
	}
	busyReq := httptest.NewRequest(http.MethodGet, "/api/events?sub="+id, nil)
	busyRR := httptest.NewRecorder()
	if _, ok := p.attachEventStream(busyRR, busyReq, id); ok {
		t.Fatal("attachEventStream succeeded for a busy subscription")
	}
	if busyRR.Code != http.StatusConflict {
		t.Fatalf("busy status = %d, want 409", busyRR.Code)
	}
	p.hub.Detach(id)
}

func TestAttachEventStream_StatusMappingSuccess(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	successReq := httptest.NewRequest(http.MethodGet, "/api/events?sub="+id, nil)
	successRR := httptest.NewRecorder()
	stream, ok := p.attachEventStream(successRR, successReq, id)
	if !ok {
		t.Fatalf("attachEventStream rejected a valid idle subscription: status=%d body=%q",
			successRR.Code, successRR.Body.String())
	}
	if stream.ch == nil {
		t.Fatal("attached stream channel is nil")
	}
	if successRR.Body.Len() != 0 {
		t.Fatalf("attachEventStream wrote a success body before streaming: %q", successRR.Body.String())
	}
	p.hub.Detach(id)
}

type nonFlushingRecorder struct {
	header http.Header
	body   strings.Builder
	status int
}

func newNonFlushingRecorder() *nonFlushingRecorder {
	return &nonFlushingRecorder{header: http.Header{}}
}

func (r *nonFlushingRecorder) Header() http.Header {
	return r.header
}

func (r *nonFlushingRecorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
}

func (r *nonFlushingRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}
