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
// are drained by a background consumer per subscription, so Deliver
// always takes the fast non-blocking enqueue path (never the
// drop-oldest branch). That isolates the steady-state channel-send +
// replay-ring-append + id-mint cost that dominates fan-out, rather than
// measuring backpressure (covered by TestHub_BackpressureDropsOldest).
//
// Each measured iteration delivers ONE event to ONE subscription
// (round-robin across the `subs` subscriptions), so ns/op is the
// per-Deliver cost the poller pays. b.RunParallel is intentionally NOT
// used: the production poller delivers serially under a single
// goroutine, and a parallel benchmark would instead measure mutex
// contention, which is a different question.
func BenchmarkHubFanout(b *testing.B) {
	const subs = 1000

	// Large channel cap + replay buffer so a 1000-deep fan-out drains
	// without tripping drop-oldest, and long retention so no timer fires
	// mid-benchmark. The background drainers keep the channels empty.
	h := New(Options{
		ChannelCap:   4096,
		ReplayBuffer: 256,
		Retention:    time.Hour,
	})
	b.Cleanup(h.Shutdown)

	ids := make([]string, subs)
	stop := make(chan struct{})
	for i := 0; i < subs; i++ {
		id := fmt.Sprintf("sub-%04d", i)
		ids[i] = id
		h.Add(id)
		ch, _, _, st := h.Attach(id, "")
		if st != AttachOK {
			b.Fatalf("attach %s: status=%v, want AttachOK", id, st)
		}
		// One drainer goroutine per subscription keeps Deliver on the
		// fast path. It exits when stop is closed (the channel itself is
		// closed by Shutdown via b.Cleanup, which also unblocks the range).
		go func(c <-chan Event) {
			for {
				select {
				case <-stop:
					return
				case _, ok := <-c:
					if !ok {
						return
					}
				}
			}
		}(ch)
	}
	b.Cleanup(func() { close(stop) })

	ev := Event{Kind: "session_changed", SessionID: "x", RootSessionID: "x", TS: 1}

	b.ReportAllocs()
	b.ResetTimer()
	var delivered int64
	for i := 0; i < b.N; i++ {
		if h.Deliver(ids[i%subs], ev) {
			delivered++
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
