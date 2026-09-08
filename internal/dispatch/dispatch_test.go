package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// fakeGit records worktree operations and materializes worktree dirs so the
// launcher has a real cwd to point at.
type fakeGit struct {
	root    string
	Added   []string
	Removed []string
}

func (g *fakeGit) RepoRoot(dir string) (string, error) { return g.root, nil }
func (g *fakeGit) CreateBranch(repoDir, branch string) error {
	return errors.New("dispatch must use worktrees, not in-place branches")
}
func (g *fakeGit) WorktreeAdd(repoDir, path, branch string) error {
	g.Added = append(g.Added, path)
	return os.MkdirAll(path, 0o755)
}
func (g *fakeGit) WorktreeRemove(repoDir, path string, force bool) error {
	g.Removed = append(g.Removed, path)
	return os.RemoveAll(path)
}
func (g *fakeGit) OriginURL(repoDir string) string { return "github.com/o/r" }

type launchCall struct {
	Dir     string
	Argv    []string
	LogPath string
}

// fakeLauncher records launches; OnWait runs when the process is waited on
// (tests use it to flip the issue to closed, or to fail).
type fakeLauncher struct {
	mu     sync.Mutex
	Calls  []launchCall
	OnWait func(call launchCall) error
}

type fakeProc struct {
	l    *fakeLauncher
	call launchCall
}

func (p *fakeProc) Pid() int { return 4242 }
func (p *fakeProc) Wait() error {
	if p.l.OnWait != nil {
		return p.l.OnWait(p.call)
	}
	return nil
}

func (l *fakeLauncher) Start(ctx context.Context, dir string, argv []string, logPath string) (Process, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := launchCall{Dir: dir, Argv: argv, LogPath: logPath}
	l.Calls = append(l.Calls, c)
	return &fakeProc{l: l, call: c}, nil
}

func newFixture(t *testing.T, ready ...int) (*Dispatcher, *testutil.FakeGhClient, *fakeGit, *fakeLauncher) {
	t.Helper()
	root := t.TempDir()
	gh := &testutil.FakeGhClient{Issues: map[int]ports.Issue{}, Labeled: map[string][]ports.IssueSummary{}}
	for _, n := range ready {
		gh.Issues[n] = ports.Issue{Number: n, Title: "dispatch me", Body: "body", State: "OPEN"}
		gh.Labeled["ready"] = append(gh.Labeled["ready"], ports.IssueSummary{Number: n, Title: "dispatch me"})
	}
	git := &fakeGit{root: root}
	l := &fakeLauncher{}
	d := &Dispatcher{
		Gh: gh, Git: git, Launcher: l,
		Store: &state.Store{Path: filepath.Join(t.TempDir(), "workflows.json")},
		Opts:  Options{Parallel: 2, Agent: "claude", LogDir: filepath.Join(t.TempDir(), "logs"), WorkDir: root},
	}
	return d, gh, git, l
}

