package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// initRepo creates a git repo with one commit.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func workflowDeps(t *testing.T, repo string) (Deps, *testutil.FakeGhClient, string) {
	t.Helper()
	fake := &testutil.FakeGhClient{
		Issues: map[int]ports.Issue{
			36: {Number: 36, Title: "ports adapter", Body: "body", State: "OPEN"},
		},
	}
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	return Deps{Gh: fake, Git: git.New(), StateFile: stateFile, WorkDir: repo}, fake, stateFile
}

func executeWith(t *testing.T, deps Deps, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", deps)
	var outBuf, errBuf strings.Builder
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		return outBuf.String() + errBuf.String(), err
	}
	return outBuf.String(), nil
}

func loadState(t *testing.T, path string) []state.Workflow {
	t.Helper()
	all, err := (&state.Store{Path: path}).List()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return all
}

func TestWork_CreatesBranchAndRecordsState(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)

	out, err := executeWith(t, deps, "work", "36")
	if err != nil {
		t.Fatalf("work 36: %v\n%s", err, out)
	}
	ok, err := git.New().BranchExists(repo, "issue/36-ports-adapter")
	if err != nil || !ok {
		t.Errorf("branch issue/36-ports-adapter exists = %v, %v", ok, err)
	}
	all := loadState(t, stateFile)
	if len(all) != 1 || all[0].Issue != 36 || all[0].Status != state.StatusInProgress {
		t.Errorf("state = %+v, want one in_progress record for #36", all)
	}
	if all[0].Branch != "issue/36-ports-adapter" {
		t.Errorf("recorded branch = %q", all[0].Branch)
	}
	if !strings.Contains(out, "issue/36-ports-adapter") {
		t.Errorf("work output missing branch guidance:\n%s", out)
	}
}

func TestWork_IsIdempotent(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)

	if _, err := executeWith(t, deps, "work", "36"); err != nil {
		t.Fatal(err)
	}
	if _, err := executeWith(t, deps, "work", "36"); err != nil {
		t.Fatalf("re-work 36: %v, want idempotent success", err)
	}
	if all := loadState(t, stateFile); len(all) != 1 {
		t.Errorf("records after re-work = %d, want 1", len(all))
	}
}

func TestWork_WithAutoWorktree(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)

	if _, err := executeWith(t, deps, "work", "36", "--worktree"); err != nil {
		t.Fatalf("work --worktree: %v", err)
	}
	all := loadState(t, stateFile)
	if len(all) != 1 || all[0].Worktree == "" {
		t.Fatalf("state = %+v, want recorded worktree path", all)
	}
	if _, err := os.Stat(filepath.Join(all[0].Worktree, "f.txt")); err != nil {
		t.Errorf("auto worktree not checked out at %q: %v", all[0].Worktree, err)
	}
}

func TestWork_WithExplicitWorktree(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)
	custom := filepath.Join(t.TempDir(), "custom-wt")

	if _, err := executeWith(t, deps, "work", "36", "--worktree="+custom); err != nil {
		t.Fatalf("work --worktree custom: %v", err)
	}
	if all := loadState(t, stateFile); len(all) != 1 || all[0].Worktree != custom {
		t.Errorf("state = %+v, want custom worktree %q", all, custom)
	}
}

func TestWork_WithoutNumberNeedsTUI(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := workflowDeps(t, repo)

	_, err := executeWith(t, deps, "work")
	if err == nil {
		t.Fatal("work without number = nil, want TUI-pending failure (#37)")
	}
	if !strings.Contains(err.Error(), "#37") {
		t.Errorf("work error = %q, want pointer to #37 TUI", err)
	}
}

func TestWork_InvalidNumberFails(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := workflowDeps(t, repo)

	if _, err := executeWith(t, deps, "work", "abc"); err == nil {
		t.Error("work abc = nil, want number parse failure")
	}
}

func TestWork_UnknownIssuePropagates(t *testing.T) {
	repo := initRepo(t)
	deps, fake, _ := workflowDeps(t, repo)
	fake.Issues = map[int]ports.Issue{}

	if _, err := executeWith(t, deps, "work", "999"); err == nil {
		t.Error("work 999 (unknown) = nil, want propagation")
	}
}

func TestStatus_ListsWorkflows(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := workflowDeps(t, repo)

	out, err := executeWith(t, deps, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "no workflows") {
		t.Errorf("empty status = %q, want 'no workflows' message", out)
	}
	if _, err := executeWith(t, deps, "work", "36"); err != nil {
		t.Fatal(err)
	}
	out, err = executeWith(t, deps, "status")
	if err != nil {
		t.Fatalf("status after work: %v", err)
	}
	for _, want := range []string{"#36", "issue/36-ports-adapter", "in_progress"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

func TestStatus_JSON(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := workflowDeps(t, repo)
	if _, err := executeWith(t, deps, "work", "36"); err != nil {
		t.Fatal(err)
	}
	out, err := executeWith(t, deps, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	var decoded struct {
		Workflows []state.Workflow `json:"workflows"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("status --json not valid JSON: %v\n%s", err, out)
	}
	if len(decoded.Workflows) != 1 || decoded.Workflows[0].Issue != 36 {
		t.Errorf("status --json = %+v, want one record for #36", decoded.Workflows)
	}
}

func TestStatus_CorruptFileSurfacesError(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := executeWith(t, deps, "status")
	if err == nil {
		t.Fatal("status on corrupt file = nil, want error")
	}
	if !strings.Contains(out, "corrupt") {
		t.Errorf("corrupt output = %q, want 'corrupt' guidance", out)
	}
}

func TestClean_RemovesWorktreeAndRecord(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)
	if _, err := executeWith(t, deps, "work", "36", "--worktree"); err != nil {
		t.Fatal(err)
	}
	wt := loadState(t, stateFile)[0].Worktree

	out, err := executeWith(t, deps, "clean", "36")
	if err != nil {
		t.Fatalf("clean 36: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
		t.Errorf("worktree %q still present after clean", wt)
	}
	if all := loadState(t, stateFile); len(all) != 0 {
		t.Errorf("records after clean = %+v, want empty", all)
	}
	// Idempotent: cleaning again succeeds.
	if _, err := executeWith(t, deps, "clean", "36"); err != nil {
		t.Errorf("re-clean 36: %v, want idempotent success", err)
	}
}

func TestClean_WithoutRecordSucceeds(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := workflowDeps(t, repo)

	if _, err := executeWith(t, deps, "clean", "36"); err != nil {
		t.Errorf("clean without record: %v, want idempotent success", err)
	}
}

func TestWork_PropagatesMissingGit(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := workflowDeps(t, repo)
	testutil.EmptyBin(t) // hides git (gh fake is in-memory, unaffected)

	// RepoRoot runs `git`, so work must surface the missing binary.
	if _, err := executeWith(t, deps, "work", "36"); err == nil {
		t.Error("work with no git = nil, want failure")
	}
	_ = context.Background
}
