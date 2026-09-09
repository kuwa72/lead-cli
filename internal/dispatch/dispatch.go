// Package dispatch hands `ready` issues to headless coding agents
// (docs/rfc-inbox-ux.md §7, issue #93 Part A).
//
// One pass (Once): list open issues labelled ready, skip ones the local
// state marks blocked, take up to Parallel, and for each: create/reuse an
// isolated worktree (workflow.Start), launch the agent non-interactively
// in that directory with its output going to a log file, wait, then check
// GitHub. A closed issue means the agent finished the whole loop
// (PR → CI → merge → close): the worktree and state record are removed.
// Anything else counts as a failed attempt; MaxAttempts failures swap the
// ready label for blocked and post the cause to the issue so the inbox
// surfaces it. GitHub remains the source of truth; workflows.json only
// holds what GitHub cannot (worktree, pid, attempts, log).
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kuwa72/lead-cli/internal/adapters/agent"
	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/doctor"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/workflow"
)

// Defaults (RFC §7, §13: label names are fixed for now).
const (
	DefaultParallel     = 2
	DefaultMaxAttempts  = 3
	DefaultReadyLabel   = "ready"
	DefaultBlockedLabel = "blocked"
)

// Options tunes one Dispatcher. Zero fields take the defaults above.
type Options struct {
	Parallel     int
	Agent        string // headless implementation agent (agents.impl)
	ReadyLabel   string
	BlockedLabel string
	MaxAttempts  int
	LogDir       string // agent stdout/stderr files; required
	WorkDir      string // repository context for workflow.Start; required
	// SkipProtectionCheck makes Preflight warn instead of refusing when the
	// repository lacks branch protection / required checks (issue #67).
	SkipProtectionCheck bool
}

