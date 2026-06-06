package opencode

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// This file holds the WAL fsnotify WAKEUP-HINT machinery and the timer-reset
// idiom the realtime poll loop (tailLoop in tailer.go) uses. Split out of
// tailer.go to keep each file ≤400 lines (SOW-0005 round-2; the P1-A/P3-C comment
// expansions pushed tailer.go over budget). None of this is authoritative change
// detection — the hint only nudges the cadence; the MAX(id)/MAX(time_updated)
// probes (tailer.go) decide what actually changed.

// watchWAL sets up a best-effort fsnotify watch on the opencode.db-wal companion
// path and returns a channel that fires (a bare struct{}) on each Write/Chmod
// event plus a close func. It is a WAKEUP HINT ONLY: a missing WAL file, an Add
// failure, or a watcher error is reported once via onError and yields a closed
// channel so the caller falls back to pure timer polling. The watch is on the
// PARENT directory (the WAL file may not exist yet, and watching a not-yet-
// existing file fails); events are filtered to the WAL basename.
func watchWAL(dbPath string, onError func(error)) (<-chan struct{}, func()) {
	walPath := dbPath + "-wal"
	dir := filepath.Dir(walPath)
	walBase := filepath.Base(walPath)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		onError(fmt.Errorf("opencode: wal watcher (falling back to timer polling): %w", err))
		return closedHintChan(), func() {}
	}
	if aerr := watcher.Add(dir); aerr != nil {
		onError(fmt.Errorf("opencode: watch wal dir %s (falling back to timer polling): %w", dir, aerr))
		_ = watcher.Close()
		return closedHintChan(), func() {}
	}

	hint := make(chan struct{}, 1)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go runWALWatcher(watcher, walBase, hint, done, &wg, onError)

	// closeWatch stops the goroutine and WAITS for it to exit before returning
	// (SOW-0005 round-7 P2-2). The watcher goroutine may call onError (a watcher
	// error) — i.e. a send on the adapter's out channel; if closeWatch returned
	// before the goroutine exited, the source goroutine could then close(events)
	// while this goroutine was still sending on it → a send-on-closed-channel
	// panic. signalling `done` AND closing the watcher unblocks the select; the
	// WaitGroup makes the goroutine's exit a happens-before of closeWatch's return
	// (Tail's `defer closeWatch()` therefore guarantees the watcher goroutine is
	// dead before Tail returns and the source closes events). It is idempotent:
	// `close(done)` is guarded by sync.Once so a double call (e.g. an explicit call
	// plus the deferred one) does not panic.
	var once sync.Once
	closeWatch := func() {
		once.Do(func() {
			close(done)
			_ = watcher.Close()
		})
		wg.Wait()
	}
	return hint, closeWatch
}

func runWALWatcher(watcher *fsnotify.Watcher, walBase string, hint chan<- struct{}, done <-chan struct{}, wg *sync.WaitGroup, onError func(error)) {
	defer wg.Done()
	defer close(hint)
	for {
		select {
		case <-done:
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if walEventMatches(ev, walBase) {
				sendWALHint(hint)
			}
		case werr, ok := <-watcher.Errors:
			if !ok {
				return
			}
			onError(fmt.Errorf("opencode: wal watcher error: %w", werr))
		}
	}
}

func walEventMatches(ev fsnotify.Event, walBase string) bool {
	if filepath.Base(ev.Name) != walBase {
		return false
	}
	return ev.Op&(fsnotify.Write|fsnotify.Chmod|fsnotify.Create) != 0
}

func sendWALHint(hint chan<- struct{}) {
	select {
	case hint <- struct{}{}:
	default:
	}
}

// closedHintChan returns an already-closed hint channel, used when the WAL watch
// cannot be established so the tail loop falls back to pure timer polling.
func closedHintChan() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// resetTimer safely resets a timer to fire after d, draining any pending fire so
// the next select sees only the new deadline (the standard time.Timer reset
// idiom).
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}
