package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrLocked means another lead process already dispatches this repository
// (issue #262). Callers use errors.Is to distinguish contention from real
// lock failures (permissions, missing state dir, ...).
var ErrLocked = errors.New("another lead is already dispatching this repository")

// LockInfo is the holder record written into the lock file. It is advisory:
// the flock is the authority, the content only tells `lead status` who holds
// it. A crashed holder leaves stale content behind but no flock, so the next
// AcquireLock succeeds and overwrites it.
type LockInfo struct {
	PID       int       `json:"pid"`
	Repo      string    `json:"repo"`
	StartedAt time.Time `json:"started_at"`
}

// RepoLock is an exclusive, repository-scoped dispatch lock. The underlying
// flock is released automatically when the process exits, so a killed lead
// never leaves a lock that blocks the next run.
type RepoLock struct {
	path string
	file *os.File
}

// LockPath is <state dir>/locks/<repo-key>.lock. The key is sanitized so a
// GitHub slug ("owner/repo") stays a single path element.
func LockPath(stateDir, repoKey string) string {
	return filepath.Join(stateDir, "locks", sanitizeLockKey(repoKey)+".lock")
}

func sanitizeLockKey(key string) string {
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "local"
	}
	return b.String()
}

// AcquireLock takes the repository's dispatch lock without blocking. On
// contention it returns an error wrapping ErrLocked; the caller decides
// whether to exit (`lead dispatch`) or disable dispatch and warn (inbox).
func AcquireLock(stateDir, repoKey string) (*RepoLock, error) {
	path := LockPath(stateDir, repoKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("dispatch lock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("dispatch lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if info, held := LockHolder(stateDir, repoKey); held && info.PID > 0 {
				return nil, fmt.Errorf("%w (pid %d)", ErrLocked, info.PID)
			}
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("dispatch lock %s: %w", path, err)
	}
	info := LockInfo{PID: os.Getpid(), Repo: repoKey, StartedAt: time.Now().UTC()}
	if err := writeLockInfo(f, info); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, fmt.Errorf("dispatch lock %s: %w", path, err)
	}
	return &RepoLock{path: path, file: f}, nil
}

func writeLockInfo(f *os.File, info LockInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// Release drops the lock. The file stays in place: unlinking a flock file
// races with a concurrent AcquireLock (both sides could end up holding a
// lock on different inodes), and a leftover file with no flock is harmless.
func (l *RepoLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	closeErr := f.Close()
	return errors.Join(unlockErr, closeErr)
}

// Path is the lock file location.
func (l *RepoLock) Path() string { return l.path }

// LockHolder reports the current holder of the repository lock. It probes
// with a non-blocking flock: success means nobody holds it (a stale file is
// not a holder), failure means the file is locked and its content names the
// process. Missing or unreadable files report no holder.
func LockHolder(stateDir, repoKey string) (LockInfo, bool) {
	path := LockPath(stateDir, repoKey)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return LockInfo{}, false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return LockInfo{}, false
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return LockInfo{}, true
		}
		var info LockInfo
		if err := json.Unmarshal(data, &info); err != nil {
			return LockInfo{}, true
		}
		return info, true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return LockInfo{}, false
}
