package dispatch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// fakeGit records worktree operations and materializes worktree dirs so the
// launcher has a real cwd to point at.
type fakeGit struct {
	root    string
	origin  string
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
func (g *fakeGit) OriginURL(repoDir string) string                   { return g.origin }
func (g *fakeGit) BranchExists(repoDir, branch string) (bool, error) { return true, nil }
func (g *fakeGit) WorktreeList(repoDir string) ([]git.WorktreeInfo, error) {
	return nil, nil
}
func (g *fakeGit) ListBranches(repoDir string) ([]string, error) {
	return nil, nil
}
func (g *fakeGit) CheckoutBranch(repoDir, branch string) error { return nil }

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
	git := &fakeGit{root: root, origin: "github.com/o/r"}
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

func (l *fakeLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.Calls)
}

// waitFor polls cond for up to 5s (worker-pool tests are asynchronous).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// issueOf recovers the issue number from a launch's worktree dir.
func issueOf(c launchCall) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(c.Dir), "issue-"))
	return n
}

// Refill (Part B): 3 ready, parallel 2 → the third starts as soon as one of
// the first two finishes, while the other is still running; Once returns
// after all three are settled.
func TestOnce_RefillsSlotAsSoonAsOneFinishes(t *testing.T) {
	d, gh, _, l := newFixture(t, 1, 2, 3)
	d.Opts.Parallel = 2
	release := map[int]chan struct{}{1: make(chan struct{}), 2: make(chan struct{}), 3: make(chan struct{})}
	var ghMu sync.Mutex
	l.OnWait = func(c launchCall) error {
		n := issueOf(c)
		<-release[n]
		ghMu.Lock()
		defer ghMu.Unlock()
		iss := gh.Issues[n]
		iss.State = "CLOSED"
		gh.Issues[n] = iss
		return nil
	}

	done := make(chan Report, 1)
	go func() {
		rep, err := d.Once(context.Background())
		if err != nil {
			t.Errorf("Once: %v", err)
		}
		done <- rep
	}()

	waitFor(t, "first two launches", func() bool { return l.count() == 2 })
	time.Sleep(20 * time.Millisecond)
	if got := l.count(); got != 2 {
		t.Fatalf("launches while both slots busy = %d, want 2", got)
	}
	close(release[1]) // one slot frees; #2 is still running
	waitFor(t, "third launch after one slot freed", func() bool { return l.count() == 3 })
	select {
	case <-done:
		t.Fatal("Once returned before #2/#3 finished")
	default:
	}
	close(release[2])
	close(release[3])
	rep := <-done
	if len(rep.Results) != 3 {
		t.Fatalf("results = %+v, want 3", rep.Results)
	}
	for _, r := range rep.Results {
		if r.Outcome != OutcomeCompleted {
			t.Errorf("#%d outcome = %s, want completed", r.Issue, r.Outcome)
		}
	}
}

