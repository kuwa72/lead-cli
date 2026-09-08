// Package spec is the 仕様 AI (docs/rfc-inbox-ux.md §8, issue #94):
// `lead say "<one-liner>"` runs a spec agent headlessly and files the
// resulting issue drafts with `gh issue create --label needs-review`.
//
// Flow: build a prompt (one-liner + repository rules + optional parent
// issue or redraft target) → run the agent via agent.HeadlessArgv with its
// output captured to a log file → parse the JSON it prints → lint titles
// with prompter.LintTitle → create (or edit) issues through ports.GhClient.
// GitHub is the only state: nothing is written besides the agent log.
package spec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/kuwa72/lead-cli/internal/adapters/agent"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/prompter"
)

// DefaultLabel marks issues awaiting human approval (RFC §8, §13).
const DefaultLabel = "needs-review"

// DefaultRules is used when the working directory has no AGENTS.md.
const DefaultRules = "TDD必須。テストなき実装・PRは受け付けない。"

// Draft is one issue the agent proposes (JSON shape it must print).
type Draft struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// AgentRunner runs the spec agent to completion in dir with argv, tees
// its stdout/stderr to logPath, and returns stdout. Tests inject fakes.
type AgentRunner interface {
	Run(ctx context.Context, dir string, argv []string, logPath string) ([]byte, error)
}

// Options tunes one Runner.
type Options struct {
	Agent      string // spec agent (agents.spec); empty = agent.DefaultAgent
	Label      string // default needs-review
	LogDir     string // agent output files; required unless DryRun with a fake
	WorkDir    string // repository the one-liner is about (AGENTS.md source)
	Repository string // informational slug/URL for the prompt; "" = local
	Rules      string // repository rules; "" = read WorkDir/AGENTS.md, else DefaultRules
	DryRun     bool   // print would-be issues, no gh mutation
	FollowUp   int    // 実機 NG → 追い Issue: reference this parent issue
	Redraft    int    // rewrite this issue's body instead of creating
}

func (o Options) withDefaults() (Options, error) {
	o.Agent = agent.Resolve(o.Agent)
	if o.Label == "" {
		o.Label = DefaultLabel
	}
	if o.Repository == "" {
		o.Repository = "local"
	}
	if o.FollowUp < 0 || o.Redraft < 0 {
		return o, errors.New("say: issue numbers must be positive")
	}
	if o.FollowUp > 0 && o.Redraft > 0 {
		return o, errors.New("say: --follow-up and --redraft are mutually exclusive")
	}
	if strings.TrimSpace(o.Rules) == "" {
		o.Rules = loadRules(o.WorkDir)
	}
	return o, nil
}

func loadRules(dir string) string {
	if dir == "" {
		return DefaultRules
	}
	b, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return DefaultRules
	}
	return strings.TrimSpace(string(b))
}

// Result is what one Say produced.
type Result struct {
	Drafts    []Draft          // parsed and linted drafts (create mode), or the single redraft
	Created   []ports.IssueRef // created issues (empty in dry-run / redraft)
	Redrafted int              // issue edited in redraft mode
	LogPath   string           // agent output log
}

// Runner wires the ports together.
type Runner struct {
	Gh    ports.GhClient
	Agent AgentRunner
	Opts  Options
	Out   io.Writer // human-readable progress; nil = silent
}

// Say turns oneLiner into issues (or a redraft) per Opts.
func (r *Runner) Say(ctx context.Context, oneLiner string) (Result, error) {
	oneLiner = strings.TrimSpace(oneLiner)
	if oneLiner == "" {
		return Result{}, errors.New("say: the one-liner is empty")
	}
	opts, err := r.Opts.withDefaults()
	if err != nil {
		return Result{}, err
	}
	if opts.Redraft > 0 {
		return r.redraft(ctx, opts, oneLiner)
	}
	return r.create(ctx, opts, oneLiner)
}

