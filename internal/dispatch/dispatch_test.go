package dispatch

import (
	"context"
	"errors"
	"os"
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

func TestPreflight_RefusesUnprotectedRepoWithGuidance(t *testing.T) {
	d, gh, _, _ := newFixture(t)
	gh.Protection = ports.BranchProtection{} // classic 404 and no rulesets
	var out strings.Builder
	d.Out = &out

	err := d.Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight on unprotected repo = nil, want refusal")
	}
	for _, want := range []string{"main", "not protected", "required status checks", "--skip-protection-check", "lead doctor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	if !reflect.DeepEqual(gh.ProtectionCalls, []string{"o/r@main"}) {
		t.Errorf("protection asked for %v, want o/r@main (slug from origin, default branch from gh)", gh.ProtectionCalls)
	}
}

func TestPreflight_MissingRequiredChecksAloneRefuses(t *testing.T) {
	d, gh, _, _ := newFixture(t)
	gh.Protection = ports.BranchProtection{Protected: true, RequiresPR: true}
	err := d.Preflight(context.Background())
	if err == nil || !strings.Contains(err.Error(), "required status checks") || strings.Contains(err.Error(), "not protected") {
		t.Errorf("err = %v, want refusal naming only the missing required checks", err)
	}
}

func TestPreflight_SkipFlagWarnsAndProceeds(t *testing.T) {
	d, _, _, _ := newFixture(t)
	d.Opts.SkipProtectionCheck = true
	var out strings.Builder
	d.Out = &out
	if err := d.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight with skip = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "WARNING") || !strings.Contains(out.String(), "not protected") {
		t.Errorf("skip output = %q, want a warning naming the gap", out.String())
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