// seedRunning stores a record the way a previous `lead dispatch` process
// would have left it: in progress, PID recorded, worktree on disk.
func seedRunning(t *testing.T, d *Dispatcher, git *fakeGit, issue, pid int) string {
	t.Helper()
	wt := filepath.Join(git.root, ".worktrees", "issue-"+strconv.Itoa(issue))
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	err := d.Store.Upsert(state.Workflow{
		Repository: "https://github.com/o/r.git", // same repo as fakeGit, other URL form
		Issue:      issue, Mode: state.ModeImplement, Branch: "issue/" + strconv.Itoa(issue) + "-x",
		Worktree: wt, Status: state.StatusInProgress, Agent: "claude", PID: pid, Attempts: 1,
		LogPath: filepath.Join(d.Opts.LogDir, "issue-"+strconv.Itoa(issue)+".log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return wt
}

func TestOnce_ReconnectsAliveAgentAndKeepsItsSlot(t *testing.T) {
	d, _, git, l := newFixture(t, 5, 6, 7)
	d.Opts.Parallel = 2
	seedRunning(t, d, git, 5, 999)
	var asked []int
	d.ProcessAlive = func(pid int) bool { asked = append(asked, pid); return true }

	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if !reflect.DeepEqual(asked, []int{999}) {
		t.Errorf("liveness asked for %v, want [999]", asked)
	}
	if !reflect.DeepEqual(rep.Running, []Running{{Issue: 5, PID: 999}}) {
		t.Errorf("running = %+v, want #5 pid 999", rep.Running)
	}
	for _, c := range l.Calls {
		if issueOf(c) == 5 {
			t.Errorf("#5 re-dispatched while its agent is alive")
		}
	}
	// One slot is held by the reconnected agent: only one new launch at a time,
	// but the pool still drains both remaining issues.
	if l.count() != 2 {
		t.Errorf("new launches = %d, want 2 (#6, #7 through the single free slot)", l.count())
	}
	if w, ok, _ := d.Store.Get(5, ""); !ok || w.PID != 999 {
		t.Errorf("#5 record = %+v, %v; want untouched with PID 999", w, ok)
	}
}

func TestOnce_GoneAgentWithClosedIssueCleansUp(t *testing.T) {
	d, gh, git, l := newFixture(t, 5)
	wt := seedRunning(t, d, git, 5, 999)
	iss := gh.Issues[5]
	iss.State = "CLOSED"
	gh.Issues[5] = iss
	d.ProcessAlive = func(int) bool { return false }

	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Issue != 5 || rep.Results[0].Outcome != OutcomeCompleted {
		t.Fatalf("results = %+v, want #5 completed", rep.Results)
	}
	if !reflect.DeepEqual(git.Removed, []string{wt}) {
		t.Errorf("worktree removed = %q, want %q", git.Removed, wt)
	}
	if _, ok, _ := d.Store.Get(5, ""); ok {
		t.Errorf("record for #5 still present")
	}
	if l.count() != 0 {
		t.Errorf("closed issue was launched again (%d launches)", l.count())
	}
}

func TestOnce_GoneAgentWithOpenIssueCountsAttemptAndClearsPID(t *testing.T) {
	d, gh, git, l := newFixture(t, 5)
	seedRunning(t, d, git, 5, 999)
	d.ProcessAlive = func(int) bool { return false }
	d.Opts.MaxAttempts = 3

	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Outcome != OutcomeRetry || rep.Results[0].Attempts != 2 {
		t.Fatalf("results = %+v, want #5 retry at attempt 2", rep.Results)
	}
	if !strings.Contains(rep.Results[0].Err.Error(), "999") {
		t.Errorf("cause %q should name the vanished pid", rep.Results[0].Err)
	}
	w, ok, _ := d.Store.Get(5, "")
	if !ok || w.PID != 0 || w.Attempts != 2 || w.Status != state.StatusInProgress {
		t.Errorf("record = %+v, want pid cleared, attempts 2, in_progress", w)
	}
	if l.count() != 0 {
		t.Errorf("settled issue was re-dispatched in the same pass (%d launches); it must wait for the next pass", l.count())
	}
	if len(gh.AddedLabels) != 0 {
		t.Errorf("labels touched before max attempts: %+v", gh.AddedLabels)
	}

	// Next pass: the pid is gone from the record, so the normal path launches it.
	if _, err := d.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.count() != 1 {
		t.Errorf("launches on next pass = %d, want 1", l.count())
	}
}

func TestOnce_IgnoresRunningRecordsOfOtherRepositories(t *testing.T) {
	d, _, git, l := newFixture(t, 5)
	seedRunning(t, d, git, 5, 999)
	w, _, _ := d.Store.Get(5, "")
	w.Repository = "https://github.com/someone/else.git"
	if err := d.Store.Upsert(w); err != nil {
		t.Fatal(err)
	}
	d.ProcessAlive = func(int) bool { t.Error("liveness checked for a foreign repo record"); return true }

	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(rep.Running) != 0 || l.count() != 1 {
		t.Errorf("running=%+v launches=%d; foreign record must neither hold a slot nor block #5", rep.Running, l.count())
	}
}

func protectedFake(gh *testutil.FakeGhClient) {
	gh.Protection = ports.BranchProtection{Protected: true, RequiredChecks: []string{"test"}, RequiresPR: true}
	gh.AutoMergeAllowed = true
}

func TestPreflight_UnprotectedRepoWarnsAndProceeds(t *testing.T) {
	// Issue #200: missing protection is a warning, not a refusal, so
	// repositories without admin setup can still dispatch.
	d, gh, _, _ := newFixture(t)
	gh.Protection = ports.BranchProtection{} // classic 404 and no rulesets
	var out strings.Builder
	d.Out = &out

	if err := d.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight on unprotected repo = %v, want nil", err)
	}
	for _, want := range []string{"main", "not protected", "required status checks", "WARNING", "lead doctor"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("warning %q lacks %q", out.String(), want)
		}
	}
	if !reflect.DeepEqual(gh.ProtectionCalls, []string{"o/r@main"}) {
		t.Errorf("protection asked for %v, want o/r@main (slug from origin, default branch from gh)", gh.ProtectionCalls)
	}
}

func TestPreflight_MissingRequiredChecksAloneWarns(t *testing.T) {
	d, gh, _, _ := newFixture(t)
	gh.Protection = ports.BranchProtection{Protected: true, RequiresPR: true}
	var out strings.Builder
	d.Out = &out
	if err := d.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight = %v, want nil", err)
	}
	if got := out.String(); !strings.Contains(got, "required status checks") || strings.Contains(got, "not protected") {
		t.Errorf("warning = %q, want only the missing required checks named", got)
	}
}