func (o Options) withDefaults() Options {
	if o.Parallel <= 0 {
		o.Parallel = DefaultParallel
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.ReadyLabel == "" {
		o.ReadyLabel = DefaultReadyLabel
	}
	if o.BlockedLabel == "" {
		o.BlockedLabel = DefaultBlockedLabel
	}
	o.Agent = agent.Resolve(o.Agent)
	return o
}

// Process is a started agent.
type Process interface {
	Pid() int
	Wait() error
}

// Launcher starts an agent process in dir with argv, sending its output
// to logPath. Tests substitute fakes; production uses ExecLauncher.
type Launcher interface {
	Start(ctx context.Context, dir string, argv []string, logPath string) (Process, error)
}

// Outcome of one dispatched issue in one pass.
type Outcome string

const (
	OutcomeCompleted Outcome = "completed" // issue closed on GitHub; cleaned up
	OutcomeRetry     Outcome = "retry"     // failed; stays ready for the next pass
	OutcomeBlocked   Outcome = "blocked"   // failed MaxAttempts times; labelled blocked
	OutcomeError     Outcome = "error"     // dispatch itself failed before/around launch
)

// Result describes one issue handled in a pass.
type Result struct {
	Issue    int
	Outcome  Outcome
	Attempts int
	Worktree string
	LogPath  string
	Err      error
}

// Running is an agent from an earlier pass (or an earlier `lead` process)
// that is still alive; it holds a parallel slot and is not re-dispatched.
type Running struct {
	Issue int
	PID   int
}

// Report is one pass.
type Report struct {
	Results []Result
	Running []Running
}

// Dispatcher wires the ports together. Store access is serialized with mu
// because *state.Store has no locking of its own.
type Dispatcher struct {
	Gh       ports.GhClient
	Git      workflow.GitRunner
	Store    *state.Store
	Launcher Launcher
	Opts     Options
	Out      io.Writer // pass summaries; nil = silent
	// ProcessAlive reports whether pid still exists on this host (reconnect,
	// RFC §7). Nil means signal 0 via the OS.
	ProcessAlive func(pid int) bool

	mu sync.Mutex
}

// Preflight is the safety gate of RFC §9 / issue #67: unattended agents
// merge their own PRs, so the repository must reject unreviewed pushes and
// failing CI on its own. It refuses (with the same guidance `lead doctor`
// prints) when the default branch is unprotected or has no required status
// checks; Opts.SkipProtectionCheck turns the refusal into a warning.
func (d *Dispatcher) Preflight(ctx context.Context) error {
	opts := d.Opts.withDefaults()
	repoRoot, err := d.Git.RepoRoot(opts.WorkDir)
	if err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	var missing []string
	if slug := git.RepoSlug(d.Git.OriginURL(repoRoot)); slug == "" {
		missing = []string{"no GitHub origin remote; agents cannot open pull requests from here"}
	} else {
		p, err := doctor.InspectProtection(ctx, d.Gh, slug)
		if err != nil {
			return fmt.Errorf("dispatch: preflight: %w", err)
		}
		missing = p.Missing()
	}
	if len(missing) == 0 {
		return nil
	}
	if opts.SkipProtectionCheck {
		if d.Out != nil {
			fmt.Fprintln(d.Out, "dispatch: WARNING: repository is not protected; proceeding because of --skip-protection-check")
			for _, m := range missing {
				fmt.Fprintln(d.Out, "  - "+m)
			}
		}
		return nil
	}
	var b strings.Builder
	b.WriteString("dispatch: refusing to start unattended agents: the repository would accept their merges unchecked\n")
	for _, m := range missing {
		b.WriteString("  - " + m + "\n")
	}
	b.WriteString("fix the repository settings (see `lead doctor`), or pass --skip-protection-check to proceed anyway")
	return errors.New(b.String())
}

// Once runs a single dispatch pass: reconnect to agents left running by an
// earlier pass, settle the ones that have gone, then hand out every ready
// issue through a pool of Parallel slots (minus the reconnected ones) and
// return once the picked issues are all settled.
func (d *Dispatcher) Once(ctx context.Context) (Report, error) {
	opts := d.Opts.withDefaults()
	running, settled, err := d.reconnect(ctx, opts)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Results: settled, Running: running}
	skip := map[int]bool{}
	for _, r := range running {
		skip[r.Issue] = true
	}
	for _, r := range settled {
		skip[r.Issue] = true // settled this pass; a retry waits for the next one
	}

	ready, err := d.Gh.ListByLabel(ctx, opts.ReadyLabel)
	if err != nil {
		return Report{}, fmt.Errorf("dispatch: list %s issues: %w", opts.ReadyLabel, err)
	}
	var picks []int
	for _, s := range ready {
		if skip[s.Number] {
			continue
		}
		w, ok, err := d.Store.Get(s.Number, "")
		if err != nil {
			return Report{}, fmt.Errorf("dispatch: %w", err)
		}
		if ok && w.Status == state.StatusBlocked {
			continue // local blocked state wins until a human re-readies it
		}
		picks = append(picks, s.Number)
	}
	rep.Results = append(rep.Results, d.runPool(ctx, opts, picks, opts.Parallel-len(running))...)
	if ctx.Err() == nil {
		d.print(rep)
	}
	return rep, nil
}

// runPool starts picks through slots workers: the next issue starts as soon
// as one finishes (no batch-then-wait). ctx cancellation stops new starts.
// On cancel it does not wait for already-started agents: they keep running
// and are never killed (RFC §7).
func (d *Dispatcher) runPool(ctx context.Context, opts Options, picks []int, slots int) []Result {
	if slots <= 0 || len(picks) == 0 {
		return nil
	}
	results := make([]Result, len(picks))
	sem := make(chan struct{}, slots)
	var wg sync.WaitGroup
	started := 0
	for i, n := range picks {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			i = len(picks) // stop starting
		}
		if i == len(picks) {
			break
		}
		started++
		wg.Add(1)
		go func(i, n int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = d.runOne(ctx, opts, n)
		}(i, n)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return results[:started]
	case <-ctx.Done():
		return []Result{}
	}
}

