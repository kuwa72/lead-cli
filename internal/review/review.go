// Package review implements `lead review <pr>` (issue #246): an independent
// headless agent reviews a PR's diff, the issue's acceptance criteria, and
// CI results, then labels the PR review:pass or review:needs-work.
//
// The reviewer runs in a separate process from the implementation session
// (agent resolution: agents.review → agents.impl → agent). The verdict is
// the last non-empty line of the agent output and must be exactly
// `VERDICT: pass` or `VERDICT: needs-work`; anything else fails closed.
// The full output is posted to the PR as a comment.
package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/kuwa72/lead-cli/internal/adapters/agent"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/projinit"
	"github.com/kuwa72/lead-cli/internal/scenario"
	"github.com/kuwa72/lead-cli/internal/state"
)

const (
	VerdictPass      = "pass"
	VerdictNeedsWork = "needs-work"
	// VerdictInvalid marks output whose final line is not a known verdict.
	VerdictInvalid = "invalid"
)

var (
	// ErrNeedsWork is returned by the CLI path when the reviewer asks for
	// changes (exit 1).
	ErrNeedsWork = errors.New("review: VERDICT: needs-work")
	// ErrInvalidVerdict reports a missing or unrecognized verdict line.
	ErrInvalidVerdict = errors.New("review: verdict line missing or unrecognized")
)

// AgentRunner runs the reviewer to completion in dir with argv, teeing its
// output to logPath and returning stdout. It is satisfied by spec.ExecRunner.
type AgentRunner interface {
	Run(ctx context.Context, dir string, argv []string, logPath string) ([]byte, error)
}

// GitRunner is the subset of git operations the review worktree needs.
// *git.Runner and workflow.GitRunner satisfy it.
type GitRunner interface {
	// HeadSHA returns the commit checked out in dir.
	HeadSHA(dir string) (string, error)
	// MainWorktree returns the repository's main worktree (the one that
	// owns .worktrees/), even when dir is itself a linked worktree.
	MainWorktree(dir string) (string, error)
	// EnsureCommit makes sha available locally, fetching it from origin
	// when the checkout does not have it yet.
	EnsureCommit(repoDir, sha string) error
	// WorktreeAddDetached creates a detached worktree at commit.
	WorktreeAddDetached(repoDir, path, commit string) error
	WorktreeRemove(repoDir, path string, force bool) error
}

// Options tunes one review run.
type Options struct {
	PR    int
	Issue int    // 0 = derive from the PR body, then the head branch
	Agent string // agents.review / agents.impl / agent; empty = agy
	// LogDir receives the reviewer's output log and prompt file; required.
	LogDir string
	// WorkDir is the reviewer's working directory when it already matches
	// the PR head (read-only repository).
	WorkDir string
	// Repository is informational (owner/repo or URL) for the prompt.
	Repository string
	// RepoSlug is "owner/repo" for label creation; empty uses gh's cwd.
	RepoSlug string
	// StateDir locates the issue's scenario at
	// <StateDir>/scenarios/<owner>_<repo>/<issue>.md (issue #277).
	// Empty means no scenario: the review is diff-only (SCENARIOS: 0/0).
	StateDir string
}

// Runner wires the ports together.
type Runner struct {
	Gh    ports.GhClient
	Agent AgentRunner
	// Git creates the PR-head worktree when WorkDir is on another commit.
	// Nil keeps the reviewer in WorkDir.
	Git  GitRunner
	Opts Options
}

// Result is one review run.
type Result struct {
	PR      int
	Issue   int
	Verdict string
	Passed  bool
	// HeadSHA is the PR head the reviewer examined (issue #249). finish
	// merges only when the current head still matches it.
	HeadSHA string
	// ScenariosPassed/ScenariosTotal are the generated-test result; 0/0
	// when the issue has no scenario (issue #277).
	ScenariosPassed int
	ScenariosTotal  int
	Output          string
	LogPath         string
	Message         string
}