func TestPreflight_ProtectedRepoPasses(t *testing.T) {
	d, gh, _, _ := newFixture(t)
	protectedFake(gh)
	var out strings.Builder
	d.Out = &out
	if err := d.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight = %v, want nil", err)
	}
	if out.Len() != 0 {
		t.Errorf("protected repo printed %q, want silence", out.String())
	}
}

func TestPreflight_NoOriginRefuses(t *testing.T) {
	d, gh, git, _ := newFixture(t)
	protectedFake(gh)
	git.origin = "" // no remote at all
	err := d.Preflight(context.Background())
	if err == nil || !strings.Contains(err.Error(), "origin") {
		t.Errorf("err = %v, want refusal about missing origin", err)
	}
	if len(gh.ProtectionCalls) != 0 {
		t.Errorf("gh asked %v without a repo slug", gh.ProtectionCalls)
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

func TestLoop_CancelDoesNotKillAgent(t *testing.T) {
	root := t.TempDir()
	argLog := testutil.InstallDummy(t, "claude", "exec sleep 10")

	gh := &testutil.FakeGhClient{
		Issues:  map[int]ports.Issue{7: {Number: 7, Title: "dispatch me", Body: "do the thing", State: "OPEN"}},
		Labeled: map[string][]ports.IssueSummary{"ready": {{Number: 7, Title: "dispatch me"}}},
	}
	store := &state.Store{Path: filepath.Join(t.TempDir(), "workflows.json")}
	logDir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{root: root, origin: "github.com/o/r"}
	d := &Dispatcher{
		Gh:       gh,
		Git:      git,
		Store:    store,
		Launcher: &ExecLauncher{},
		Opts:     Options{Parallel: 1, Agent: "claude", LogDir: logDir, WorkDir: root},
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Loop(ctx, 100*time.Millisecond) }()

	var pid int
	waitFor(t, "agent start", func() bool {
		w, ok, err := store.Get(7, "")
		if err != nil {
			t.Fatalf("store Get: %v", err)
		}
		if ok && w.PID > 0 {
			pid = w.PID
			return true
		}
		return false
	})

	p, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("agent process %d not running before cancel: %v", pid, err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Loop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return after cancel")
	}

	// The process must still be alive: cancel does not kill the agent.
	if err := p.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("agent process %d was killed by cancel: %v", pid, err)
	}

	// Kill it explicitly so the runOne goroutine can finish and the test can exit.
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitFor(t, "agent exit", func() bool {
		return p.Signal(syscall.Signal(0)) != nil
	})

	logText := testutil.LogText(t, argLog)
	for _, want := range []string{"<-p>", "<--dangerously-skip-permissions>", "Issue #7: dispatch me", "do the thing"} {
		if !strings.Contains(logText, want) {
			t.Errorf("agent argv missing %q:\n%s", want, logText)
		}
	}
}

func spawnSleepAgent(t *testing.T) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep unavailable")
	}
	var cmd *exec.Cmd
	if _, err := exec.LookPath("setsid"); err == nil {
		cmd = exec.Command("setsid", "sleep", "60")
	} else {
		cmd = exec.Command("sleep", "60")
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func stopFixture(t *testing.T, pid int, worktree string) (*Dispatcher, *testutil.FakeGhClient, *fakeGit) {
	t.Helper()
	d, gh, git, _ := newFixture(t)
	gh.Issues[7] = ports.Issue{Number: 7, Title: "work", Body: "body", State: "OPEN"}
	rec := state.Workflow{Issue: 7, Repository: "o/r", Branch: "issue/7-t", Worktree: worktree, Status: state.StatusInProgress, Agent: "claude", PID: pid}
	if err := d.Store.Upsert(rec); err != nil {
		t.Fatal(err)
	}
	return d, gh, git
}

func TestStop_KillsProcessCleansUpAndComments(t *testing.T) {
	sleeper := spawnSleepAgent(t)
	pid := sleeper.Process.Pid
	worktree := filepath.Join(t.TempDir(), "issue-7")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	d, gh, _ := stopFixture(t, pid, worktree)
	if err := d.Stop(context.Background(), 7); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	sleeper.Wait()
	if d.alive(pid) {
		t.Errorf("pid %d still alive after Stop", pid)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("worktree not removed: %v", err)
	}
	found := false
	for _, c := range gh.Comments {
		if c.Number == 7 && strings.Contains(c.Body, "Interrupted") {
			found = true
		}
	}
	if !found {
		t.Errorf("no interruption comment on #7: %+v", gh.Comments)
	}
	if _, ok, _ := d.Store.Get(7, ""); ok {
		t.Error("state record not deleted after Stop")
	}
	for _, l := range gh.RemovedLabels {
		if l.Number == 7 && l.Label == "ready" {
			t.Error("ready label removed by Stop; the issue must stay queued")
		}
	}
}

func TestStop_WithoutRecordFails(t *testing.T) {
	d, _, _, _ := newFixture(t)
	if err := d.Stop(context.Background(), 9); err == nil {
		t.Fatal("Stop without a record = nil, want error")
	}
}

func TestStop_ProcessAlreadyGoneStillCleansUp(t *testing.T) {
	sleeper := spawnSleepAgent(t)
	pid := sleeper.Process.Pid
	sleeper.Process.Kill()
	sleeper.Wait()
	d, gh, _ := stopFixture(t, pid, "")
	if err := d.Stop(context.Background(), 7); err != nil {
		t.Fatalf("Stop with dead pid: %v", err)
	}
	if _, ok, _ := d.Store.Get(7, ""); ok {
		t.Error("state record not deleted after Stop")
	}
	if len(gh.Comments) != 1 {
		t.Errorf("comments = %+v, want one interruption comment", gh.Comments)
	}
}

func stuckFixture(t *testing.T, attempts int, logAge time.Duration, startedAgo time.Duration) (*Dispatcher, *testutil.FakeGhClient, *os.Process) {
	t.Helper()
	d, gh, _, _ := newFixture(t)
	d.Opts.StuckAfter = time.Minute * 30
	d.Opts.MaxRuntime = time.Hour * 3
	gh.Issues[7] = ports.Issue{Number: 7, Title: "work", Body: "body", State: "OPEN"}
	sleeper := spawnSleepAgent(t)
	logPath := filepath.Join(t.TempDir(), "issue-7.log")
	if err := os.WriteFile(logPath, []byte("out\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-logAge)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}
	rec := state.Workflow{Issue: 7, Repository: "o/r", Branch: "issue/7", Status: state.StatusInProgress, Agent: "claude", PID: sleeper.Process.Pid, LogPath: logPath, Attempts: attempts, StartedAt: time.Now().Add(-startedAgo)}
	if err := d.Store.Upsert(rec); err != nil {
		t.Fatal(err)
	}
	return d, gh, sleeper.Process
}

func TestOnce_StuckAgentFailsAndCountsAttempt(t *testing.T) {
	d, _, proc := stuckFixture(t, 0, time.Hour, time.Hour)
	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(rep.Running) != 0 {
		t.Errorf("Running = %+v, want stuck agent settled", rep.Running)
	}
	if len(rep.Results) != 1 || rep.Results[0].Outcome != OutcomeRetry {
		t.Errorf("Results = %+v, want one retry", rep.Results)
	}
	w, _, _ := d.Store.Get(7, "")
	if w.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", w.Attempts)
	}
	waitGone(t, proc.Pid)
}

func TestOnce_StuckAgentBlockedAtMaxAttempts(t *testing.T) {
	d, gh, proc := stuckFixture(t, 2, time.Hour, time.Hour)
	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Outcome != OutcomeBlocked {
		t.Fatalf("Results = %+v, want one blocked", rep.Results)
	}
	blocked := false
	for _, l := range gh.AddedLabels {
		if l.Number == 7 && l.Label == "blocked" {
			blocked = true
		}
	}
	if !blocked {
		t.Errorf("blocked label not added: %+v", gh.AddedLabels)
	}
	_ = proc
}

// waitGone polls until pid is dead or reaped. Asserting death instantly
// after SIGKILL flakes on loaded machines (kill returns before the target
// is scheduled to die), so poll with a deadline instead.
func waitGone(t *testing.T, pid int) {
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

func TestOnce_BlockedCommentContainsLogTail(t *testing.T) {
	d, gh, _, l := newFixture(t, 7)
	d.Opts.MaxAttempts = 1
	l.OnWait = func(c launchCall) error {
		// Production ExecLauncher creates the log dir; the fake mirrors it.
		if err := os.MkdirAll(filepath.Dir(c.LogPath), 0o755); err != nil {
			return err
		}
		var b strings.Builder
		for i := 1; i <= 100; i++ {
			b.WriteString("log line " + strconv.Itoa(i) + "\n")
		}
		if err := os.WriteFile(c.LogPath, []byte(b.String()), 0o644); err != nil {
			return err
		}
		return errors.New("exit status 1")
	}
	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if rep.Results[0].Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %s, want blocked", rep.Results[0].Outcome)
	}
	if len(gh.Comments) != 1 {
		t.Fatalf("comments = %+v, want one blocked report", gh.Comments)
	}
	body := gh.Comments[0].Body
	for _, want := range []string{"exit status 1", "log line 100\n", "1"} {
		if !strings.Contains(body, want) {
			t.Errorf("blocked comment missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "\nlog line 1\n") {
		t.Errorf("blocked comment exceeds the tail cap (head lines present):\n%s", body)
	}
}

type fakeNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
}

type notifyCall struct {
	title   string
	message string
}

func (f *fakeNotifier) Notify(title, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, notifyCall{title: title, message: message})
	return nil
}

func (f *fakeNotifier) titles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.title)
	}
	return out
}