func (r *Runner) create(ctx context.Context, opts Options, oneLiner string) (Result, error) {
	in := PromptInput{Repository: opts.Repository, OneLiner: oneLiner, Rules: opts.Rules}
	if opts.FollowUp > 0 {
		parent, err := r.Gh.View(ctx, opts.FollowUp)
		if err != nil {
			return Result{}, fmt.Errorf("say: follow-up #%d: %w", opts.FollowUp, err)
		}
		in.Parent = &parent
	}
	prompt, err := BuildPrompt(in)
	if err != nil {
		return Result{}, err
	}
	out, logPath, err := r.runAgent(ctx, opts, prompt)
	res := Result{LogPath: logPath}
	if err != nil {
		return res, err
	}
	drafts, err := ParseDrafts(out)
	if err != nil {
		return res, fmt.Errorf("say: agent output (see %s): %w", logPath, err)
	}
	if err := Validate(drafts); err != nil {
		return res, fmt.Errorf("say: %w", err)
	}
	if opts.FollowUp > 0 {
		for i := range drafts {
			drafts[i].Body = EnsureReference(drafts[i].Body, opts.FollowUp)
		}
	}
	res.Drafts = drafts
	labels := []string{opts.Label}

	if opts.DryRun {
		for i, d := range drafts {
			r.printf("[dry-run] would create issue %d/%d\n  title: %s\n  labels: %s\n---\n%s\n---\n",
				i+1, len(drafts), d.Title, strings.Join(labels, ","), strings.TrimRight(d.Body, "\n"))
		}
		return res, nil
	}

	for _, d := range drafts {
		ref, err := r.Gh.IssueCreate(ctx, d.Title, d.Body, labels)
		if err != nil {
			return res, fmt.Errorf("say: create %q: %w", d.Title, err)
		}
		res.Created = append(res.Created, ref)
		r.printf("created #%d %s  %s\n", ref.Number, ref.URL, d.Title)
	}
	// Siblings cross-reference each other in the body (RFC §8: 親子は本文で相互参照).
	if len(res.Created) > 1 {
		for i, d := range drafts {
			body := d.Body + siblingFooter(res.Created, i)
			if err := r.Gh.IssueEdit(ctx, res.Created[i].Number, d.Title, body); err != nil {
				return res, fmt.Errorf("say: cross-reference #%d: %w", res.Created[i].Number, err)
			}
		}
	}
	return res, nil
}

func siblingFooter(refs []ports.IssueRef, self int) string {
	var b strings.Builder
	b.WriteString("\n\n## 関連 Issue（lead say で同時起票）\n")
	for j, ref := range refs {
		if j == self {
			continue
		}
		fmt.Fprintf(&b, "- #%d\n", ref.Number)
	}
	return b.String()
}

func (r *Runner) redraft(ctx context.Context, opts Options, oneLiner string) (Result, error) {
	n := opts.Redraft
	iss, err := r.Gh.View(ctx, n)
	if err != nil {
		return Result{}, fmt.Errorf("say: redraft #%d: %w", n, err)
	}
	comments, err := r.Gh.IssueComments(ctx, n)
	if err != nil {
		return Result{}, fmt.Errorf("say: redraft #%d comments: %w", n, err)
	}
	prompt, err := BuildPrompt(PromptInput{
		Repository: opts.Repository, OneLiner: oneLiner, Rules: opts.Rules,
		Redraft: &RedraftInput{Issue: iss, Comments: comments},
	})
	if err != nil {
		return Result{}, err
	}
	out, logPath, err := r.runAgent(ctx, opts, prompt)
	res := Result{LogPath: logPath}
	if err != nil {
		return res, err
	}
	d, err := ParseDraft(out)
	if err != nil {
		return res, fmt.Errorf("say: agent output (see %s): %w", logPath, err)
	}
	if err := Validate([]Draft{d}); err != nil {
		return res, fmt.Errorf("say: %w", err)
	}
	res.Drafts = []Draft{d}
	summary := ChangeSummary(oneLiner, iss, d)

	if opts.DryRun {
		r.printf("[dry-run] would edit #%d\n  title: %s\n---\n%s\n---\n[dry-run] would comment:\n%s\n", n, d.Title, strings.TrimRight(d.Body, "\n"), summary)
		return res, nil
	}
	if err := r.Gh.IssueEdit(ctx, n, d.Title, d.Body); err != nil {
		return res, fmt.Errorf("say: redraft #%d: edit: %w", n, err)
	}
	if err := r.Gh.IssueAddLabel(ctx, n, opts.Label); err != nil {
		return res, fmt.Errorf("say: redraft #%d: label %s: %w", n, opts.Label, err)
	}
	if err := r.Gh.IssueComment(ctx, n, summary); err != nil {
		return res, fmt.Errorf("say: redraft #%d: comment: %w", n, err)
	}
	res.Redrafted = n
	r.printf("redrafted #%d  %s\n", n, d.Title)
	return res, nil
}

