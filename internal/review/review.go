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
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/kuwa72/lead-cli/internal/adapters/agent"
	"github.com/kuwa72/lead-cli/internal/ports"
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

// Options tunes one review run.
type Options struct {
	PR    int
	Issue int    // 0 = derive from the PR body, then the head branch
	Agent string // agents.review / agents.impl / agent; empty = agy
	// LogDir receives the reviewer's output log; required.
	LogDir string
	// WorkDir is the reviewer's working directory (read-only repository).
	WorkDir string
	// Repository is informational (owner/repo or URL) for the prompt.
	Repository string
}

// Runner wires the ports together.
type Runner struct {
	Gh    ports.GhClient
	Agent AgentRunner
	Opts  Options
}

// Result is one review run.
type Result struct {
	PR      int
	Issue   int
	Verdict string
	Passed  bool
	Output  string
	LogPath string
	Message string
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
	})
	if err != nil {
		return Result{}, err
	}
	argv, err := agent.HeadlessArgv(r.Opts.Agent, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("review: %w", err)
	}
	logPath := filepath.Join(r.Opts.LogDir, fmt.Sprintf("review-%d-%s.log", pr, time.Now().UTC().Format("20060102T150405Z")))
	out, runErr := r.Agent.Run(ctx, r.Opts.WorkDir, argv, logPath)
	res := Result{PR: pr, Issue: issueNumber, Output: string(out), LogPath: logPath}
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

	res.Verdict = verdict
	res.Passed = verdict == VerdictPass
	label := state.ReviewNeedsWorkLabel
	if res.Passed {
		label = state.ReviewPassLabel
		res.Message = fmt.Sprintf("review: PR #%d passed (%s)", pr, state.ReviewPassLabel)
	} else {
		res.Message = fmt.Sprintf("review: PR #%d needs work (%s)", pr, state.ReviewNeedsWorkLabel)
	}
	if err := r.publish(ctx, pr, res, label); err != nil {
		return res, err
	}
	return res, nil
}

// publish posts the full reviewer output to the PR and swaps the verdict
// label so a rerun cannot leave a stale review:pass behind.
func (r *Runner) publish(ctx context.Context, pr int, res Result, label string) error {
	if err := r.Gh.PrComment(ctx, pr, CommentBody(res)); err != nil {
		return fmt.Errorf("review: PR #%d: comment: %w", pr, err)
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
	return nil
}

// CommentBody renders the PR comment: a verdict header plus the reviewer's
// full output.
func CommentBody(res Result) string {
	return fmt.Sprintf("## lead review: PR #%d\n\nVERDICT: %s\n\n%s",
		res.PR, res.Verdict, strings.TrimRight(res.Output, "\n"))
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