func TestOnce_BlockedNotifies(t *testing.T) {
	d, gh, _, l := newFixture(t, 7)
	d.Opts.MaxAttempts = 1
	n := &fakeNotifier{}
	d.Notify = n
	l.OnWait = func(c launchCall) error { return errors.New("exit status 1") }
	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	found := false
	for _, title := range n.titles() {
		if strings.Contains(title, "#7") && strings.Contains(strings.ToLower(title), "blocked") {
			found = true
		}
	}
	if !found {
		t.Errorf("no blocked notification for #7: %v", n.titles())
	}
	_ = gh
}

func TestOnce_AllDoneNotifies(t *testing.T) {
	d, _, _, l := newFixture(t, 7)
	n := &fakeNotifier{}
	d.Notify = n
	l.OnWait = func(c launchCall) error { return errors.New("exit status 1") }
	d.Opts.MaxAttempts = 1
	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	found := false
	for _, title := range n.titles() {
		if strings.Contains(strings.ToLower(title), "all done") {
			found = true
		}
	}
	if !found {
		t.Errorf("no all-done notification: %v", n.titles())
	}
}

func TestOnce_IdlePassStaysSilent(t *testing.T) {
	d, _, _, _ := newFixture(t)
	n := &fakeNotifier{}
	d.Notify = n
	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(n.titles()) != 0 {
		t.Errorf("idle pass notified: %v", n.titles())
	}
}

