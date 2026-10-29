// Package scenario stores per-issue scenarios outside the worktree and runs
// throwaway integration tests generated from them at the PR head (issue #276).
//
// A scenario lives at <state-dir>/scenarios/<owner>_<repo>/<issue>.md so the
// implementation agent cannot edit it. The generator agent reads the scenario
// and the PR-head worktree and writes a self-contained runner to a temporary
// directory. lead copies that directory into the worktree, runs it, parses
// one `SCENARIO <n>: pass|fail` line per step, and removes it again.
package scenario

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/kuwa72/lead-cli/internal/adapters/agent"
	"github.com/kuwa72/lead-cli/internal/watchdog"
)

// Step verdicts printed by the generated runner.
const (
	VerdictPass = "pass"
	VerdictFail = "fail"
)

const (
	runnerFile  = "run.sh"
	workDirName = ".lead-scenario"
)

// ErrScenariosFailed reports at least one failed scenario (CLI exit 1).
var ErrScenariosFailed = errors.New("scenario: some scenarios failed")

// DefaultExecTimeout caps one generated-test run so a hanging test cannot
// block the caller forever.
const DefaultExecTimeout = 10 * time.Minute

// Step is one numbered item under `## Steps`.
type Step struct {
	Number int
	Text   string
}

// Scenario is a parsed scenario document.
type Scenario struct {
	Steps []Step
}

// Dir returns the repository-scoped scenario directory, outside any worktree.
func Dir(stateDir, repoSlug string) string {
	slug := strings.ReplaceAll(strings.TrimSpace(repoSlug), "/", "_")
	if slug == "" {
		slug = "local"
	}
	return filepath.Join(stateDir, "scenarios", slug)
}

// Path returns the scenario file for one issue.
func Path(stateDir, repoSlug string, issue int) string {
	return filepath.Join(Dir(stateDir, repoSlug), fmt.Sprintf("%d.md", issue))
}

// Save writes the scenario body, creating the directory as needed.
func Save(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("scenario: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("scenario: write %s: %w", path, err)
	}
	return nil
}

// Load reads a scenario file.
func Load(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Entry is one stored scenario.
type Entry struct {
	Issue int
	Path  string
}

// List returns the scenarios of one repository, ordered by issue number.
func List(stateDir, repoSlug string) ([]Entry, error) {
	dir := Dir(stateDir, repoSlug)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scenario: list %s: %w", dir, err)
	}
	var out []Entry
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".md"))
		if err != nil || n <= 0 {
			continue
		}
		out = append(out, Entry{Issue: n, Path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Issue < out[j].Issue })
	return out, nil
}

var (
	stepItemPattern = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+(.+)$`)
	verdictPattern  = regexp.MustCompile(`(?i)^\s*SCENARIO\s+(\d+)\s*:\s*(pass|fail)\s*$`)
)

// Parse extracts the list under `## Steps`. Numbered and unnumbered items
// both count; fenced code blocks and later sections are ignored.
func Parse(md string) Scenario {
	var out Scenario
	inSteps, fenced := false, false
	fence := ""
	for _, raw := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			marker := line[:3]
			if !fenced {
				fenced, fence = true, marker
			} else if marker == fence {
				fenced, fence = false, ""
			}
			continue
		}
		if fenced {
			continue
		}
		// Nested bullets and continuation lines belong to the step above;
		// only top-level items are steps.
		if strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t") {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			inSteps = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "## ")), "Steps")
			continue
		}
		if strings.HasPrefix(line, "# ") {
			inSteps = false
			continue
		}
		if !inSteps {
			continue
		}
		m := stepItemPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out.Steps = append(out.Steps, Step{Number: len(out.Steps) + 1, Text: strings.TrimSpace(m[1])})
	}
	return out
}

// StepResult is the verdict for one step.
type StepResult struct {
	Number  int
	Text    string
	Verdict string
}