// Review gathers the PR/issue/CI context, runs the independent reviewer,
// publishes the verdict to the PR, and reports it.
func (r *Runner) Review(ctx context.Context) (Result, error) {
	if r.Opts.PR <= 0 {
		return Result{}, errors.New("review: PR number is required")
	}
	if r.Agent == nil {
		return Result{}, errors.New("review: no agent runner")
	}
	pr := r.Opts.PR

	body, err := r.Gh.PrBody(ctx, pr)
	if err != nil {
		return Result{}, fmt.Errorf("review: PR #%d: body: %w", pr, err)
	}
	issueNumber := r.Opts.Issue
	if issueNumber == 0 {
		issueNumber = IssueFromPR(body)
	}
	if issueNumber == 0 {
		if branch, err := r.Gh.PrHeadBranch(ctx, pr); err == nil {
			issueNumber = IssueFromBranch(branch)
		}
	}
	if issueNumber == 0 {
		return Result{}, fmt.Errorf("review: PR #%d: cannot determine the issue number from the PR body or branch; pass --issue", pr)
	}
	issue, err := r.Gh.View(ctx, issueNumber)
	if err != nil {
		return Result{}, fmt.Errorf("review: issue #%d: %w", issueNumber, err)
	}
	// Bind the review to the head before reading the diff: if a push lands
	// mid-gather, the marker keeps the older SHA and finish stops instead
	// of accepting a pass for a commit the reviewer never saw (issue #249).
	headSHA, err := r.Gh.PrHeadSHA(ctx, pr)
	if err != nil {
		return Result{}, fmt.Errorf("review: PR #%d: head SHA: %w", pr, err)
	}
	diff, err := r.Gh.PrDiff(ctx, pr)
	if err != nil {
		return Result{}, fmt.Errorf("review: PR #%d: diff: %w", pr, err)
	}
	checks, err := r.Gh.PrChecks(ctx, pr)
	if err != nil {
		return Result{}, fmt.Errorf("review: PR #%d: checks: %w", pr, err)
	}

	prompt, err := BuildPrompt(PromptInput{
		Repository: r.Opts.Repository,
		PR:         pr,
		Issue:      issue,
		PRBody:     body,
		Diff:       diff,
		Checks:     checks,
		Scenario:   r.scenarioText(issueNumber),
	})
	if err != nil {
		return Result{}, err
	}
	// The prompt goes through a file, never argv: a diff can exceed Linux's
	// MAX_ARG_STRLEN (128 KiB) and would otherwise fail with E2BIG (#250).
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.MkdirAll(r.Opts.LogDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("review: prompt dir: %w", err)
	}
	promptPath := filepath.Join(r.Opts.LogDir, fmt.Sprintf("review-%d-%s.prompt.md", pr, stamp))
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return Result{}, fmt.Errorf("review: write prompt: %w", err)
	}
	defer os.Remove(promptPath)
	argv, err := agent.HeadlessArgv(r.Opts.Agent, PromptInstruction(promptPath))
	if err != nil {
		return Result{}, fmt.Errorf("review: %w", err)
	}
	// Review the PR head, not whatever the caller's checkout happens to be
	// (#250). A cwd already at the head is reused; otherwise a temporary
	// worktree under <repo>/.worktrees/ is created and removed afterwards.
	runDir, cleanup, err := r.prepareWorktree(headSHA)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	logPath := filepath.Join(r.Opts.LogDir, fmt.Sprintf("review-%d-%s.log", pr, stamp))
	out, runErr := r.Agent.Run(ctx, runDir, argv, logPath)
	res := Result{PR: pr, Issue: issueNumber, HeadSHA: headSHA, Output: string(out), LogPath: logPath}
	if runErr != nil {
		res.Message = fmt.Sprintf("review: agent failed for PR #%d (log: %s)", pr, logPath)
		return res, fmt.Errorf("review: PR #%d: %w", pr, runErr)
	}

	verdict, ok := ParseVerdict(out)
	if !ok {
		res.Verdict = VerdictInvalid
		res.Message = fmt.Sprintf("review: PR #%d: verdict line missing or unrecognized; treated as fail (log: %s)", pr, logPath)
		if pubErr := r.publish(ctx, pr, res, state.ReviewNeedsWorkLabel); pubErr != nil {
			return res, fmt.Errorf("%s; %w", res.Message, pubErr)
		}
		return res, fmt.Errorf("%s: %w", res.Message, ErrInvalidVerdict)
	}

	// The generated scenario tests are the main gate; the diff review stays
	// advisory (issue #277). A scenario harness failure fails closed.
	scRes, scErr := r.runScenario(ctx, issueNumber, runDir)
	passed, total := scRes.Passed, scRes.Total
	if total == 0 {
		if p, t, ok := ParseScenarioScore(out); ok {
			passed, total = p, t
		}
	}
	res.Verdict = verdict
	res.ScenariosPassed, res.ScenariosTotal = passed, total
	res.Passed = verdict == VerdictPass && scErr == nil && (total == 0 || passed == total)
	if !res.Passed && verdict == VerdictPass {
		res.Verdict = VerdictNeedsWork
	}
	if scErr != nil {
		res.Output = strings.TrimRight(string(out), "\n") + "\n\n## シナリオ実行\n\ngenerated tests failed to run: " + scErr.Error()
	} else if total > 0 {
		res.Output = strings.TrimRight(string(out), "\n") + "\n\n" + scenarioSummary(scRes)
	}
	label := state.ReviewNeedsWorkLabel
	if res.Passed {
		label = state.ReviewPassLabel
		res.Message = fmt.Sprintf("review: PR #%d passed (%s, scenarios %d/%d)", pr, state.ReviewPassLabel, passed, total)
	} else {
		res.Message = fmt.Sprintf("review: PR #%d needs work (%s, scenarios %d/%d)", pr, state.ReviewNeedsWorkLabel, passed, total)
	}
	if err := r.publish(ctx, pr, res, label); err != nil {
		return res, err
	}
	if scErr != nil {
		return res, fmt.Errorf("review: PR #%d: scenario: %w", pr, scErr)
	}
	return res, nil
}