func TestOnce_RetryDoesNotNotifyAllDone(t *testing.T) {
	d, _, _, l := newFixture(t, 7)
	n := &fakeNotifier{}
	d.Notify = n
	l.OnWait = func(c launchCall) error { return errors.New("exit status 1") }
	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	for _, title := range n.titles() {
		if strings.Contains(strings.ToLower(title), "all done") {
			t.Errorf("retry pass must not report all done: %v", n.titles())
		}
	}
}

// queueFixture builds a dispatcher whose ready list is given verbatim so
// tests control Parent/BlockedBy/UpdatedAt. Parallel=1 keeps launch order
// deterministic; OnWait closes every issue so passes settle.
func queueFixture(t *testing.T, ready ...ports.IssueSummary) (*Dispatcher, *testutil.FakeGhClient, *fakeLauncher) {
	t.Helper()
	root := t.TempDir()
	gh := &testutil.FakeGhClient{Issues: map[int]ports.Issue{}, Labeled: map[string][]ports.IssueSummary{"ready": ready}}
	for _, s := range ready {
		gh.Issues[s.Number] = ports.Issue{Number: s.Number, Title: "dispatch me", Body: "body", State: "OPEN"}
	}
	l := &fakeLauncher{}
	l.OnWait = func(c launchCall) error {
		n := issueOf(c)
		iss := gh.Issues[n]
		iss.State = "CLOSED"
		gh.Issues[n] = iss
		return nil
	}
	d := &Dispatcher{
		Gh: gh, Git: &fakeGit{root: root, origin: "github.com/o/r"}, Launcher: l,
		Store: &state.Store{Path: filepath.Join(t.TempDir(), "workflows.json")},
		Opts:  Options{Parallel: 1, Agent: "claude", LogDir: filepath.Join(t.TempDir(), "logs"), WorkDir: root},
	}
	return d, gh, l
}

