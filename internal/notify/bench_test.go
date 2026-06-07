package notify

import (
	"fmt"
	"testing"
	"time"
)

// BenchmarkHubFanout measures the per-Deliver fan-out cost of the SSE
// Hub at scale: one Deliver call enqueues an event into a single
// subscription's channel under the hub mutex (hub.go:233). The
// presenter's poller calls Deliver once per matched (subscription,
// event) pair, so at N connected clients a single notify-table row
// fans out as N Deliver calls — this benchmark is that hot loop.
//
// The hub is seeded with `subs` attached subscriptions whose channels
// are sized for the current measured run, so Deliver always takes the
// fast non-blocking enqueue path (never the drop-oldest branch). That
// isolates the steady-state channel-send + replay-ring-append + id-mint
// cost that dominates fan-out, rather than measuring backpressure
// (covered by TestHub_BackpressureDropsOldest).
//
// Do NOT add helper drainer goroutines here. The production poller
// delivers serially, and helper-goroutine scheduling noise can dominate
// this microbenchmark under normal desktop/VM load. Deterministic buffer
// sizing keeps the timed environment focused on the serial Deliver hot
// path.
//
// Each measured iteration delivers one deterministic fixed batch of events,
// round-robin across the `subs` subscriptions. The batch is a bounded slice of
// the serial poller hot loop, large enough to amortize timer/scheduler noise
// without forcing every operation to enqueue once to every subscription. The
// deliveries/sec metric below keeps the per-delivery interpretation visible.
// b.RunParallel is intentionally NOT used: the production poller delivers
// serially under a single goroutine, and a parallel benchmark would instead
// measure mutex contention, which is a different question.
func BenchmarkHubFanout(b *testing.B) {
	const subs = 1000
	const deliveriesPerOp = 256

	// Size each subscription channel for the number of round-robin deliveries it
	// will receive in this run. This keeps the benchmark on the fast enqueue path
	// without background drainers in the timed environment.
	channelCap := channelCapForRoundRobinDeliveries(b.N*deliveriesPerOp, subs)
	h := New(Options{
		ChannelCap:   channelCap,
		ReplayBuffer: 256,
		Retention:    time.Hour,
	})
	b.Cleanup(h.Shutdown)

	ids := make([]string, subs)
	for i := 0; i < subs; i++ {
		id := fmt.Sprintf("sub-%04d", i)
		ids[i] = id
		h.Add(id)
		_, _, _, st := h.Attach(id, "")
		if st != AttachOK {
			b.Fatalf("attach %s: status=%v, want AttachOK", id, st)
		}
	}

	ev := Event{Kind: "session_changed", SessionID: "x", RootSessionID: "x", TS: 1}

	b.ReportAllocs()
	b.ResetTimer()
	var delivered int64
	nextSub := 0
	for i := 0; i < b.N; i++ {
		for deliver := 0; deliver < deliveriesPerOp; deliver++ {
			if h.Deliver(ids[nextSub], ev) {
				delivered++
			}
			nextSub++
			if nextSub == subs {
				nextSub = 0
			}
		}
	}
	b.StopTimer()

	wallSec := b.Elapsed().Seconds()
	if wallSec <= 0 {
		wallSec = 1e-9
	}
	b.ReportMetric(float64(delivered)/wallSec, "deliveries/sec")
	b.ReportMetric(float64(subs), "subscriptions")
}

func channelCapForRoundRobinDeliveries(deliveries, subscriptions int) int {
	if deliveries <= 0 || subscriptions <= 0 {
		return 1
	}
	return (deliveries + subscriptions - 1) / subscriptions
}
