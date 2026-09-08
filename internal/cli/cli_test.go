package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

func execute(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd("v0.0.0-test", "abc1234", "2026-09-07")
	var outBuf, errBuf strings.Builder
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestEachCommandHelpRenders(t *testing.T) {
	for _, cmd := range []string{"version", "work", "status", "clean", "finish", "server", "api", "setup", "completion", "doctor", "update", "init"} {
		stdout, _, err := execute(t, cmd, "--help")
		if err != nil {
			t.Errorf("lead %s --help: %v, want exit 0", cmd, err)
			continue
		}
		if !strings.Contains(stdout, "lead "+cmd) {
			t.Errorf("lead %s --help output missing command name, got:\n%s", cmd, stdout)
		}
	}
}

func TestRootHelpListsCommands(t *testing.T) {
	stdout, _, err := execute(t, "--help")
	if err != nil {
		t.Fatalf("lead --help: %v, want exit 0", err)
	}
	for _, cmd := range []string{"work", "status", "clean", "finish", "server", "api", "setup", "completion", "doctor", "update", "init", "version"} {
		if !strings.Contains(stdout, cmd) {
			t.Errorf("lead --help missing %q, got:\n%s", cmd, stdout)
		}
	}
}

func TestUnknownCommandFails(t *testing.T) {
	_, _, err := execute(t, "no-such-command")
	if err == nil {
		t.Fatal("lead no-such-command = nil error, want non-zero exit")
	}
}

// Bare `lead` opens the inbox TUI (issue #91). Under `go test` stdin is not a
// terminal, so it must refuse with a hint instead of printing help or hanging.
func TestBareLeadWithoutTTYFailsWithHelpHint(t *testing.T) {
	t.Setenv("LEAD_TEST_INBOX_KEYS", "")
	stdout, _, err := execute(t)
	if err == nil {
		t.Fatal("bare lead on non-TTY = nil error, want non-zero exit")
	}
	if !strings.Contains(err.Error(), "--help") {
		t.Errorf("error should point at --help, got %q", err)
	}
	if strings.Contains(stdout, "Available Commands") {
		t.Errorf("bare lead must not print help anymore, got:\n%s", stdout)
	}
}

// LEAD_TEST_INBOX_KEYS drives the inbox headlessly; `a` on the first
// needs-review issue must swap labels through the injected gh client.
func TestBareLeadHeadlessKeysReachGh(t *testing.T) {
	t.Setenv("LEAD_TEST_INBOX_KEYS", "a,q")
	gh := &testutil.FakeGhClient{Labeled: map[string][]ports.IssueSummary{
		"needs-review": {{Number: 7, Title: "spec"}},
	}}
	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", Deps{Gh: gh, WorkDir: t.TempDir()})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(nil)
	if err := root.Execute(); err != nil {
		t.Fatalf("headless inbox: %v", err)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: "needs-review"}}; !reflect.DeepEqual(gh.RemovedLabels, want) {
		t.Errorf("RemovedLabels = %+v, want %+v", gh.RemovedLabels, want)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: "ready"}}; !reflect.DeepEqual(gh.AddedLabels, want) {
		t.Errorf("AddedLabels = %+v, want %+v", gh.AddedLabels, want)
	}
	if !strings.Contains(out.String(), "#7") {
		t.Errorf("headless output should render the inbox, got:\n%s", out.String())
	}
}

func TestUnknownFlagFails(t *testing.T) {
	_, _, err := execute(t, "work", "--bogus-flag")
	if err == nil {
		t.Fatal("lead work --bogus-flag = nil error, want flag parse failure")
	}
}

func TestVersionFlagMatchesSubcommand(t *testing.T) {
	sub, _, err := execute(t, "version")
	if err != nil {
		t.Fatalf("lead version: %v", err)
	}
	for _, args := range [][]string{{"--version"}, {"-v"}} {
		got, _, err := execute(t, args...)
		if err != nil {
			t.Fatalf("lead %v: %v", args, err)
		}
		if got != sub {
			t.Errorf("lead %v = %q, want same as `lead version` %q", args, got, sub)
		}
	}
}

func TestVersionPrintsStampedFields(t *testing.T) {
	stdout, _, err := execute(t, "version")
	if err != nil {
		t.Fatalf("lead version: %v", err)
	}
	for _, want := range []string{"lead", "v0.0.0-test", "abc1234", "2026-09-07"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("lead version output missing %q, got %q", want, stdout)
		}
	}
}

// NOTE: bare `lead work` (picker path) is covered by tui_test.go with
// FakeSelector plus test/test_tui_picker.sh via LEAD_TEST_SELECTION.
// It is intentionally not exercised here with production defaults:
// that would list real issues and touch the TTY.

// Flags are parsed without executing the command: running `work 36` with
// production defaults reached the real gh, git checkout and Herdr session
// (issue #96). TestMain additionally guards against a regression.
func TestWorkAcceptsDocumentedFlags(t *testing.T) {
	root := NewRootCmd("v0.0.0-test", "abc1234", "2026-09-07")
	work, _, err := root.Find([]string{"work"})
	if err != nil || work == nil {
		t.Fatalf("work command not found: %v", err)
	}
	if err := work.ParseFlags([]string{"36", "--mode", "plan", "--draft", "--agent", "devin"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if got, _ := work.Flags().GetString("mode"); got != "plan" {
		t.Errorf("--mode = %q, want plan", got)
	}
	if got, _ := work.Flags().GetBool("draft"); !got {
		t.Error("--draft not set, want true")
	}
	if got, _ := work.Flags().GetString("agent"); got != "devin" {
		t.Errorf("--agent = %q, want devin", got)
	}
}

func TestCompletionGeneratesScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		stdout, _, err := execute(t, "completion", shell)
		if err != nil {
			t.Errorf("lead completion %s: %v", shell, err)
			continue
		}
		if len(stdout) == 0 {
			t.Errorf("lead completion %s output empty", shell)
		}
	}
	if _, _, err := execute(t, "completion", "csh"); err == nil {
		t.Error("lead completion csh = nil error, want unsupported-shell failure")
	}
}