// reconnect scans workflows.json for this repository's records with a
// recorded PID (RFC §7: closing the inbox must not kill agents, reopening
// it must pick them up). Alive processes are reported as Running; gone
// ones have PID cleared and go through the normal evaluation (closed →
// cleanup, open → failed attempt).
func (d *Dispatcher) reconnect(ctx context.Context, opts Options) ([]Running, []Result, error) {
	repoRoot, err := d.Git.RepoRoot(opts.WorkDir)
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch: %w", err)
	}
	key := repoKey(d.Git.OriginURL(repoRoot))
	d.mu.Lock()
	all, err := d.Store.List()
	d.mu.Unlock()
	if err != nil {
		return nil, nil, fmt.Errorf("dispatch: %w", err)
	}
	var running []Running
	var settled []Result
	for _, w := range all {
		if w.PID <= 0 || w.Part != "" || repoKey(w.Repository) != key {
			continue
		}
		if d.alive(w.PID) {
			running = append(running, Running{Issue: w.Issue, PID: w.PID})
			continue
		}
		gone := fmt.Errorf("agent process %d is gone without closing the issue", w.PID)
		settled = append(settled, d.evaluate(ctx, opts, w.Issue, repoRoot, w.Worktree, w.LogPath, gone))
	}
	return running, settled, nil
}

func (d *Dispatcher) alive(pid int) bool {
	if d.ProcessAlive != nil {
		return d.ProcessAlive(pid)
	}
	return processAlive(pid)
}

// processAlive sends signal 0: success or EPERM means the pid exists.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// repoKey normalizes what workflow.Start stores in Workflow.Repository so
// records of this repository can be matched whatever URL form origin had.
func repoKey(origin string) string {
	if s := git.RepoSlug(origin); s != "" {
		return s
	}
	if origin == "" {
		return "local"
	}
	return origin
}