func (r *Runner) runAgent(ctx context.Context, opts Options, prompt string) ([]byte, string, error) {
	argv, err := agent.HeadlessArgv(opts.Agent, prompt)
	if err != nil {
		return nil, "", fmt.Errorf("say: %w", err)
	}
	logPath := filepath.Join(opts.LogDir, "say-"+time.Now().UTC().Format("20060102T150405Z")+".log")
	out, err := r.Agent.Run(ctx, opts.WorkDir, argv, logPath)
	if err != nil {
		return nil, logPath, fmt.Errorf("say: agent %s failed (log: %s): %w", opts.Agent, logPath, err)
	}
	return out, logPath, nil
}

func (r *Runner) printf(format string, args ...any) {
	if r.Out != nil {
		fmt.Fprintf(r.Out, format, args...)
	}
}

// ChangeSummary is the comment posted after a redraft: what the one-liner
// was and how title/body changed (line-level, order-insensitive).
func ChangeSummary(oneLiner string, before ports.Issue, after Draft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "lead say --redraft: 一言「%s」を反映して本文を再起案しました。\n\n", oneLiner)
	if before.Title == after.Title {
		b.WriteString("- タイトル: 変更なし\n")
	} else {
		fmt.Fprintf(&b, "- タイトル: `%s` → `%s`\n", before.Title, after.Title)
	}
	added, removed := lineDelta(before.Body, after.Body)
	fmt.Fprintf(&b, "- 本文: +%d 行 / -%d 行\n", len(added), len(removed))
	if len(added)+len(removed) > 0 {
		b.WriteString("\n<details><summary>差分（行単位）</summary>\n\n```diff\n")
		for _, l := range removed {
			b.WriteString("- " + l + "\n")
		}
		for _, l := range added {
			b.WriteString("+ " + l + "\n")
		}
		b.WriteString("```\n</details>\n")
	}
	return b.String()
}