func launchedOrder(l *fakeLauncher) []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []int
	for _, c := range l.Calls {
		out = append(out, issueOf(c))
	}
	return out
}

// Issue #207: dispatch order follows the parent (tracking) issue's
// sub-issue order, not `gh issue list` order.
func TestOnce_SubIssueOrderControlsLaunchOrder(t *testing.T) {
	base := time.Now()
	d, gh, l := queueFixture(t,
		ports.IssueSummary{Number: 3, Parent: 50, UpdatedAt: base},
		ports.IssueSummary{Number: 1, Parent: 50, UpdatedAt: base.Add(time.Hour)},
		ports.IssueSummary{Number: 2, Parent: 50, UpdatedAt: base.Add(2 * time.Hour)},
	)
	gh.SubIssueLists = map[int]ports.SubIssueList{
		50: {State: "OPEN", Numbers: []int{1, 2, 3}},
	}

	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if got, want := launchedOrder(l), []int{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(gh.SubIssuesCalls, []int{50}) {
		t.Errorf("SubIssues calls = %v, want [50]", gh.SubIssuesCalls)
	}
}

// An open blocker defers a ready issue without labelling it; it dispatches
// once the blocker is closed.
func TestOnce_OpenBlockerDefersIssue(t *testing.T) {
	d, gh, l := queueFixture(t,
		ports.IssueSummary{Number: 8, BlockedBy: []ports.IssueDependency{{Number: 35, State: "OPEN"}}},
		ports.IssueSummary{Number: 9},
	)

	rep, err := d.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if got, want := launchedOrder(l), []int{9}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order = %v, want %v (#8 must be deferred)", got, want)
	}
	if !reflect.DeepEqual(rep.Deferred, []int{8}) {
		t.Errorf("deferred = %v, want [8]", rep.Deferred)
	}

	// #9 closed in pass 1, so a real `--state open` listing drops it.
	gh.Labeled["ready"] = []ports.IssueSummary{
		{Number: 8, BlockedBy: []ports.IssueDependency{{Number: 35, State: "CLOSED"}}},
	}
	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once #2: %v", err)
	}
	if got, want := launchedOrder(l), []int{9, 8}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order after unblock = %v, want %v", got, want)
	}
}

