package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestWork_WithoutNumberEmptyList(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := workflowDeps(t, repo) // fake has no summaries

	out, err := executeWith(t, deps, "work")
	if err != nil {
		t.Fatalf("work with empty list: %v", err)
	}
	if !strings.Contains(out, "no open issues") {
		t.Errorf("output = %q, want empty notice", out)
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

// issue #216: in_progress workflows show elapsed (since started_at) and
// last-activity (log mtime, else started_at); --json carries both fields.
func TestStatus_ShowsActivityForInProgress(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)
	logPath := filepath.Join(t.TempDir(), "agent-7.log")
	if err := os.WriteFile(logPath, []byte("working\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	if err := os.Chtimes(logPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	store := &state.Store{Path: stateFile}
	if err := store.Upsert(state.Workflow{
		Repository: "o/r", Issue: 7, Mode: state.ModeImplement, Status: state.StatusInProgress,
		Branch: "issue/7", Agent: "agy", LogPath: logPath, StartedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// A completed workflow must not carry activity fields.
	if err := store.Upsert(state.Workflow{
		Repository: "o/r", Issue: 8, Mode: state.ModeImplement, Status: state.StatusCompleted,
		Branch: "issue/8",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := executeWith(t, deps, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"Elapsed:", "Last activity:"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q for in_progress workflow:\n%s", want, out)
		}
	}

	out, err = executeWith(t, deps, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	var decoded struct {
		Workflows []struct {
			Issue               int        `json:"issue"`
			ElapsedSeconds      *int64     `json:"elapsed_seconds"`
			LastActivityAt      *time.Time `json:"last_activity_at"`
			LastActivitySeconds *int64     `json:"last_activity_seconds"`
		} `json:"workflows"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("status --json not valid JSON: %v\n%s", err, out)
	}
	var inProg, done *struct {
		Issue               int        `json:"issue"`
		ElapsedSeconds      *int64     `json:"elapsed_seconds"`
		LastActivityAt      *time.Time `json:"last_activity_at"`
		LastActivitySeconds *int64     `json:"last_activity_seconds"`
	}
	for i := range decoded.Workflows {
		switch decoded.Workflows[i].Issue {
		case 7:
			inProg = &decoded.Workflows[i]
		case 8:
			done = &decoded.Workflows[i]
		}
	}
	if inProg == nil {
		t.Fatalf("status --json missing #7:\n%s", out)
	}
	if inProg.ElapsedSeconds == nil || *inProg.ElapsedSeconds < 3500 {
		t.Errorf("elapsed_seconds = %v, want ~3600", inProg.ElapsedSeconds)
	}
	if inProg.LastActivitySeconds == nil || *inProg.LastActivitySeconds < 240 || *inProg.LastActivitySeconds > 400 {
		t.Errorf("last_activity_seconds = %v, want ~300", inProg.LastActivitySeconds)
	}
	if inProg.LastActivityAt == nil || !inProg.LastActivityAt.Equal(mtime) {
		t.Errorf("last_activity_at = %v, want %v", inProg.LastActivityAt, mtime)
	}
	if done != nil && (done.ElapsedSeconds != nil || done.LastActivitySeconds != nil) {
		t.Errorf("completed workflow #8 carries activity fields: %+v", done)
	}
}

// issue #216: last-activity falls back to started_at when the log file is
// absent — the watchdog's definition.
func TestStatus_ActivityFallsBackToStartedAt(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)
	store := &state.Store{Path: stateFile}
	if err := store.Upsert(state.Workflow{
		Repository: "o/r", Issue: 9, Mode: state.ModeImplement, Status: state.StatusInProgress,
		Branch: "issue/9", Agent: "agy", LogPath: filepath.Join(t.TempDir(), "missing.log"),
		StartedAt: time.Now().Add(-30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	out, err := executeWith(t, deps, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	var decoded struct {
		Workflows []struct {
			Issue               int    `json:"issue"`
			LastActivitySeconds *int64 `json:"last_activity_seconds"`
		} `json:"workflows"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("status --json not valid JSON: %v\n%s", err, out)
	}
	if len(decoded.Workflows) != 1 || decoded.Workflows[0].LastActivitySeconds == nil {
		t.Fatalf("status --json missing last_activity_seconds:\n%s", out)
	}
	if got := *decoded.Workflows[0].LastActivitySeconds; got < 1700 {
		t.Errorf("last_activity_seconds = %d, want ~1800 (started_at fallback)", got)
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

func TestWork_HerdrPreparesAgentCommand(t *testing.T) {
	repo := initRepo(t)
	deps, fake, _ := workflowDeps(t, repo)
	fake.Issues[36] = ports.Issue{Number: 36, Title: "ports adapter", Body: "body", State: "OPEN"}

	t.Setenv("HERDR_ENV", "1")
	logPath := testutil.InstallDummy(t, "herdr",
		`if [ "$1 $2" = "pane split" ]; then printf '%s' '{"result":{"pane":{"pane_id":"pane-123"}}}'; elif [ "$1 $2" = "pane send-text" ]; then :; else echo "unexpected: $@" >&2; exit 3; fi`)

	out, err := executeWith(t, deps, "work", "36")
	if err != nil {
		t.Fatalf("work 36: %v\n%s", err, out)
	}

	log := testutil.LogText(t, logPath)
	for _, want := range []string{
		"<pane>", "<split>", "<--direction>", "<right>", "<--ratio>", "<0.5>",
		"<send-text>", "<pane-123>",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("herdr log missing %q, got:\n%s", want, log)
		}
	}
	if !strings.Contains(log, `cd "`) || !strings.Contains(log, `&& agy -i "`) {
		t.Errorf("herdr log missing prepared agent command, got:\n%s", log)
	}
	if !strings.Contains(log, "ports adapter") {
		t.Errorf("herdr log missing issue title, got:\n%s", log)
	}
	if !strings.Contains(log, "body") {
		t.Errorf("herdr log missing issue body, got:\n%s", log)
	}
	if strings.Contains(log, "<run>") {
		t.Errorf("must not call herdr pane run; got:\n%s", log)
	}
}

func TestWork_InlineFallbackSurfacesCommand(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := workflowDeps(t, repo)
	t.Setenv("HERDR_ENV", "")

	out, err := executeWith(t, deps, "work", "36")
	if err != nil {
		t.Fatalf("work 36: %v\n%s", err, out)
	}
	if !strings.Contains(out, `agy -i "`) {
		t.Errorf("inline output missing agy command, got:\n%s", out)
	}
	if !strings.Contains(out, `cd "`) {
		t.Errorf("inline output missing cd to working directory, got:\n%s", out)
	}
}

func TestWork_DoesNotReSplitPane(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := workflowDeps(t, repo)
	fakeHerdr := &testutil.FakeHerdrRunner{PaneID: "pane-1"}
	deps.Herdr = fakeHerdr

	if _, err := executeWith(t, deps, "work", "36"); err != nil {
		t.Fatalf("work 36: %v", err)
	}
	if _, err := executeWith(t, deps, "work", "36"); err != nil {
		t.Fatalf("re-work 36: %v", err)
	}
	if len(fakeHerdr.Splits) != 1 || len(fakeHerdr.Sends) != 1 {
		t.Errorf("splits=%d sends=%d, want 1 each (pane reuse)", len(fakeHerdr.Splits), len(fakeHerdr.Sends))
	}
}

func TestWork_AgentCommandRespectsAgentFlag(t *testing.T) {
	repo := initRepo(t)
	deps, fake, _ := workflowDeps(t, repo)
	fake.Issues[36] = ports.Issue{Number: 36, Title: "ports adapter", Body: "body", State: "OPEN"}

	t.Setenv("HERDR_ENV", "1")
	logPath := testutil.InstallDummy(t, "herdr",
		`if [ "$1 $2" = "pane split" ]; then printf '%s' '{"result":{"pane":{"pane_id":"pane-123"}}}'; elif [ "$1 $2" = "pane send-text" ]; then :; else echo "unexpected: $@" >&2; exit 3; fi`)

	out, err := executeWith(t, deps, "work", "36", "--agent", "devin")
	if err != nil {
		t.Fatalf("work 36 --agent devin: %v\n%s", err, out)
	}

	log := testutil.LogText(t, logPath)
	if !strings.Contains(log, `&& devin "`) {
		t.Errorf("herdr log missing devin positional prompt command, got:\n%s", log)
	}
	if strings.Contains(log, "-i") {
		t.Errorf("devin command must not use agy -i flag; got:\n%s", log)
	}
}

func TestResume_CLI(t *testing.T) {
	repo := initRepo(t)
	deps, fake, _ := workflowDeps(t, repo)
	fake.Issues[36] = ports.Issue{Number: 36, Title: "ports adapter", Body: "body", State: "OPEN"}

	// Start work initially
	if _, err := executeWith(t, deps, "work", "36"); err != nil {
		t.Fatalf("work 36: %v", err)
	}

	// Resume by issue number
	out, err := executeWith(t, deps, "resume", "36")
	if err != nil {
		t.Fatalf("resume 36: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Issue #36") || !strings.Contains(out, "issue/36-ports-adapter") {
		t.Errorf("resume output missing issue/branch:\n%s", out)
	}

	// Resume by branch name
	out, err = executeWith(t, deps, "resume", "issue/36-ports-adapter")
	if err != nil {
		t.Fatalf("resume branch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "issue/36-ports-adapter") {
		t.Errorf("resume branch output missing branch:\n%s", out)
	}
}

func TestStatus_ShowsResumeInstruction(t *testing.T) {
	repo := initRepo(t)
	deps, _, _ := workflowDeps(t, repo)
	if _, err := executeWith(t, deps, "work", "36"); err != nil {
		t.Fatal(err)
	}

	out, err := executeWith(t, deps, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	want := "Next: resume with lead resume 36"
	if !strings.Contains(out, want) {
		t.Errorf("status output missing %q, got:\n%s", want, out)
	}
}

