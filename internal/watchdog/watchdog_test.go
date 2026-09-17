package watchdog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStuckReason(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	touch := func(t *testing.T, mtime time.Time) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "agent.log")
		if err := os.WriteFile(p, []byte("out\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fresh := touch(t, now.Add(-time.Minute))
	stale := touch(t, now.Add(-time.Hour))
	cases := []struct {
		name    string
		log     string
		started time.Time
		idle    time.Duration
		maxRun  time.Duration
		stuck   bool
	}{
		{"active output", fresh, now.Add(-time.Hour), time.Minute * 30, time.Hour * 3, false},
		{"idle too long", stale, now.Add(-time.Hour), time.Minute * 30, time.Hour * 3, true},
		{"idle check disabled", stale, now.Add(-time.Hour), 0, time.Hour * 3, false},
		{"runtime exceeded", fresh, now.Add(-4 * time.Hour), time.Minute * 30, time.Hour * 3, true},
		{"runtime check disabled", fresh, now.Add(-4 * time.Hour), time.Minute * 30, 0, false},
		{"nothing known", "", time.Time{}, time.Minute * 30, time.Hour * 3, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := StuckReason(c.log, c.started, now, c.idle, c.maxRun)
			if (err != nil) != c.stuck {
				t.Errorf("StuckReason() = %v, want stuck=%v", err, c.stuck)
			}
		})
	}
}

func TestStuckReason_MessageMentionsDuration(t *testing.T) {
	now := time.Now()
	err := StuckReason("", now.Add(-time.Hour), now, 30*time.Minute, 0)
	if err == nil || !strings.Contains(err.Error(), "no output") {
		t.Fatalf("err = %v, want a no-output reason", err)
	}
}

func TestStalledError_UnwrapsReason(t *testing.T) {
	reason := errors.New("agent produced no output for 30m0s")
	err := &StalledError{Reason: reason}
	if !errors.Is(err, reason) {
		t.Errorf("StalledError does not unwrap: %v", err)
	}
	if !strings.Contains(err.Error(), "no output") {
		t.Errorf("Error() = %q, want the reason text", err.Error())
	}
}

func TestKillProcessGroup_GoneIsSuccess(t *testing.T) {
	// A pid that almost certainly does not exist: ESRCH counts as gone.
	if err := KillProcessGroup(1 << 30); err != nil {
		t.Errorf("KillProcessGroup on dead pid = %v, want nil", err)
	}
}
