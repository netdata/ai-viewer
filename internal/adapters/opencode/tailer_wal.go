package opencode

import (
	"fmt"
	"path/filepath"
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
	go func() {
		defer close(hint)
		for {
			select {
			case <-done:
				return
			case ev, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != walBase {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Chmod|fsnotify.Create) == 0 {
					continue
				}
				// Non-blocking notify: a pending hint already wakes the next poll.
				select {
				case hint <- struct{}{}:
				default:
				}
			case werr, ok := <-watcher.Errors:
				if !ok {
					return
				}
				// A watcher error never terminates the tail loop; report and
				// keep the watch (or let the timer net carry it).
				onError(fmt.Errorf("opencode: wal watcher error: %w", werr))
			}
		}
	}()

	closeWatch := func() {
		close(done)
		_ = watcher.Close()
	}
	return hint, closeWatch
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