// Untracked ready issues run after every tracked group.
func TestOnce_UntrackedIssueRunsAfterTracked(t *testing.T) {
	base := time.Now()
	d, gh, l := queueFixture(t,
		ports.IssueSummary{Number: 9, UpdatedAt: base}, // no parent
		ports.IssueSummary{Number: 2, Parent: 50, UpdatedAt: base.Add(2 * time.Hour)},
		ports.IssueSummary{Number: 1, Parent: 50, UpdatedAt: base.Add(time.Hour)},
	)
	gh.SubIssueLists = map[int]ports.SubIssueList{
		50: {State: "OPEN", Numbers: []int{1, 2}},
	}

	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if got, want := launchedOrder(l), []int{1, 2, 9}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order = %v, want %v", got, want)
	}
}

// A closed tracking issue no longer orders its sub-issues; they fall back
// to updatedAt order among the untracked.
func TestOnce_ClosedParentDoesNotOrder(t *testing.T) {
	base := time.Now()
	d, gh, l := queueFixture(t,
		ports.IssueSummary{Number: 2, Parent: 50, UpdatedAt: base.Add(2 * time.Hour)},
		ports.IssueSummary{Number: 1, Parent: 50, UpdatedAt: base.Add(time.Hour)},
	)
	gh.SubIssueLists = map[int]ports.SubIssueList{
		50: {State: "CLOSED", Numbers: []int{2, 1}},
	}

	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if got, want := launchedOrder(l), []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order = %v, want %v (updatedAt order)", got, want)
	}
}

// Mixed parents are dispatched as whole groups in parent-number order.
func TestOnce_MultipleParentsGroupByParentNumber(t *testing.T) {
	d, gh, l := queueFixture(t,
		ports.IssueSummary{Number: 6, Parent: 60},
		ports.IssueSummary{Number: 1, Parent: 50},
		ports.IssueSummary{Number: 5, Parent: 60},
		ports.IssueSummary{Number: 2, Parent: 50},
	)
	gh.SubIssueLists = map[int]ports.SubIssueList{
		50: {State: "OPEN", Numbers: []int{1, 2}},
		60: {State: "OPEN", Numbers: []int{5, 6}},
	}

	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if got, want := launchedOrder(l), []int{1, 2, 5, 6}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order = %v, want %v", got, want)
	}
}

// A SubIssues lookup failure must not stall the pass: the affected group
// falls back to untracked ordering.
func TestOnce_SubIssuesErrorFallsBack(t *testing.T) {
	base := time.Now()
	d, gh, l := queueFixture(t,
		ports.IssueSummary{Number: 2, Parent: 50, UpdatedAt: base.Add(2 * time.Hour)},
		ports.IssueSummary{Number: 1, Parent: 50, UpdatedAt: base.Add(time.Hour)},
	)
	gh.SubIssuesErr = errors.New("gh api down")

	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if got, want := launchedOrder(l), []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("launch order = %v, want %v (updatedAt order)", got, want)
	}
}

// Deferred issues are still pending work, so they suppress the all-done
// notification even when everything dispatched completed.
func TestOnce_DeferredDoesNotNotifyAllDone(t *testing.T) {
	d, gh, _ := queueFixture(t,
		ports.IssueSummary{Number: 8, BlockedBy: []ports.IssueDependency{{Number: 35, State: "OPEN"}}},
		ports.IssueSummary{Number: 9},
	)
	n := &fakeNotifier{}
	d.Notify = n
	if _, err := d.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	for _, title := range n.titles() {
		if strings.Contains(strings.ToLower(title), "all done") {
			t.Errorf("deferred pass must not report all done: %v", n.titles())
		}
	}
	_ = gh
}
