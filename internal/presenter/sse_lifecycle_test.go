package presenter

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRunNotifyPoller_Exported asserts the exported RunNotifyPoller entry
// point (used by the serve binary) starts the poll loop and returns on ctx
// cancel.
func TestRunNotifyPoller_Exported(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.RunNotifyPoller(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunNotifyPoller did not return on cancel")
	}
}

// TestShutdownSSE_DeliversDisconnect asserts the exported ShutdownSSE
// (graceful-shutdown entry point) delivers a disconnect frame to a connected
// client and then closes the hub channels so the stream goroutine returns.
func TestShutdownSSE_DeliversDisconnect(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	sr, cancel, done := startStream(t, p, id, nil)
	defer cancel()
	waitForHeader(t, sr)

	p.ShutdownSSE()

	body := waitForBody(t, sr, "event: disconnect")
	if !strings.Contains(body, "server_shutdown") {
		t.Fatalf("ShutdownSSE did not deliver disconnect:\n%s", body)
	}
	// hub.Shutdown closed the channel, so the stream goroutine must return
	// without the test cancelling its context.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream goroutine did not return after ShutdownSSE closed the hub")
	}
	if p.hub.Has(id) {
		t.Fatalf("hub still holds subscription after Shutdown")
	}
}