func TestOnce_CompletedIssueCleansUp(t *testing.T) {
	d, gh, git, l := newFixture(t, 7)
	l.OnWait = func(c launchCall) error {
		iss := gh.Issues[7]
		iss.State = "CLOSED"
		gh.Issues[7] = iss
		return nil
	}

	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(l.Calls) != 1 {
		t.Fatalf("launch calls = %d, want 1", len(l.Calls))
	}
	c := l.Calls[0]
	wantDir := filepath.Join(git.root, ".worktrees", "issue-7")
	if c.Dir != wantDir {
		t.Errorf("launch dir = %q, want %q", c.Dir, wantDir)
	}
	wantHead := []string{"claude", "-p", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(c.Argv[:3], wantHead) {
		t.Errorf("argv head = %q, want %q", c.Argv[:3], wantHead)
	}
	prompt := c.Argv[3]
	for _, want := range []string{"Issue #7: dispatch me", "body", "gh pr create", "closed"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if !strings.HasPrefix(c.LogPath, d.Opts.LogDir) {
		t.Errorf("log path %q not under %q", c.LogPath, d.Opts.LogDir)
	}
	if len(rep.Results) != 1 || rep.Results[0].Outcome != OutcomeCompleted {
		t.Fatalf("results = %+v, want one completed", rep.Results)
	}
	if !reflect.DeepEqual(git.Removed, []string{wantDir}) {
		t.Errorf("worktree removed = %q, want %q", git.Removed, []string{wantDir})
	}
	if _, ok, _ := d.Store.Get(7, ""); ok {
		t.Errorf("state record for #7 still present after completion")
	}
}

func TestOnce_FailuresRetryThenBlockAtMax(t *testing.T) {
	d, gh, _, l := newFixture(t, 7)
	l.OnWait = func(c launchCall) error { return errors.New("exit status 1") }
	d.Opts.MaxAttempts = 3

	for i := 1; i <= 2; i++ {
		rep, err := d.Once(context.Background())
		if err != nil {
			t.Fatalf("Once #%d: %v", i, err)
		}
		if rep.Results[0].Outcome != OutcomeRetry {
			t.Fatalf("Once #%d outcome = %s, want retry", i, rep.Results[0].Outcome)
		}
		w, ok, _ := d.Store.Get(7, "")
		if !ok || w.Attempts != i || w.Status != state.StatusInProgress {
			t.Fatalf("Once #%d: state = %+v, want attempts=%d in_progress", i, w, i)
		}
	}
	if len(gh.AddedLabels)+len(gh.RemovedLabels)+len(gh.Comments) != 0 {
		t.Fatalf("labels/comments touched before max attempts: %+v %+v %+v", gh.AddedLabels, gh.RemovedLabels, gh.Comments)
	}

	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once #3: %v", err)
	}
	if rep.Results[0].Outcome != OutcomeBlocked {
		t.Fatalf("Once #3 outcome = %s, want blocked", rep.Results[0].Outcome)
	}
	if !reflect.DeepEqual(gh.RemovedLabels, []testutil.LabelCall{{Number: 7, Label: "ready"}}) {
		t.Errorf("removed labels = %+v", gh.RemovedLabels)
	}
	if !reflect.DeepEqual(gh.AddedLabels, []testutil.LabelCall{{Number: 7, Label: "blocked"}}) {
		t.Errorf("added labels = %+v", gh.AddedLabels)
	}
	if len(gh.Comments) != 1 || gh.Comments[0].Number != 7 ||
		!strings.Contains(gh.Comments[0].Body, "3") || !strings.Contains(gh.Comments[0].Body, "exit status 1") {
		t.Errorf("comments = %+v, want one blocked report on #7 with attempts and cause", gh.Comments)
	}
	w, _, _ := d.Store.Get(7, "")
	if w.Status != state.StatusBlocked {
		t.Errorf("state status = %s, want blocked", w.Status)
	}

	// The fake gh still lists #7 as ready; local blocked state must win.
	before := len(l.Calls)
	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once #4: %v", err)
	}
	if len(l.Calls) != before {
		t.Errorf("blocked issue was re-dispatched (%d → %d launches)", before, len(l.Calls))
	}
}

func TestOnce_RespectsParallelLimit(t *testing.T) {
	d, _, _, l := newFixture(t, 1, 2, 3)
	d.Opts.Parallel = 2
	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(l.Calls) != 2 {
		t.Errorf("launches = %d, want 2 (parallel limit)", len(l.Calls))
	}
}

func TestOnce_NoReadyIssuesIsNoop(t *testing.T) {
	d, _, _, l := newFixture(t)
	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(rep.Results) != 0 || len(l.Calls) != 0 {
		t.Errorf("expected no-op, got results=%+v launches=%d", rep.Results, len(l.Calls))
	}
}

func TestBuildPrompt_Golden(t *testing.T) {
	got := BuildPrompt(ports.Issue{Number: 42, Title: "feat: thing", Body: "## 受入条件\n- a\n- b"})
	testutil.AssertGoldenString(t, "testdata/prompt.golden", got)
}

func TestExecLauncher_RunsInDirAndLogs(t *testing.T) {
	argLog := testutil.InstallDummy(t, "claude", `pwd; echo "err-line" >&2`)
	dir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "logs", "issue-7.log")

	proc, err := (&ExecLauncher{}).Start(context.Background(), dir, []string{"claude", "-p", "hello world"}, logPath)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if proc.Pid() <= 0 {
		t.Errorf("pid = %d", proc.Pid())
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := testutil.LogLines(t, argLog); !reflect.DeepEqual(got, []string{"<-p>", "<hello world>"}) {
		t.Errorf("dummy argv = %q", got)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(raw), dir) || !strings.Contains(string(raw), "err-line") {
		t.Errorf("log missing cwd or stderr:\n%s", raw)
	}
}

func TestExecLauncher_MissingBinary(t *testing.T) {
	testutil.EmptyBin(t)
	_, err := (&ExecLauncher{}).Start(context.Background(), t.TempDir(), []string{"claude", "-p", "x"}, filepath.Join(t.TempDir(), "l.log"))
	if !ports.IsBinaryNotFound(err) {
		t.Errorf("err = %v, want BinaryNotFoundError", err)
	}
}
