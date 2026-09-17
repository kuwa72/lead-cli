// Package watchdog supervises headless agent processes (issue #186 model,
// extended to every headless launch path by issue #215): an agent whose log
// has been idle past the stall timeout, or whose run exceeds the wall-clock
// cap, is treated as failed and its process group is killed.
//
// The two checks are shared by `lead dispatch` (which re-evaluates
// reconnected agents via StuckReason) and `lead say` (whose ExecRunner
// supervises its own child). Both read the thresholds from the shared
// inbox-config.json settings (stall_timeout / max_runtime).
package watchdog

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Defaults mirror the dispatch thresholds introduced in issue #186. Silent
// stretches cover legitimate CI waits, so the stall default is generous.
const (
	DefaultStallTimeout = 30 * time.Minute
	DefaultMaxRuntime   = 3 * time.Hour
)

// StalledError marks a kill caused by the watchdog (stall or wall-clock
// cap) rather than the agent exiting on its own. Spec runs notify on it.
type StalledError struct {
	Reason error
}

func (e *StalledError) Error() string { return "stalled: " + e.Reason.Error() }
func (e *StalledError) Unwrap() error { return e.Reason }

// StuckReason returns non-nil when a running agent should be treated as
// failed: past maxRuntime, or silent longer than stallTimeout (issue #186).
// Non-positive timeouts disable their check. Zero startedAt with no log
// means nothing is known: never stuck.
func StuckReason(logPath string, startedAt, now time.Time, stallTimeout, maxRuntime time.Duration) error {
	if maxRuntime > 0 && !startedAt.IsZero() && now.Sub(startedAt) > maxRuntime {
		return fmt.Errorf("agent exceeded maximum runtime of %s", maxRuntime)
	}
	if stallTimeout <= 0 {
		return nil
	}
	last := startedAt
	if logPath != "" {
		if fi, err := os.Stat(logPath); err == nil && fi.ModTime().After(last) {
			last = fi.ModTime()
		}
	}
	if last.IsZero() || now.Sub(last) <= stallTimeout {
		return nil
	}
	return fmt.Errorf("agent produced no output for %s", now.Sub(last).Round(time.Second))
}

// KillProcessGroup SIGKILLs the process group (agents start in their own
// group via Setpgid) and falls back to the single pid. ESRCH anywhere
// means already gone, which counts as success.
func KillProcessGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// PollInterval picks the watchdog tick: a tenth of the tightest active
// limit, clamped so short test timeouts still fire promptly and long ones
// do not spin.
func PollInterval(stallTimeout, maxRuntime time.Duration) time.Duration {
	limit := time.Duration(0)
	for _, d := range []time.Duration{stallTimeout, maxRuntime} {
		if d > 0 && (limit == 0 || d < limit) {
			limit = d
		}
	}
	p := limit / 10
	if p < 50*time.Millisecond {
		p = 50 * time.Millisecond
	}
	if p > 30*time.Second {
		p = 30 * time.Second
	}
	return p
}