// Result is one scenario run. Missing verdicts count as fail.
type Result struct {
	Passed  int
	Total   int
	Steps   []StepResult
	Output  string
	LogPath string
}

// Score renders "passed/total" (0/0 when no scenario is configured).
func (r Result) Score() string { return fmt.Sprintf("%d/%d", r.Passed, r.Total) }

// AgentRunner runs the generator agent to completion in dir with argv,
// teeing its output to logPath and returning stdout. Satisfied by
// spec.ExecRunner.
type AgentRunner interface {
	Run(ctx context.Context, dir string, argv []string, logPath string) ([]byte, error)
}

// Options tunes one run.
type Options struct {
	Issue        int
	ScenarioPath string // empty means no scenario is configured
	WorkDir      string // PR-head worktree to test
	Agent        string
	LogDir       string // prompt and agent log; required
	// ExecTimeout caps the generated-test run. Zero = DefaultExecTimeout.
	ExecTimeout time.Duration
}

// Runner wires the generator agent to a worktree.
type Runner struct {
	Agent AgentRunner
	Opts  Options
}

// Run generates the throwaway tests, executes them in the worktree, and
// removes them. A missing or step-less scenario yields 0/0 without an error.
func (r *Runner) Run(ctx context.Context) (Result, error) {
	if r.Agent == nil {
		return Result{}, errors.New("scenario: no agent runner")
	}
	opts := r.Opts
	if opts.Issue <= 0 {
		return Result{}, errors.New("scenario: issue number is required")
	}
	if opts.WorkDir == "" {
		return Result{}, errors.New("scenario: worktree is required")
	}
	if opts.LogDir == "" {
		return Result{}, errors.New("scenario: log directory is required")
	}
	raw, err := Load(opts.ScenarioPath)
	if os.IsNotExist(err) {
		return Result{}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("scenario: read %s: %w", opts.ScenarioPath, err)
	}
	sc := Parse(raw)
	if len(sc.Steps) == 0 {
		return Result{}, nil
	}

	outDir, err := os.MkdirTemp("", "lead-scenario-*")
	if err != nil {
		return Result{}, fmt.Errorf("scenario: temp dir: %w", err)
	}
	defer os.RemoveAll(outDir)

	prompt, err := BuildPrompt(PromptInput{
		Issue:    opts.Issue,
		Scenario: raw,
		WorkDir:  opts.WorkDir,
		OutDir:   outDir,
	})
	if err != nil {
		return Result{}, err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.MkdirAll(opts.LogDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("scenario: prompt dir: %w", err)
	}
	promptPath := filepath.Join(opts.LogDir, fmt.Sprintf("scenario-%d-%s.prompt.md", opts.Issue, stamp))
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return Result{}, fmt.Errorf("scenario: write prompt: %w", err)
	}
	defer os.Remove(promptPath)
	argv, err := agent.HeadlessArgv(opts.Agent, PromptInstruction(promptPath))
	if err != nil {
		return Result{}, fmt.Errorf("scenario: %w", err)
	}
	logPath := filepath.Join(opts.LogDir, fmt.Sprintf("scenario-%d-%s.log", opts.Issue, stamp))
	if _, err := r.Agent.Run(ctx, opts.WorkDir, argv, logPath); err != nil {
		return Result{LogPath: logPath}, fmt.Errorf("scenario: agent failed (log: %s): %w", logPath, err)
	}

	if _, err := os.Stat(filepath.Join(outDir, runnerFile)); err != nil {
		return Result{LogPath: logPath}, fmt.Errorf("scenario: agent produced no %s (log: %s)", runnerFile, logPath)
	}
	dest := filepath.Join(opts.WorkDir, workDirName)
	if err := os.RemoveAll(dest); err != nil {
		return Result{LogPath: logPath}, fmt.Errorf("scenario: clear %s: %w", dest, err)
	}
	if err := copyTree(outDir, dest); err != nil {
		return Result{LogPath: logPath}, fmt.Errorf("scenario: copy generated tests: %w", err)
	}
	defer os.RemoveAll(dest)

	out, runErr := runRunner(ctx, opts.WorkDir, opts.ExecTimeout)
	if errors.Is(runErr, context.DeadlineExceeded) {
		return Result{Total: len(sc.Steps), Output: string(out), LogPath: logPath}, fmt.Errorf("scenario: generated tests timed out after %s", execTimeout(opts.ExecTimeout))
	}
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return Result{LogPath: logPath}, fmt.Errorf("scenario: run %s: %w", runnerFile, runErr)
	}

	res := Result{Total: len(sc.Steps), Output: string(out), LogPath: logPath}
	verdicts := parseVerdicts(string(out))
	for _, s := range sc.Steps {
		v := verdicts[s.Number]
		if v != VerdictPass {
			v = VerdictFail
		}
		if v == VerdictPass {
			res.Passed++
		}
		res.Steps = append(res.Steps, StepResult{Number: s.Number, Text: s.Text, Verdict: v})
	}
	return res, nil
}

func execTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultExecTimeout
	}
	return d
}

// runRunner executes the generated runner in its own process group so a
// timeout also kills tests it spawned.
func runRunner(parent context.Context, workDir string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, execTimeout(timeout))
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(workDirName, runnerFile))
	cmd.Dir = workDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return watchdog.KillProcessGroup(cmd.Process.Pid) }
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, context.DeadlineExceeded
	}
	return out, err
}

func parseVerdicts(out string) map[int]string {
	verdicts := map[int]string{}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		m := verdictPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			continue
		}
		verdicts[n] = strings.ToLower(m[2])
	}
	return verdicts
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}

// PromptInstruction is the short argv prompt pointing at the prompt file.
func PromptInstruction(path string) string {
	return "Read and follow the scenario test instructions in this file: " + path
}

// PromptInput feeds BuildPrompt.
type PromptInput struct {
	Issue    int
	Scenario string
	WorkDir  string
	OutDir   string
}

const promptTemplate = `あなたは使い捨ての統合テスト生成器です。Issue のシナリオを、対象 worktree の実装に対して検証するテストを生成します。

## 対象
- Issue: #{{.Issue}}
- worktree: {{.WorkDir}}
- 出力先: {{.OutDir}}

## シナリオ
{{.Scenario}}

## 生成するもの
- 出力先に run.sh を必ず作成すること。
- 追加のテストファイルはすべて出力先の配下に置くこと。
- run.sh は worktree のルートをカレントディレクトリとして ` + "`bash .lead-scenario/run.sh`" + ` で実行される。生成物は実行時に worktree の .lead-scenario/ へコピーされる。
- run.sh はシナリオの各ステップについて、次の形式の行を標準出力に1行ずつ出すこと（n は Steps の番号、1始まり）。
  SCENARIO <n>: pass
  SCENARIO <n>: fail
- 判定できないステップは fail とすること。
- worktree のファイルを変更しないこと。生成物を出力先の外に書かないこと。
- run.sh は実行後に自分が worktree に作った一時ファイルを削除すること。
- テストは実装の写経ではなく、シナリオの振る舞いを検証すること。
`

// BuildPrompt renders the generator prompt.
func BuildPrompt(in PromptInput) (string, error) {
	if in.Issue <= 0 {
		return "", errors.New("scenario: issue number is required")
	}
	if strings.TrimSpace(in.Scenario) == "" {
		return "", errors.New("scenario: the scenario is empty")
	}
	if in.WorkDir == "" || in.OutDir == "" {
		return "", errors.New("scenario: worktree and output directory are required")
	}
	tpl, err := template.New("scenario").Parse(promptTemplate)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := tpl.Execute(&sb, in); err != nil {
		return "", err
	}
	return sb.String(), nil
}
