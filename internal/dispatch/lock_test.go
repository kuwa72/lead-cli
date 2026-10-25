package dispatch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// issue #262: repository-scoped dispatch lock.

func TestLockPath_SanitizesRepoKey(t *testing.T) {
	got := LockPath("/state", "owner/repo")
	want := filepath.Join("/state", "locks", "owner-repo.lock")
	if got != want {
		t.Errorf("LockPath = %q, want %q", got, want)
	}
}

func TestAcquireLock_SecondHolderIsRefusedAndReleased(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireLock(dir, "o/r")
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	if _, err := AcquireLock(dir, "o/r"); !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireLock = %v, want ErrLocked", err)
	}

	info, held := LockHolder(dir, "o/r")
	if !held {
		t.Fatal("LockHolder = free while the lock is held")
	}
	if info.PID != os.Getpid() {
		t.Errorf("holder pid = %d, want %d", info.PID, os.Getpid())
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, held := LockHolder(dir, "o/r"); held {
		t.Fatal("LockHolder = held after Release")
	}
	second, err := AcquireLock(dir, "o/r")
	if err != nil {
		t.Fatalf("AcquireLock after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestAcquireLock_StaleFileDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	path := LockPath(dir, "o/r")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A crashed holder leaves content but no flock.
	if err := os.WriteFile(path, []byte(`{"pid":999999,"repo":"o/r"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, held := LockHolder(dir, "o/r"); held {
		t.Fatal("stale lock file reported as held")
	}
	lock, err := AcquireLock(dir, "o/r")
	if err != nil {
		t.Fatalf("AcquireLock with stale file: %v", err)
	}
	defer lock.Release()
	info, held := LockHolder(dir, "o/r")
	if !held || info.PID != os.Getpid() {
		t.Errorf("holder after acquire = %+v, held=%v; want current pid", info, held)
	}
}