// scenarioPath is the issue's scenario outside the worktree, or "" when the
// caller did not configure a state dir.
func (r *Runner) scenarioPath(issue int) string {
	if r.Opts.StateDir == "" {
		return ""
	}
	return scenario.Path(r.Opts.StateDir, r.Opts.RepoSlug, issue)
}

// scenarioText reads the scenario for the reviewer prompt. A missing or
// unreadable file is not an error here: runScenario reports it when it runs.
func (r *Runner) scenarioText(issue int) string {
	path := r.scenarioPath(issue)
	if path == "" {
		return ""
	}
	raw, err := scenario.Load(path)
	if err != nil {
		return ""
	}
	return raw
}

// runScenario generates and runs throwaway tests at the PR head. No scenario
// configured yields the zero Result (0/0), never an error.
func (r *Runner) runScenario(ctx context.Context, issue int, runDir string) (scenario.Result, error) {
	path := r.scenarioPath(issue)
	if path == "" {
		return scenario.Result{}, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return scenario.Result{}, nil
	}
	s := &scenario.Runner{Agent: r.Agent, Opts: scenario.Options{
		Issue: issue, ScenarioPath: path, WorkDir: runDir,
		Agent: r.Opts.Agent, LogDir: r.Opts.LogDir,
	}}
	return s.Run(ctx)
}

