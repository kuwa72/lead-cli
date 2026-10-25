package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/state"
)

// issue #231: `lead peek <issue>` is the CLI counterpart of the inbox `p`
// key. It opens the recorded agent log with $PAGER.

// pagerDummy installs a fake pager that records its argv and echoes the
// file it was asked to open, so the test asserts the exact log path.
func pagerDummy(t *testing.T) (pager, argLog string) {
	t.Helper()
	bin := t.TempDir()
	pager = filepath.Join(bin, "pager")
	argLog = filepath.Join(bin, "argv.log")
	script := "#!/bin/sh\nfor x in \"$@\"; do printf '<%s>\\n' \"$x\" >> " + argLog + "; done\ncat \"$1\"\n"
	if err := os.WriteFile(pager, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return pager, argLog
}

func TestPeek_OpensRecordedLogWithPager(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "workflows.json")
	logPath := filepath.Join(dir, "issue-42.log")
	if err := os.WriteFile(logPath, []byte("agent output line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &state.Store{Path: stateFile}
	if err := store.Upsert(state.Workflow{Issue: 42, Branch: "issue/42-x", Status: state.StatusInProgress, LogPath: logPath}); err != nil {
		t.Fatal(err)
	}

	pager, argLog := pagerDummy(t)
	t.Setenv("PAGER", pager)

	out, err := executeWith(t, Deps{StateFile: stateFile, WorkDir: dir}, "peek", "42")
	if err != nil {
		t.Fatalf("peek 42: %v\n%s", err, out)
	}
	if !strings.Contains(out, "agent output line") {
		t.Errorf("peek did not show the log:\n%s", out)
	}
	raw, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatalf("pager was not invoked: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "<"+logPath+">" {
		t.Errorf("pager argv = %q, want <%s>", got, logPath)
	}
}

func TestPeek_MissingWorkflowFails(t *testing.T) {
	dir := t.TempDir()
	_, err := executeWith(t, Deps{StateFile: filepath.Join(dir, "workflows.json"), WorkDir: dir}, "peek", "99")
	if err == nil {
		t.Fatal("peek without a workflow = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("error = %q, want the issue number", err)
	}
}

func TestPeek_WorkflowWithoutLogFails(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "workflows.json")
	store := &state.Store{Path: stateFile}
	if err := store.Upsert(state.Workflow{Issue: 43, Branch: "issue/43-x", Status: state.StatusInProgress}); err != nil {
		t.Fatal(err)
	}
	_, err := executeWith(t, Deps{StateFile: stateFile, WorkDir: dir}, "peek", "43")
	if err == nil {
		t.Fatal("peek without a log = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "log") {
		t.Errorf("error = %q, want a log message", err)
	}
}

func TestPeek_MissingLogFileFails(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "workflows.json")
	store := &state.Store{Path: stateFile}
	if err := store.Upsert(state.Workflow{
		Issue: 44, Branch: "issue/44-x", Status: state.StatusInProgress,
		LogPath: filepath.Join(dir, "gone.log"),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := executeWith(t, Deps{StateFile: stateFile, WorkDir: dir}, "peek", "44")
	if err == nil {
		t.Fatal("peek with a missing log file = nil error, want failure")
	}
}