// Loop repeats Once every interval until ctx is cancelled. Agents already
// running are not killed on cancel (RFC §7: closing the inbox must not kill
// work); Once returns only after its own batch has finished.
func (d *Dispatcher) Loop(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	for {
		if _, err := d.Once(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// RunningCount returns the number of in-progress workflows for the current
// repository. It is safe to call from the UI while a dispatch pass is running.
func (d *Dispatcher) RunningCount() (int, error) {
	opts := d.Opts.withDefaults()
	repoRoot, err := d.Git.RepoRoot(opts.WorkDir)
	if err != nil {
		return 0, err
	}
	key := repoKey(d.Git.OriginURL(repoRoot))
	d.mu.Lock()
	all, err := d.Store.List()
	d.mu.Unlock()
	if err != nil {
		return 0, err
	}
	var count int
	for _, w := range all {
		if w.Status != state.StatusInProgress || w.Part != "" {
			continue
		}
		if repoKey(w.Repository) != key {
			continue
		}
		count++
	}
	return count, nil
}

func (d *Dispatcher) runOne(ctx context.Context, opts Options, number int) Result {
	res := Result{Issue: number}
	iss, err := d.Gh.View(ctx, number)
	if err != nil {
		res.Outcome, res.Err = OutcomeError, fmt.Errorf("#%d: view: %w", number, err)
		return res
	}

	d.mu.Lock()
	start, err := workflow.Start(ctx, d.Git, d.Store, workflow.StartOptions{
		Issue: iss, Mode: state.ModeImplement, Worktree: "auto", WorkDir: opts.WorkDir,
	})
	d.mu.Unlock()
	if err != nil {
		res.Outcome, res.Err = OutcomeError, err
		return res
	}
	res.Worktree = start.Worktree

	argv, err := agent.HeadlessArgv(opts.Agent, BuildPrompt(iss))
	if err != nil {
		res.Outcome, res.Err = OutcomeError, fmt.Errorf("#%d: %w", number, err)
		return res
	}
	logPath := filepath.Join(opts.LogDir, fmt.Sprintf("issue-%d-%s.log", number, time.Now().UTC().Format("20060102T150405Z")))
	res.LogPath = logPath

	proc, err := d.Launcher.Start(ctx, start.Worktree, argv, logPath)
	if err != nil {
		res.Outcome, res.Err = OutcomeError, fmt.Errorf("#%d: launch %s: %w", number, opts.Agent, err)
		return res
	}
	if err := d.update(number, func(w *state.Workflow) {
		w.Agent, w.PID, w.LogPath = opts.Agent, proc.Pid(), logPath
	}); err != nil {
		res.Outcome, res.Err = OutcomeError, err
		return res
	}

	waitErr := proc.Wait()
	if ctx.Err() != nil {
		return Result{Issue: number, Worktree: start.Worktree, LogPath: logPath, Outcome: OutcomeRetry}
	}
	return d.evaluate(ctx, opts, number, start.RepoRoot, start.Worktree, logPath, waitErr)
}

// evaluate settles an issue whose agent is no longer running: closed on
// GitHub → remove worktree and record (completed); otherwise count the
// attempt and, at MaxAttempts, block it. waitErr is the agent's exit error
// (nil = exited cleanly but left the issue open).
func (d *Dispatcher) evaluate(ctx context.Context, opts Options, number int, repoRoot, worktree, logPath string, waitErr error) Result {
	res := Result{Issue: number, Worktree: worktree, LogPath: logPath}

	after, viewErr := d.Gh.View(ctx, number)
	if viewErr == nil && strings.EqualFold(after.State, "CLOSED") {
		d.mu.Lock()
		defer d.mu.Unlock()
		if worktree != "" {
			if err := d.Git.WorktreeRemove(repoRoot, worktree, true); err != nil {
				res.Outcome, res.Err = OutcomeError, fmt.Errorf("#%d: closed, but worktree cleanup failed: %w", number, err)
				return res
			}
		}
		if _, err := d.Store.Delete(number, ""); err != nil {
			res.Outcome, res.Err = OutcomeError, err
			return res
		}
		res.Outcome = OutcomeCompleted
		return res
	}

	cause := "agent exited without closing the issue"
	switch {
	case waitErr != nil:
		cause = waitErr.Error()
	case viewErr != nil:
		cause = "could not re-check issue state: " + viewErr.Error()
	}
	res.Err = fmt.Errorf("#%d: %s", number, cause)

	var attempts int
	if err := d.update(number, func(w *state.Workflow) {
		w.Attempts++
		w.PID = 0
		attempts = w.Attempts
	}); err != nil {
		res.Outcome, res.Err = OutcomeError, err
		return res
	}
	res.Attempts = attempts
	if attempts < opts.MaxAttempts {
		res.Outcome = OutcomeRetry
		return res
	}

	if err := d.block(ctx, opts, number, attempts, cause, logPath, worktree); err != nil {
		res.Outcome, res.Err = OutcomeError, err
		return res
	}
	res.Outcome = OutcomeBlocked
	return res
}

// block swaps ready→blocked on GitHub, posts the cause, and marks local state.
func (d *Dispatcher) block(ctx context.Context, opts Options, number, attempts int, cause, logPath, worktree string) error {
	if err := d.Gh.IssueRemoveLabel(ctx, number, opts.ReadyLabel); err != nil {
		return fmt.Errorf("#%d: remove label %s: %w", number, opts.ReadyLabel, err)
	}
	if err := d.Gh.IssueAddLabel(ctx, number, opts.BlockedLabel); err != nil {
		return fmt.Errorf("#%d: add label %s: %w", number, opts.BlockedLabel, err)
	}
	body := fmt.Sprintf(`lead dispatch: %d 回連続で完了（PR マージ・Issue クローズ）まで到達できなかったため `+"`%s`"+` にしました。

- エージェント: %s
- 最終エラー: %s
- ログ: %s
- worktree: %s

原因を直すか方針をこの Issue にコメントしてから `+"`%s`"+` ラベルを戻すと再走します。`,
		attempts, opts.BlockedLabel, opts.Agent, cause, logPath, worktree, opts.ReadyLabel)
	if err := d.Gh.IssueComment(ctx, number, body); err != nil {
		return fmt.Errorf("#%d: comment: %w", number, err)
	}
	return d.update(number, func(w *state.Workflow) { w.Status = state.StatusBlocked })
}

// update applies fn to the stored record under the dispatcher lock.
func (d *Dispatcher) update(number int, fn func(*state.Workflow)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok, err := d.Store.Get(number, "")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("#%d: state record vanished", number)
	}
	fn(&w)
	return d.Store.Upsert(w)
}

func (d *Dispatcher) print(rep Report) {
	if d.Out == nil {
		return
	}
	for _, r := range rep.Running {
		fmt.Fprintf(d.Out, "#%d running (pid %d, reconnected)\n", r.Issue, r.PID)
	}
	if len(rep.Results) == 0 {
		if len(rep.Running) == 0 {
			fmt.Fprintln(d.Out, "dispatch: no ready issues")
		}
		return
	}
	for _, r := range rep.Results {
		line := fmt.Sprintf("#%d %s", r.Issue, r.Outcome)
		if r.Attempts > 0 {
			line += fmt.Sprintf(" (attempt %d)", r.Attempts)
		}
		if r.Err != nil {
			line += ": " + r.Err.Error()
		}
		if r.LogPath != "" {
			line += "  log=" + r.LogPath
		}
		fmt.Fprintln(d.Out, line)
	}
}

// BuildPrompt is the headless launch prompt: the issue itself plus the
// completion definition the agent must satisfy alone (AGENTS.md flow,
// through merge and close). Project rules files are read by the agent CLI
// itself and are not inlined.
func BuildPrompt(iss ports.Issue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Issue #%d: %s\n", iss.Number, iss.Title)
	if body := strings.TrimSpace(iss.Body); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	fmt.Fprintf(&b, `
---
## 完了定義（lead dispatch）

この Issue はヘッドレスで担当している。途中で人間の確認は入らない。次をすべて自分で完了させること。

1. 現在のディレクトリ（この Issue 専用の worktree・ブランチ）で作業する。main には直接 push しない。
2. TDD: 先に失敗するテストを追加して Red を確認し、実装して Green にする。
3. リポジトリのテストをすべてパスさせる（./test/run-tests.sh、なければ go test ./... 等プロジェクト固有のコマンド）。
4. git push -u origin <現在のブランチ> し、gh pr create --fill --base main で PR を作る。PR 本文に Issue の受入条件をチェックリストとして転記し、Closes #%d を含める。
5. bin/ci-wait <PR番号>（なければ gh pr checks）で CI が全て pass するまで待つ。
6. gh pr merge --squash --delete-branch でマージする。
7. gh issue view %d --json state で closed になったことを確認する。closed でなければ gh issue close %d する。
8. 進められない場合は原因と試したことを gh issue comment %d --body に書いて終了する。

AGENTS.md、.claude/、.devin/ などプロジェクト規約ファイルは、この Issue で明示されていない限り編集しない。
`, iss.Number, iss.Number, iss.Number, iss.Number)
	return b.String()
}

// ExecLauncher runs the agent binary as a child process. Output is
// appended to logPath; stdin is /dev/null so a prompt for input fails
// fast instead of hanging. exec.Command (not CommandContext) on purpose:
// cancelling lead must not kill a running agent (RFC §7).
type ExecLauncher struct {
	LookPath func(name string) (string, error)
}

var _ Launcher = (*ExecLauncher)(nil)

// Start implements Launcher.
func (l *ExecLauncher) Start(ctx context.Context, dir string, argv []string, logPath string) (Process, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("dispatch: empty argv")
	}
	lookPath := l.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath(argv[0]); err != nil {
		return nil, &ports.BinaryNotFoundError{Binary: argv[0]}
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("dispatch: log dir: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("dispatch: open log: %w", err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, fmt.Errorf("dispatch: start %s: %w", argv[0], err)
	}
	return &execProcess{cmd: cmd, log: f}, nil
}

type execProcess struct {
	cmd *exec.Cmd
	log *os.File
}

func (p *execProcess) Pid() int { return p.cmd.Process.Pid }

func (p *execProcess) Wait() error {
	err := p.cmd.Wait()
	p.log.Close()
	return err
}