// scenarioSummary renders the per-step pass/fail lines the PR comment keeps.
func scenarioSummary(res scenario.Result) string {
	var b strings.Builder
	b.WriteString("## シナリオ実行\n\n")
	for _, s := range res.Steps {
		fmt.Fprintf(&b, "- SCENARIO %d: %s — %s\n", s.Number, s.Verdict, s.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// PromptInstruction is the short argv prompt that points the reviewer at the
// prompt file. Keeping the full prompt out of argv avoids E2BIG on large
// diffs (#250).
func PromptInstruction(path string) string {
	return "Read and follow the review instructions in this file: " + path
}

// prepareWorktree returns the directory the reviewer runs in and a cleanup
// function. When the caller's checkout is not the PR head it creates a
// detached worktree under <main worktree>/.worktrees/review-pr<n> (#250).
// /tmp is not usable as the workspace for every agent (devin rejects it as
// untrusted), so the worktree lives inside the repository.
func (r *Runner) prepareWorktree(headSHA string) (string, func(), error) {
	noop := func() {}
	if r.Git == nil || r.Opts.WorkDir == "" {
		return r.Opts.WorkDir, noop, nil
	}
	head, err := r.Git.HeadSHA(r.Opts.WorkDir)
	if err != nil {
		return "", noop, fmt.Errorf("review: HEAD of %s: %w", r.Opts.WorkDir, err)
	}
	if strings.EqualFold(head, headSHA) {
		return r.Opts.WorkDir, noop, nil
	}
	root, err := r.Git.MainWorktree(r.Opts.WorkDir)
	if err != nil {
		return "", noop, fmt.Errorf("review: main worktree: %w", err)
	}
	if err := r.Git.EnsureCommit(root, headSHA); err != nil {
		return "", noop, fmt.Errorf("review: PR head %s is not available: %w", headSHA, err)
	}
	dir := filepath.Join(root, ".worktrees", fmt.Sprintf("review-pr%d", r.Opts.PR))
	// An interrupted earlier run may have left this path registered.
	if err := r.Git.WorktreeRemove(root, dir, true); err != nil {
		return "", noop, fmt.Errorf("review: remove stale worktree %s: %w", dir, err)
	}
	if err := r.Git.WorktreeAddDetached(root, dir, headSHA); err != nil {
		return "", noop, fmt.Errorf("review: create worktree at %s: %w", dir, err)
	}
	return dir, func() { _ = r.Git.WorktreeRemove(root, dir, true) }, nil
}

// publish swaps the verdict label and then posts the full reviewer output
// (which carries the authoritative head-SHA marker) to the PR. The label
// goes first so a failed label swap never leaves a marker that finish would
// accept; a failed comment leaves only the display label, which finish
// ignores.
func (r *Runner) publish(ctx context.Context, pr int, res Result, label string) error {
	if err := r.ensureVerdictLabel(ctx, label); err != nil {
		return err
	}
	labels, err := r.Gh.PrLabels(ctx, pr)
	if err != nil {
		return fmt.Errorf("review: PR #%d: labels: %w", pr, err)
	}
	other := state.ReviewNeedsWorkLabel
	if label == state.ReviewNeedsWorkLabel {
		other = state.ReviewPassLabel
	}
	if slices.Contains(labels, other) {
		if err := r.Gh.IssueRemoveLabel(ctx, pr, other); err != nil {
			return fmt.Errorf("review: PR #%d: remove label %s: %w", pr, other, err)
		}
	}
	if !slices.Contains(labels, label) {
		if err := r.Gh.IssueAddLabel(ctx, pr, label); err != nil {
			return fmt.Errorf("review: PR #%d: add label %s: %w", pr, label, err)
		}
	}
	if err := r.Gh.PrComment(ctx, pr, CommentBody(res)); err != nil {
		return fmt.Errorf("review: PR #%d: comment: %w", pr, err)
	}
	return nil
}

// ensureVerdictLabel creates the verdict label when the repository does not
// have it yet (#250). A failure stops before the PR comment so a review
// never leaves a comment without its label.
func (r *Runner) ensureVerdictLabel(ctx context.Context, label string) error {
	def, ok := verdictLabelDefinition(label)
	if !ok {
		return fmt.Errorf("review: unknown verdict label %q", label)
	}
	labels, err := r.Gh.RepoLabels(ctx, r.Opts.RepoSlug)
	if err != nil {
		return fmt.Errorf("review: repository labels: %w", err)
	}
	if slices.Contains(labels, label) {
		return nil
	}
	if err := r.Gh.RepoCreateLabel(ctx, r.Opts.RepoSlug, def); err != nil {
		return fmt.Errorf("review: create label %s: %w", label, err)
	}
	return nil
}

// verdictLabelDefinition returns the canonical definition of a verdict
// label, shared with `lead enable` so colors and descriptions stay in sync.
func verdictLabelDefinition(name string) (ports.LabelDefinition, bool) {
	for _, def := range projinit.RequiredIssueLabels {
		if def.Name == name {
			return def, true
		}
	}
	return ports.LabelDefinition{}, false
}

// CommentBody renders the PR comment: a machine-readable verdict marker
// (head SHA + verdict, issue #249), a verdict header, and the reviewer's
// full output.
func CommentBody(res Result) string {
	return fmt.Sprintf("%s\n## lead review: PR #%d\n\nVERDICT: %s\nSCENARIOS: %d/%d\n\n%s",
		MarkerLine(res.HeadSHA, res.Verdict, fmt.Sprintf("%d/%d", res.ScenariosPassed, res.ScenariosTotal)),
		res.PR, res.Verdict, res.ScenariosPassed, res.ScenariosTotal, strings.TrimRight(res.Output, "\n"))
}

// MarkerLine renders the marker finish reads back:
// `<!-- lead-review sha=<40 hex> verdict=<verdict> scenarios=<n/m> -->`.
func MarkerLine(sha, verdict, scenarios string) string {
	return fmt.Sprintf("<!-- lead-review sha=%s verdict=%s scenarios=%s -->", sha, verdict, scenarios)
}

// Marker is one parsed review verdict marker.
type Marker struct {
	SHA       string
	Verdict   string
	Scenarios string // "passed/total"; empty on markers from before #277
}

// markerPattern matches a review marker. The verdict is captured verbatim
// so an unknown verdict (including "invalid") still supersedes an older
// pass; finish treats anything but pass as blocking.
var markerPattern = regexp.MustCompile(`<!--\s*lead-review\s+sha=([0-9a-fA-F]{40})\s+verdict=([a-z-]+)(?:\s+scenarios=(\d+/\d+))?\s*-->`)

// LatestMarker returns the newest review marker across the PR's comments.
// Comments come oldest-first from gh, so the scan runs backwards.
func LatestMarker(comments []ports.Comment) (Marker, bool) {
	return LatestMarkerFor(comments, "")
}

// LatestMarkerFor is LatestMarker restricted to author's comments when
// author is non-empty (issue #250). Comments without an author are still
// accepted for compatibility with older gh output.
//
// Only the first marker in a comment counts: CommentBody writes the real
// verdict first, so a reviewer quoting a marker from the diff cannot turn
// a needs-work into a pass.
func LatestMarkerFor(comments []ports.Comment, author string) (Marker, bool) {
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		if author != "" && c.Author != "" && !strings.EqualFold(c.Author, author) {
			continue
		}
		m := markerPattern.FindStringSubmatch(c.Body)
		if m == nil {
			continue
		}
		return Marker{SHA: strings.ToLower(m[1]), Verdict: m[2], Scenarios: m[3]}, true
	}
	return Marker{}, false
}

// ParseVerdict reads only the last non-empty line. Anything but the two
// exact verdict lines is invalid: ambiguous output never counts as pass.
func ParseVerdict(out []byte) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		switch line {
		case "VERDICT: pass":
			return VerdictPass, true
		case "VERDICT: needs-work":
			return VerdictNeedsWork, true
		default:
			return "", false
		}
	}
	return "", false
}

