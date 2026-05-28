package presenter

import (
	"net/http"
	"runtime"
	"testing"
	"time"
)

// This file pins the create-vs-shutdown TOCTOU fix: POST /api/subscriptions
// must serialize its shutting-down check together with the hub.Add +
// registry-insert performed by subscriptionManager.create, so a concurrent
// ShutdownSSE cannot interleave between the check and the create. Without the
// presenter's SSE lifecycle mutex an atomic flag is checked once and then the
// create runs unguarded, which admits two defective outcomes (both confirmed
// by external reviewers):
//
//   1. create sees flag=false, then ShutdownSSE runs (flag set, hub closed),
//      then create proceeds: hub.Add is a no-op on the closed hub but the
//      registry insert still runs, so the handler returns 200 for a
//      subscription that can never attach or receive events.
//   2. hub.Add succeeds, then hub.Shutdown deletes the hub entry WITHOUT
//      firing OnRemove (by design), then create's registry insert runs after:
//      an orphan registry entry with no hub channel.
//
// The invariant pinned here is binary: after a create races a shutdown, the
// registry NEVER holds an id the hub does not also hold. Either the
// subscription is fully live in both (create won the mutex) or fully absent
// from both (create observed shutdown and returned without mutating). A
// 200-with-dead-subscription or an orphan registry entry both violate it.
//
// createHook (a nil-in-production *func() field on subscriptionManager,
// invoked inside create AFTER id generation but BEFORE hub.Add and the
// registry insert) is the deterministic seam the test uses to drive
// ShutdownSSE into the create critical section.

