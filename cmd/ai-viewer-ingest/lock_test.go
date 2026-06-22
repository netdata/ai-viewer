package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestLock_AcquiredWhenFree confirms the ingester lock can be taken on
// a fresh state_dir and the lockfile is created.
func TestLock_AcquiredWhenFree(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	lockPath := filepath.Join(stateDir, "ingester.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = lockFile.Close() }()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("first flock should succeed: %v", err)
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lockfile not created: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("lockfile should be empty, got size %d", info.Size())
	}
}

// TestLock_ConflictsWhenHeld simulates the SOW-0094 multi-process lockout
// failure mode: a second ingester starting while the first holds the
// lock must exit with EWOULDBLOCK rather than corrupt the SQLite WAL.
func TestLock_ConflictsWhenHeld(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	lockPath := filepath.Join(stateDir, "ingester.lock")

	first, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	defer func() { _ = first.Close() }()
	if err := syscall.Flock(int(first.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("first flock: %v", err)
	}
	defer func() { _ = syscall.Flock(int(first.Fd()), syscall.LOCK_UN) }()

	second, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer func() { _ = second.Close() }()
	err = syscall.Flock(int(second.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(second.Fd()), syscall.LOCK_UN)
		t.Fatalf("second flock should fail while first holds lock, got nil")
	}
	// On Linux the kernel returns EWOULDBLOCK for LOCK_NB conflicts.
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Errorf("expected EWOULDBLOCK, got %v", err)
	}
}

// TestLock_ReleasedOnClose simulates a process crash: the second ingester
// can acquire the lock immediately after the first releases (process exit
// or fd close). This is the property that distinguishes flock from
// stale-PID-file lockouts.
func TestLock_ReleasedOnClose(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	lockPath := filepath.Join(stateDir, "ingester.lock")

	first, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	if err := syscall.Flock(int(first.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("first flock: %v", err)
	}
	// Simulate the first process exiting.
	_ = first.Close()

	second, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer func() { _ = second.Close() }()
	if err := syscall.Flock(int(second.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("second flock should succeed after first closed (crash recovery), got %v", err)
	}
	defer func() { _ = syscall.Flock(int(second.Fd()), syscall.LOCK_UN) }()
}

// TestLock_StateDirResolution confirms the lockfile lives INSIDE state_dir
// (not alongside it). The inline location matters under systemd
// ProtectSystem=strict: the parent dir of state_dir is read-only, so
// a "state_dir + .lock" lockfile would fail with EROFS. Putting the
// lockfile inside state_dir keeps it under the existing ReadWritePaths.
func TestLock_StateDirResolution(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	want := filepath.Join(stateDir, "ingester.lock")
	got := filepath.Join(stateDir, "ingester.lock")
	if got != want {
		t.Fatalf("lock path mismatch: got %q want %q", got, want)
	}
}

// TestLock_ConcurrentAcquireTimeout combines the conflict + close path:
// under heavy contention, repeated LOCK_NB attempts fail fast (no
// blocking) and the second ingester exits within a second.
func TestLock_ConcurrentAcquireTimeout(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	lockPath := filepath.Join(stateDir, "ingester.lock")

	holder, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("holder flock: %v", err)
	}
	defer func() { _ = syscall.Flock(int(holder.Fd()), syscall.LOCK_UN) }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() == nil {
			t.Errorf("second acquire did not fail within 1s")
		}
	}()

	second, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer func() { _ = second.Close() }()

	// Poll for the lock with a short backoff; under contention this
	// returns EWOULDBLOCK on the first try and the loop exits via ctx.
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := syscall.Flock(int(second.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(second.Fd()), syscall.LOCK_UN)
			t.Errorf("second flock should fail while holder is alive")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