// scenarioScorePattern matches a `SCENARIOS: n/m` line in reviewer output.
var scenarioScorePattern = regexp.MustCompile(`(?m)^SCENARIOS:\s*(\d+)\s*/\s*(\d+)\s*$`)

// ParseScenarioScore reads the last `SCENARIOS: n/m` line the reviewer
// printed. It is a fallback for reviewers whose scenario runner is external.
func ParseScenarioScore(out []byte) (passed, total int, ok bool) {
	all := scenarioScorePattern.FindAllSubmatch(out, -1)
	if len(all) == 0 {
		return 0, 0, false
	}
	m := all[len(all)-1]
	p, err1 := strconv.Atoi(string(m[1]))
	t, err2 := strconv.Atoi(string(m[2]))
	if err1 != nil || err2 != nil || t < 0 || p > t {
		return 0, 0, false
	}
	return p, t, true
}

var (
	prCloserPattern = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+#(\d+)`)
	branchPattern   = regexp.MustCompile(`^issue/(\d+)(?:-|$)`)
)

// IssueFromPR extracts the issue number from a closing keyword in the PR
// body (`Closes #246`, `Fixes #12`, ...). 0 means none.
func IssueFromPR(body string) int {
	m := prCloserPattern.FindStringSubmatch(body)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// IssueFromBranch extracts the issue number from an `issue/<n>-<slug>`
// branch name. 0 means none.
func IssueFromBranch(branch string) int {
	m := branchPattern.FindStringSubmatch(strings.TrimSpace(branch))
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// PromptInput feeds BuildPrompt.
type PromptInput struct {
	Repository string
	PR         int
	Issue      ports.Issue
	PRBody     string
	Diff       string
	Checks     []ports.PRCheck
	// Scenario is the issue's scenario; the reviewer sees what the
	// generated tests check (issue #277).
	Scenario string
}

const promptTemplate = `あなたは独立レビュアーです。実装セッションとは別のプロセスとして、PRを審査します。

## 制約
- コードは読み取り専用です。ファイルの編集・作成・削除、コミット、push、マージ、ラベル操作を行わないこと。
- 検証に必要な読み取りコマンド（git diff、go test など）だけを実行してよい。書き込みを伴うコマンドは実行しない。
- 実装者の自己申告を鵜呑みにせず、diff・テスト・実行ログから自分で判断すること。

## リポジトリ
{{.Repository}}

## 対象
- PR #{{.PR}}
- Issue #{{.Issue.Number}}: {{.Issue.Title}}

### Issue 本文（受入条件を含む）
{{.Issue.Body}}

### PR 本文
{{.PRBody}}

### CI 結果
{{range .Checks}}- {{.Name}}: {{.Bucket}} ({{.State}})
{{else}}- (CI 結果なし)
{{end}}{{if .Scenario}}
### シナリオ（worktree 外に保存され、実装者は編集できない）
{{.Scenario}}

シナリオの生成テストは lead が PR head で別途実行する。あなたは diff の所見に集中し、
シナリオの合否を覆さないこと。
{{end}}
### PR diff
{{.Diff}}

## 審査観点（固定チェックリスト）
各観点について OK / 問題あり を明記し、問題にはファイル・行・根拠を示すこと。
1. 受入条件を実際に検証したか（PR本文やコメントのコマンド実行ログを見る）
2. スコープ外の差分が無いか
3. テストが実装の写経になっていないか（振る舞いをアサートしているか）
4. エラーハンドリング・入力検証
5. 秘密情報・デバッグ残骸・コメントアウト
6. マイグレーションの後方互換
7. 新規依存の妥当性
8. 「ついで」の変更が混ざっていないか

## 出力形式
- Markdown で所見を書く。前後に説明文を付けない。
- 最終行を必ず次のどちらか一方だけにすること。空白やコードフェンスを付けない。
VERDICT: pass
VERDICT: needs-work
- 判断に迷う場合は needs-work にすること。曖昧なまま pass にしない。
`

// BuildPrompt renders the reviewer prompt. Repository defaults to "local".
func BuildPrompt(in PromptInput) (string, error) {
	if in.PR <= 0 {
		return "", errors.New("review: PR number is required")
	}
	if strings.TrimSpace(in.Repository) == "" {
		in.Repository = "local"
	}
	tpl, err := template.New("review").Parse(promptTemplate)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := tpl.Execute(&sb, in); err != nil {
		return "", err
	}
	return sb.String(), nil
}