func lineDelta(before, after string) (added, removed []string) {
	set := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
			if t := strings.TrimSpace(l); t != "" {
				m[t]++
			}
		}
		return m
	}
	bm, am := set(before), set(after)
	for _, l := range strings.Split(strings.ReplaceAll(after, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(l); t != "" && bm[t] == 0 {
			added = append(added, t)
			bm[t] = -1 // report each distinct line once
		}
	}
	for _, l := range strings.Split(strings.ReplaceAll(before, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(l); t != "" && am[t] == 0 {
			removed = append(removed, t)
			am[t] = -1
		}
	}
	return added, removed
}

// EnsureReference guarantees body mentions #n (follow-up issues must
// point at the issue whose 実機 NG spawned them).
func EnsureReference(body string, n int) string {
	ref := fmt.Sprintf("#%d", n)
	if containsRef(body, ref) {
		return body
	}
	return strings.TrimRight(body, "\n") + fmt.Sprintf("\n\n元 Issue: %s\n", ref)
}

// containsRef finds #n not immediately followed by another digit (#12 must
// not satisfy #1).
func containsRef(body, ref string) bool {
	for i := 0; ; {
		j := strings.Index(body[i:], ref)
		if j < 0 {
			return false
		}
		end := i + j + len(ref)
		if end >= len(body) || body[end] < '0' || body[end] > '9' {
			return true
		}
		i = end
	}
}

// Validate applies prompter.LintTitle to every draft and requires a body.
func Validate(drafts []Draft) error {
	if len(drafts) == 0 {
		return errors.New("agent proposed no issues")
	}
	var problems []string
	for i, d := range drafts {
		for _, p := range prompter.LintTitle(d.Title) {
			problems = append(problems, fmt.Sprintf("draft %d: %s", i+1, p))
		}
		if strings.TrimSpace(d.Body) == "" {
			problems = append(problems, fmt.Sprintf("draft %d: body is empty", i+1))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("title lint failed:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// ParseDrafts extracts the JSON array of drafts from agent output. It
// tolerates ``` fences and leading/trailing prose; a single object is
// accepted as a one-element array.
func ParseDrafts(raw []byte) ([]Draft, error) {
	payload, err := extractJSON(raw)
	if err != nil {
		return nil, err
	}
	if payload[0] == '{' {
		var d Draft
		if err := json.Unmarshal(payload, &d); err != nil {
			return nil, fmt.Errorf("invalid JSON object: %w", err)
		}
		return []Draft{d}, nil
	}
	var drafts []Draft
	if err := json.Unmarshal(payload, &drafts); err != nil {
		return nil, fmt.Errorf("invalid JSON array: %w", err)
	}
	return drafts, nil
}

// ParseDraft extracts a single {"title","body"} object (redraft mode). A
// one-element array is accepted too.
func ParseDraft(raw []byte) (Draft, error) {
	drafts, err := ParseDrafts(raw)
	if err != nil {
		return Draft{}, err
	}
	if len(drafts) != 1 {
		return Draft{}, fmt.Errorf("expected exactly one draft, got %d", len(drafts))
	}
	return drafts[0], nil
}

// extractJSON returns the outermost JSON array/object in raw: fences are
// stripped, then the text from the first '[' or '{' to the matching last
// ']' or '}' is taken.
func extractJSON(raw []byte) ([]byte, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return nil, errors.New("agent printed nothing")
	}
	// Prefer a fenced block when present.
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:] // drop the info string (json, etc.)
			if j := strings.Index(rest, "```"); j >= 0 {
				s = rest[:j]
			}
		}
	}
	start := -1
	var closer byte
	for i := 0; i < len(s); i++ {
		if s[i] == '[' {
			start, closer = i, ']'
			break
		}
		if s[i] == '{' {
			start, closer = i, '}'
			break
		}
	}
	if start < 0 {
		return nil, errors.New("no JSON array or object in agent output")
	}
	end := strings.LastIndexByte(s, closer)
	if end < start {
		return nil, errors.New("unterminated JSON in agent output")
	}
	payload := bytes.TrimSpace([]byte(s[start : end+1]))
	if !json.Valid(payload) {
		return nil, errors.New("agent output is not valid JSON")
	}
	return payload, nil
}

// PromptInput feeds BuildPrompt. Exactly one of Parent/Redraft may be set.
type PromptInput struct {
	Repository string
	OneLiner   string
	Rules      string
	Parent     *ports.Issue  // follow-up: the issue whose 実機 NG this addresses
	Redraft    *RedraftInput // redraft: rewrite this issue instead of creating
}

// RedraftInput is the issue being re-proposed plus its discussion.
type RedraftInput struct {
	Issue    ports.Issue
	Comments []ports.Comment
}

// bodyFormat mirrors prompter's decomposition sections (RFC §8: 本文は
// Mode / Purpose / Scope / Acceptance criteria / TDD first step).
const bodyFormat = `## Mode
implement | research | docs のいずれか

## Purpose
このIssueで完了させること（1〜3文）

## Scope
変更対象と対象外

## Acceptance criteria
- 実行結果・生成物・終了状態で検証できる条件（チェックリスト）

## TDD first step
最初に追加してRedを確認するテスト。該当しない場合は理由

## Open questions
判断が必要な点。なければ none`

const promptTemplate = `あなたはGitHub Issueの起票担当（仕様AI）です。人間の一言を、コーディングエージェントがTDDで1セッション（PR 1本）で完了できる粒度のIssueに変換します。

## リポジトリ
{{.Repository}}

## 人間の一言
{{.OneLiner}}
{{if .Parent}}
## 元 Issue（実機確認で NG になった Issue。追い Issue を起票する）
- Number: #{{.Parent.Number}}
- Title: {{.Parent.Title}}
- Body:
{{.Parent.Body}}
{{end}}{{if .Redraft}}
## 再起案する Issue（本文を書き直す。新規作成はしない）
- Number: #{{.Redraft.Issue.Number}}
- Title: {{.Redraft.Issue.Title}}
- Body:
{{.Redraft.Issue.Body}}
{{if .Redraft.Comments}}
### これまでのコメント
{{range .Redraft.Comments}}- {{.Author}} ({{.CreatedAt}}):
{{.Body}}
{{end}}{{end}}{{end}}
## リポジトリ規約（AGENTS.md）
{{.Rules}}

## 制約
1. 各Issueは、目的・対象範囲・受入条件・検証方法が1つのまとまりになっていること。1セッションで終わらない場合は複数Issueに分け、本文で相互に参照すること。
2. 受入条件は実行結果、生成物、終了状態など検証可能な形で書くこと。
3. 実装Issueでは、最初に追加する失敗テスト（Red）を明記すること。
4. 一言にない要件を勝手に追加しないこと。不明点は Open questions に分離すること。
5. タイトルは256文字以内・1行・文末ピリオドなし。要点（何を）・対象（どこに）・意図（なぜ）が読めること。動詞始まりの接頭辞（例: feat(cli):, fix(dispatch):, docs:）を推奨。
{{if .Parent}}6. 本文に元 Issue「#{{.Parent.Number}}」への参照と、実機で何がNGだったかを書くこと。
{{end}}{{if .Redraft}}6. 一言で指示された点だけを反映し、他の内容は保つこと。タイトルは必要なときだけ変えること。
{{end}}
## 本文の形式（Markdown）
` + bodyFormat + `

## 出力形式
{{if .Redraft}}JSON オブジェクト 1 つだけを出力してください。前後に説明文やコードフェンスを付けないこと。
{"title": "<タイトル>", "body": "<Markdown 本文>"}
{{else}}JSON 配列だけを出力してください。前後に説明文やコードフェンスを付けないこと。Issue が1件でも配列にすること。
[{"title": "<タイトル>", "body": "<Markdown 本文>"}]
{{end}}`

// BuildPrompt renders the spec-agent prompt. Repository and OneLiner are
// required; Rules defaults to DefaultRules.
func BuildPrompt(in PromptInput) (string, error) {
	if strings.TrimSpace(in.OneLiner) == "" {
		return "", errors.New("say: the one-liner is empty")
	}
	if in.Parent != nil && in.Redraft != nil {
		return "", errors.New("say: follow-up and redraft are mutually exclusive")
	}
	if strings.TrimSpace(in.Repository) == "" {
		in.Repository = "local"
	}
	if strings.TrimSpace(in.Rules) == "" {
		in.Rules = DefaultRules
	}
	tpl, err := template.New("say").Parse(promptTemplate)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := tpl.Execute(&sb, in); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// ExecRunner runs the agent binary as a child process: stdout is captured
// and, together with stderr, appended to logPath; stdin is /dev/null so a
// prompt for input fails fast instead of hanging.
type ExecRunner struct {
	LookPath func(name string) (string, error)
}

var _ AgentRunner = (*ExecRunner)(nil)

// Run implements AgentRunner.
func (e *ExecRunner) Run(ctx context.Context, dir string, argv []string, logPath string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("spec: empty argv")
	}
	lookPath := e.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath(argv[0]); err != nil {
		return nil, &ports.BinaryNotFoundError{Binary: argv[0]}
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("spec: log dir: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("spec: open log: %w", err)
	}
	defer f.Close()
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = nil // /dev/null
	cmd.Stdout = io.MultiWriter(&stdout, f)
	cmd.Stderr = f
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("spec: %s: %w", argv[0], err)
	}
	return stdout.Bytes(), nil
}
