package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ports"
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
	for _, cmd := range []string{"version", "work", "status", "clean", "finish", "server", "api", "setup", "completion", "doctor", "update", "init", "enable"} {
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
	gh := &testutil.FakeGhClient{RepoLabelNames: []string{"needs-review", "ready", "blocked"}}
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
