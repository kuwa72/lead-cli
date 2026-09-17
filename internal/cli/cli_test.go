package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/projinit"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
	"github.com/kuwa72/lead-cli/internal/workflow"
)

// fakeGitRunner implements workflow.GitRunner for cli tests.
type fakeGitRunner struct {
	root   string
	origin string
}

func (g *fakeGitRunner) RepoRoot(dir string) (string, error)       { return g.root, nil }
func (g *fakeGitRunner) CreateBranch(repoDir, branch string) error { return nil }
func (g *fakeGitRunner) WorktreeAdd(repoDir, path, branch string) error {
	return os.MkdirAll(path, 0o755)
}
func (g *fakeGitRunner) WorktreeRemove(repoDir, path string, force bool) error { return nil }
func (g *fakeGitRunner) OriginURL(repoDir string) string                       { return g.origin }
func (g *fakeGitRunner) BranchExists(repoDir, branch string) (bool, error)     { return true, nil }
func (g *fakeGitRunner) WorktreeList(repoDir string) ([]git.WorktreeInfo, error) {
	return nil, nil
}
func (g *fakeGitRunner) ListBranches(repoDir string) ([]string, error) {
	return nil, nil
}
func (g *fakeGitRunner) CheckoutBranch(repoDir, branch string) error { return nil }

var _ workflow.GitRunner = (*fakeGitRunner)(nil)

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
	for _, cmd := range []string{"version", "work", "status", "clean", "finish", "server", "api", "setup", "completion", "doctor", "update", "enable", "disable"} {
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
	for _, cmd := range []string{"work", "status", "clean", "finish", "server", "api", "setup", "completion", "doctor", "update", "enable", "version"} {
		if !strings.Contains(stdout, cmd) {
			t.Errorf("lead --help missing %q, got:\n%s", cmd, stdout)
		}
	}
}

func TestInitAliasIsGone(t *testing.T) {
	if _, _, err := execute(t, "init", "--help"); err == nil {
		t.Error("lead init --help = nil error, want unknown-command failure")
	}
}