// TestSubscriptionsCreate_ShutdownRace_NoOrphan forces the worst-case
// interleaving via the create hook and asserts the registry-vs-hub invariant
// holds. Run under -race; without the lifecycle mutex it fails (orphan
// registry entry / dead 200), with the mutex it passes.
func TestSubscriptionsCreate_ShutdownRace_NoOrphan(t *testing.T) {
	// Not parallel: the hook mutates package-adjacent presenter state and we
	// want the goroutine scheduler focused on this interleaving.
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	// shutdownDone is closed by the shutdown goroutine after ShutdownSSE
	// returns.
	shutdownDone := make(chan struct{})

	// installed inside create, after id-gen, before hub.Add + registry insert.
	p.subs.createHook = func() {
		// Launch ShutdownSSE concurrently. It MUST run on a separate goroutine:
		// with the fix in place the handler goroutine holds sseLifecycleMu
		// across create (hence across this hook), and ShutdownSSE acquires the
		// same mutex — a synchronous call would self-deadlock. On a separate
		// goroutine it instead BLOCKS on the mutex until create completes.
		go func() {
			p.ShutdownSSE()
			close(shutdownDone)
		}()
		// Give the shutdown goroutine the chance to make as much progress as it
		// can before create resumes:
		//   - Without the fix it completes (flag set + hub closed) and closes
		//     shutdownDone, so we return immediately into the buggy window
		//     where create's hub.Add no-ops on the closed hub.
		//   - With the fix it parks on sseLifecycleMu, so shutdownDone never
		//     fires here; the bounded yield budget elapses and we return,
		//     letting create finish fully under the lock first.
		// Either way the post-state assertion below is correct; the budget only
		// governs how reliably the bug reproduces pre-fix, not post-fix
		// correctness (the mutex guarantees that unconditionally).
		for i := 0; i < 1000; i++ {
			select {
			case <-shutdownDone:
				return
			default:
				runtime.Gosched()
			}
		}
	}

	// Drive create directly (same path the handler uses after parse/validate),
	// guarded by the lifecycle mutex exactly as handleSubscriptionsCreate does
	// so the test exercises the real critical section rather than bypassing it.
	id, code := p.createSubscriptionLifecycle(subscriptionFilter{base: sessionFilter{group: groupAll}})

	// Make sure the shutdown goroutine has fully finished before we read the
	// shared maps, so -race sees a clean happens-before edge.
	select {
	case <-shutdownDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ShutdownSSE goroutine did not return")
	}

	// CORE INVARIANT: the registry must never hold an id the hub does not also
	// hold. This single check catches BOTH defective interleavings:
	//
	//   - #1 (200 for a dead sub): create's check passed, shutdown completed
	//     (hub closed) during the hook, create's hub.Add no-op'd on the closed
	//     hub, but the registry insert still ran → the registry holds an id the
	//     hub NEVER held → orphan.
	//   - #2 (insert after hub teardown): create's hub.Add succeeded, then
	//     hub.Shutdown removed it WITHOUT firing OnRemove, then the registry
	//     insert/clear ordering left a registry entry with no hub channel →
	//     orphan.
	//
	// With the lifecycle mutex BOTH are impossible: create completes its
	// hub.Add + registry insert fully before the flag flips, so either the sub
	// is genuinely live before shutdown (then hub.Shutdown + registry clear
	// remove it from BOTH — consistent) or the create observed shutdown and
	// returned 503 having inserted nothing. A 200 for a never-added sub is
	// impossible: if hub.Add had no-op'd, the surviving registry insert would
	// show up here as an orphan.
	for _, item := range p.subs.snapshot() {
		if !p.hub.Has(item.id) {
			t.Fatalf("orphan registry entry %q: present in registry but absent from hub "+
				"(code=%d returnedID=%q)", item.id, code, id)
		}
	}

	// Status/registry self-consistency. In THIS interleaving the hook fires
	// only AFTER create's shutting-down check has already passed (flag was
	// false), so create always wins the race and returns 200; the subsequent
	// full shutdown then tears the live subscription down from both the hub and
	// the registry, leaving both empty. (The 503 ordering — flag set BEFORE the
	// check — is pinned by TestShutdownSSE_ThenCreate_503 and the HTTP-level
	// TestSubscriptionsCreate_ShuttingDownReturns503.)
	switch code {
	case http.StatusOK:
		if id == "" {
			t.Fatal("create reported 200 but returned empty id")
		}
		// Post full-shutdown both sides are empty and consistent (asserted by
		// the orphan loop above); nothing more to check here.
	case http.StatusServiceUnavailable:
		if got := p.subs.count(); got != 0 {
			t.Fatalf("create returned 503 but registry holds %d entries (want 0)", got)
		}
	default:
		t.Fatalf("create returned unexpected status %d", code)
	}
}

// TestShutdownSSE_ThenCreate_503 keeps the simpler ordering covered: once
// ShutdownSSE has fully run, a subsequent create observes the shutting-down
// state and returns 503 with no registry mutation. (The HTTP-level twin lives
// in subscriptions_fixes2_test.go; this drives the lifecycle helper directly.)
func TestShutdownSSE_ThenCreate_503(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	p.ShutdownSSE()

	id, code := p.createSubscriptionLifecycle(subscriptionFilter{base: sessionFilter{group: groupAll}})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("create after shutdown: status = %d, want 503", code)
	}
	if id != "" {
		t.Fatalf("create after shutdown returned id %q, want empty", id)
	}
	if got := p.subs.count(); got != 0 {
		t.Fatalf("registry count = %d after 503, want 0", got)
	}
}

// TestCreateSubscriptionLifecycle_HappyPath pins the no-shutdown path of the
// lifecycle helper: it returns a live id and 200, registered in both the hub
// and the registry.
func TestCreateSubscriptionLifecycle_HappyPath(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	id, code := p.createSubscriptionLifecycle(subscriptionFilter{base: sessionFilter{group: groupAll}})
	if code != http.StatusOK {
		t.Fatalf("create: status = %d, want 200", code)
	}
	if !subIDPattern.MatchString(id) {
		t.Fatalf("id = %q, want sub-<32 hex>", id)
	}
	if !p.hub.Has(id) || !p.subs.has(id) {
		t.Fatalf("subscription %q not registered in both hub and registry", id)
	}
}