func TestInboxSettingsDisablesNotifications(t *testing.T) {
	t.Setenv("LEAD_TEST_INBOX_KEYS", "S,j,j,j,enter,q,q")
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	t.Setenv("LEAD_STATE_FILE", stateFile)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(stateFile), "inbox-seen.json"), []byte(`{"help_shown":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	gh := &testutil.FakeGhClient{}
	root := t.TempDir()
	deps := Deps{
		Gh:      gh,
		Git:     &fakeGitRunner{root: root, origin: "https://github.com/o/r.git"},
		WorkDir: root,
	}
	if out, err := runLeadCmd(t, deps); err != nil {
		t.Fatalf("headless inbox: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(stateFile), "inbox-config.json"))
	if err != nil {
		t.Fatalf("inbox config not written: %v", err)
	}
	if !strings.Contains(string(raw), `"notify_disabled": true`) {
		t.Errorf("config missing notify_disabled, got:\n%s", raw)
	}
}

func runningHeadlessDeps(t *testing.T, startedAgo time.Duration, logAge time.Duration) Deps {
	t.Helper()
	t.Setenv("LEAD_TEST_INBOX_KEYS", "j,j,j,j,enter,j,q")
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	t.Setenv("LEAD_STATE_FILE", stateFile)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(stateFile), "inbox-seen.json"), []byte(`{"help_shown":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	gh := &testutil.FakeGhClient{Issues: map[int]ports.Issue{
		7: {Number: 7, Title: "work", Body: "body", State: "OPEN"},
	}}
	logPath := filepath.Join(t.TempDir(), "issue-7.log")
	if err := os.WriteFile(logPath, []byte("working\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-logAge)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}
	store := &state.Store{Path: stateFile}
	rec := state.Workflow{Issue: 7, Repository: "o/r", Branch: "issue/7", Status: state.StatusInProgress, Agent: "agy", PID: 1 << 30, LogPath: logPath, StartedAt: time.Now().Add(-startedAgo)}
	if err := store.Upsert(rec); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return Deps{
		Gh:      gh,
		Git:     &fakeGitRunner{root: root, origin: "https://github.com/o/r.git"},
		WorkDir: root,
	}
}

func TestInboxRunningRowShowsActivity(t *testing.T) {
	out, err := runLeadCmd(t, runningHeadlessDeps(t, 90*time.Minute, 5*time.Minute))
	if err != nil {
		t.Fatalf("headless inbox: %v\n%s", err, out)
	}
	// Narrow layout has no preview pane: the Updated column carries activity.
	if !strings.Contains(out, "5m") {
		t.Errorf("running row missing activity age, got:\n%s", out)
	}
}

func TestInboxRunningPreviewShowsActivity(t *testing.T) {
	t.Setenv("LEAD_TEST_INBOX_WIDTH", "200")
	out, err := runLeadCmd(t, runningHeadlessDeps(t, 90*time.Minute, 5*time.Minute))
	if err != nil {
		t.Fatalf("headless inbox: %v\n%s", err, out)
	}
	for _, want := range []string{"Elapsed: 1h", "Active: 5m ago", "Phase: implementing"} {
		if !strings.Contains(out, want) {
			t.Errorf("running preview missing %q, got:\n%s", want, out)
		}
	}
}

func TestInboxStopKeyStopsRunningAgent(t *testing.T) {
	t.Setenv("LEAD_TEST_INBOX_KEYS", "j,j,j,j,enter,j,d,q")
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	t.Setenv("LEAD_STATE_FILE", stateFile)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(stateFile), "inbox-seen.json"), []byte(`{"help_shown":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	gh := &testutil.FakeGhClient{Issues: map[int]ports.Issue{
		7: {Number: 7, Title: "work", Body: "body", State: "OPEN"},
	}}
	// setsid puts the stand-in agent in its own process group so Stop's
	// group kill takes the deterministic path (CI flakes observed with
	// plain sleep inheriting the test's group).
	sleeper := exec.Command("setsid", "sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Skip("setsid/sleep unavailable")
	}
	t.Cleanup(func() { sleeper.Process.Kill(); sleeper.Wait() })
	store := &state.Store{Path: stateFile}
	if err := store.Upsert(state.Workflow{Issue: 7, Repository: "o/r", Branch: "issue/7", Status: state.StatusInProgress, Agent: "agy", PID: sleeper.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	deps := Deps{
		Gh:      gh,
		Git:     &fakeGitRunner{root: root, origin: "https://github.com/o/r.git"},
		WorkDir: root,
	}
	out, err := runLeadCmd(t, deps)
	if err != nil {
		t.Fatalf("headless inbox: %v\n%s", err, out)
	}
	waitAgentGone(t, sleeper.Process.Pid)
	sleeper.Wait()
	found := false
	for _, c := range gh.Comments {
		if c.Number == 7 {
			found = true
		}
	}
	if !found {
		t.Errorf("no interruption comment on #7: %+v", gh.Comments)
	}
	if _, ok, _ := store.Get(7, ""); ok {
		t.Error("state record not deleted by d key")
	}
}

func TestStopRemovesRecordAndComments(t *testing.T) {
	gh := &testutil.FakeGhClient{Issues: map[int]ports.Issue{
		7: {Number: 7, Title: "w", Body: "b", State: "OPEN"},
	}}
	root := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	store := &state.Store{Path: stateFile}
	// Stale record with a dead pid exercises the gone-path cleanup.
	if err := store.Upsert(state.Workflow{Issue: 7, Repository: "o/r", Branch: "issue/7", Status: state.StatusInProgress, PID: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Gh:        gh,
		Git:       &fakeGitRunner{root: root, origin: "https://github.com/o/r.git"},
		WorkDir:   root,
		StateFile: stateFile,
	}
	out, err := runLeadCmd(t, deps, "stop", "7")
	if err != nil {
		t.Fatalf("lead stop 7: %v\n%s", err, out)
	}
	if !strings.Contains(out, "stopped #7") {
		t.Errorf("stop missing confirmation, got:\n%s", out)
	}
	if len(gh.Comments) != 1 || gh.Comments[0].Number != 7 {
		t.Errorf("Comments = %+v, want one interruption comment on #7", gh.Comments)
	}
	if _, ok, _ := store.Get(7, ""); ok {
		t.Error("state record not deleted by stop")
	}
}

func TestStopWithoutRecordFails(t *testing.T) {
	gh := &testutil.FakeGhClient{}
	root := t.TempDir()
	deps := Deps{
		Gh:        gh,
		Git:       &fakeGitRunner{root: root, origin: "https://github.com/o/r.git"},
		WorkDir:   root,
		StateFile: filepath.Join(t.TempDir(), "workflows.json"),
	}
	if _, err := runLeadCmd(t, deps, "stop", "7"); err == nil {
		t.Error("lead stop without a record = nil error, want failure")
	}
	if _, err := runLeadCmd(t, deps, "stop", "bogus"); err == nil {
		t.Error("lead stop bogus = nil error, want failure")
	}
}

func inboxHeadlessDeps(t *testing.T, gh *testutil.FakeGhClient, root, origin string) Deps {
	t.Helper()
	t.Setenv("LEAD_TEST_INBOX_KEYS", "q")
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	t.Setenv("LEAD_STATE_FILE", stateFile)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(stateFile), "inbox-seen.json"), []byte(`{"help_shown":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return Deps{
		Gh:      gh,
		Git:     &fakeGitRunner{root: root, origin: origin},
		WorkDir: root,
	}
}

func TestInboxShowsEnableGuidanceWhenNotEnabled(t *testing.T) {
	gh := &testutil.FakeGhClient{}
	root := t.TempDir()
	out, err := runLeadCmd(t, inboxHeadlessDeps(t, gh, root, "https://github.com/o/r.git"))
	if err != nil {
		t.Fatalf("headless inbox: %v\n%s", err, out)
	}
	if !strings.Contains(out, "lead enable") {
		t.Errorf("inbox in unconfigured repo missing enable guidance, got:\n%s", out)
	}
}

func TestInboxHidesEnableGuidanceWhenEnabled(t *testing.T) {
	gh := &testutil.FakeGhClient{}
	root := t.TempDir()
	block := projinit.BlockStart + "\ntest block\n" + projinit.BlockEnd + "\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# rules\n"+block), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runLeadCmd(t, inboxHeadlessDeps(t, gh, root, "https://github.com/o/r.git"))
	if err != nil {
		t.Fatalf("headless inbox: %v\n%s", err, out)
	}
	if strings.Contains(out, "lead enable") {
		t.Errorf("inbox in configured repo shows enable guidance, got:\n%s", out)
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
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	t.Setenv("LEAD_STATE_FILE", stateFile)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(stateFile), "inbox-seen.json"), []byte(`{"help_shown":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	gh := &testutil.FakeGhClient{
		Labeled: map[string][]ports.IssueSummary{
			"needs-review": {{Number: 7, Title: "spec"}},
		},
		Issues: map[int]ports.Issue{
			7: {Number: 7, Title: "spec", Body: "## Acceptance\n- [ ] task", State: "OPEN"},
		},
	}
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

func TestBareLeadWithParallelAndNoTTYFails(t *testing.T) {
	t.Setenv("LEAD_TEST_INBOX_KEYS", "")
	_, _, err := execute(t, "--parallel", "3")
	if err == nil {
		t.Fatal("bare lead --parallel 3 on non-TTY = nil error, want non-zero exit")
	}
	if !strings.Contains(err.Error(), "--help") {
		t.Errorf("error should point at --help, got %q", err)
	}
}

func TestBareLeadParallelFlagIsWired(t *testing.T) {
	t.Setenv("LEAD_TEST_INBOX_KEYS", "R,q")
	gh := &testutil.FakeGhClient{
		Labeled: map[string][]ports.IssueSummary{
			"needs-review": {{Number: 7, Title: "spec"}},
		},
	}
	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", Deps{
		Gh:      gh,
		Git:     &fakeGitRunner{root: t.TempDir(), origin: "git@github.com:acme/widgets.git"},
		WorkDir: t.TempDir(),
	})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--parallel", "3"})
	if err := root.Execute(); err != nil {
		t.Fatalf("headless lead --parallel 3: %v", err)
	}
	if got, _ := root.Flags().GetInt("parallel"); got != 3 {
		t.Errorf("--parallel flag = %d, want 3", got)
	}
	if !strings.Contains(out.String(), "parallel 3") {
		t.Errorf("inbox header should show parallel limit 3, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Running 0") {
		t.Errorf("inbox header should show running count, got:\n%s", out.String())
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

// enableFixture builds Deps whose repository root is a temp dir and whose
// origin resolves to origin ("" = no remote).
func enableFixture(t *testing.T, gh *testutil.FakeGhClient, origin string) Deps {
	t.Helper()
	root := t.TempDir()
	return Deps{
		Gh:      gh,
		Git:     &fakeGitRunner{root: root, origin: origin},
		WorkDir: root,
	}
}

func runEnableCmd(t *testing.T, deps Deps, args ...string) (string, error) {
	t.Helper()
	return runLeadCmd(t, deps, append([]string{"enable"}, args...)...)
}

func runDisableCmd(t *testing.T, deps Deps, args ...string) (string, error) {
	t.Helper()
	return runLeadCmd(t, deps, append([]string{"disable"}, args...)...)
}

func runLeadCmd(t *testing.T, deps Deps, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", deps)
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestEnableCheckFailsWhenLabelsMissing(t *testing.T) {
	gh := &testutil.FakeGhClient{RepoLabelNames: []string{"needs-review"}}
	deps := enableFixture(t, gh, "https://github.com/o/r.git")
	if out, err := runEnableCmd(t, deps, "--yes"); err != nil {
		t.Fatalf("enable --yes: %v\n%s", err, out)
	}
	out, err := runEnableCmd(t, deps, "--check")
	if err == nil {
		t.Fatalf("enable --check with missing labels = nil error, want non-zero exit\n%s", out)
	}
	for _, want := range []string{"label/needs-review: ok", "label/ready: missing", "label/blocked: missing"} {
		if !strings.Contains(out, want) {
			t.Errorf("enable --check missing %q, got:\n%s", want, out)
		}
	}
	if !strings.Contains(err.Error(), "labels") {
		t.Errorf("enable --check error = %q, want label guidance", err)
	}
	if len(gh.RepoLabelsCalls) == 0 || gh.RepoLabelsCalls[0] != "o/r" {
		t.Errorf("RepoLabelsCalls = %v, want [o/r ...]", gh.RepoLabelsCalls)
	}
}

func TestEnableCheckPassesWhenLabelsPresent(t *testing.T) {
	gh := &testutil.FakeGhClient{
		RepoLabelNames:   []string{"needs-review", "ready", "blocked"},
		Protection:       ports.BranchProtection{Protected: true, RequiresPR: true, RequiredChecks: []string{"test"}},
		AutoMergeAllowed: true,
	}
	deps := enableFixture(t, gh, "https://github.com/o/r.git")
	if out, err := runEnableCmd(t, deps, "--yes"); err != nil {
		t.Fatalf("enable --yes: %v\n%s", err, out)
	}
	out, err := runEnableCmd(t, deps, "--check")
	if err != nil {
		t.Fatalf("enable --check with all labels: %v\n%s", err, out)
	}
	for _, want := range []string{"label/needs-review: ok", "label/ready: ok", "label/blocked: ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("enable --check missing %q, got:\n%s", want, out)
		}
	}
	for _, want := range []string{"protection/branch-protection: ok", "protection/required-checks: ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("enable --check missing %q, got:\n%s", want, out)
		}
	}
}

func TestEnableCheckPassesWithoutProtection(t *testing.T) {
	// Issue #200: labels present but no branch protection: --check must
	// pass with protection guidance since missing protection only warns.
	gh := &testutil.FakeGhClient{RepoLabelNames: []string{"needs-review", "ready", "blocked"}}
	deps := enableFixture(t, gh, "https://github.com/o/r.git")
	if out, err := runEnableCmd(t, deps, "--yes"); err != nil {
		t.Fatalf("enable --yes: %v\n%s", err, out)
	}
	out, err := runEnableCmd(t, deps, "--check")
	if err != nil {
		t.Fatalf("enable --check without protection = %v, want nil\n%s", err, out)
	}
	for _, want := range []string{"protection/branch-protection: missing", "protection/required-checks: missing", "Next:"} {
		if !strings.Contains(out, want) {
			t.Errorf("enable --check missing %q, got:\n%s", want, out)
		}
	}
}

func TestEnableCheckSkipsLabelsWithoutRemote(t *testing.T) {
	gh := &testutil.FakeGhClient{}
	deps := enableFixture(t, gh, "")
	if out, err := runEnableCmd(t, deps, "--yes"); err != nil {
		t.Fatalf("enable --yes: %v\n%s", err, out)
	}
	out, err := runEnableCmd(t, deps, "--check")
	if err != nil {
		t.Fatalf("enable --check without remote: %v\n%s", err, out)
	}
	if !strings.Contains(out, "skip") {
		t.Errorf("enable --check without remote missing skip notice, got:\n%s", out)
	}
	if len(gh.RepoLabelsCalls) != 0 {
		t.Errorf("RepoLabelsCalls = %v, want no label listing without a remote", gh.RepoLabelsCalls)
	}
}

func TestEnableApplyCreatesMissingLabels(t *testing.T) {
	gh := &testutil.FakeGhClient{RepoLabelNames: []string{"needs-review"}}
	deps := enableFixture(t, gh, "https://github.com/o/r.git")
	out, err := runEnableCmd(t, deps, "--yes")
	if err != nil {
		t.Fatalf("enable --yes: %v\n%s", err, out)
	}
	var got []string
	for _, l := range gh.CreatedLabels {
		got = append(got, l.Name)
	}
	want := []string{"ready", "blocked"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("created labels = %v, want %v (present labels must not be recreated)", got, want)
	}
	for _, wantLine := range []string{"label/ready: created", "label/blocked: created", "label/needs-review: ok"} {
		if !strings.Contains(out, wantLine) {
			t.Errorf("enable --yes missing %q, got:\n%s", wantLine, out)
		}
	}
}

func TestEnableDryRunDoesNotCreateLabels(t *testing.T) {
	gh := &testutil.FakeGhClient{RepoLabelNames: []string{}}
	deps := enableFixture(t, gh, "https://github.com/o/r.git")
	out, err := runEnableCmd(t, deps, "--dry-run")
	if err != nil {
		t.Fatalf("enable dry-run: %v\n%s", err, out)
	}
	if len(gh.CreatedLabels) != 0 {
		t.Errorf("dry-run created labels %v, want no creation without --write", gh.CreatedLabels)
	}
	if !strings.Contains(out, "label/ready: missing") {
		t.Errorf("dry-run missing label/ready missing line, got:\n%s", out)
	}
}

func TestDisableRemovesManagedFiles(t *testing.T) {
	gh := &testutil.FakeGhClient{RepoLabelNames: []string{"needs-review", "ready", "blocked"}}
	deps := enableFixture(t, gh, "https://github.com/o/r.git")
	if out, err := runEnableCmd(t, deps, "--yes"); err != nil {
		t.Fatalf("enable --yes: %v\n%s", err, out)
	}
	out, err := runDisableCmd(t, deps)
	if err != nil {
		t.Fatalf("disable: %v\n%s", err, out)
	}
	root := deps.Git.(*fakeGitRunner).root
	if _, statErr := os.Stat(filepath.Join(root, ".claude", "skills", "lead-flow", "SKILL.md")); !os.IsNotExist(statErr) {
		t.Error("disable did not remove the managed skill file")
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("disable missing removal report, got:\n%s", out)
	}
	if len(gh.CreatedLabels) != 0 {
		t.Errorf("disable created labels %v, want labels kept", gh.CreatedLabels)
	}
}

func TestEnableDroppedCompatFlagsFail(t *testing.T) {
	gh := &testutil.FakeGhClient{}
	deps := enableFixture(t, gh, "")
	if _, err := runEnableCmd(t, deps, "--write", "--yes"); err == nil {
		t.Error("enable --write = nil error, want unknown-flag failure")
	}
	if _, err := runEnableCmd(t, deps, "--uninstall"); err == nil {
		t.Error("enable --uninstall = nil error, want unknown-flag failure")
	}
}

// waitAgentGone polls until pid is dead or reaped. Asserting death instantly
// after SIGKILL flakes on loaded machines, so poll with a deadline instead.
func waitAgentGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var ws syscall.WaitStatus
	for {
		wpid, werr := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		if !(wpid == 0 && werr == nil) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still alive 10s after kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
