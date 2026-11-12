package finish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kuwa72/lead-cli/internal/adapters/agent"
	"github.com/kuwa72/lead-cli/internal/inbox"
	"github.com/kuwa72/lead-cli/internal/ports"
)

// IncidentLabel marks the issue filed when main breaks after a merge
// (issue #275). It is the P0 equivalent: a broken main stops every
// subsequent dispatch.
const IncidentLabel = "incident"

// DefaultPostMergeTimeout caps the smoke so a hung build cannot block
// the dispatch loop forever.
const DefaultPostMergeTimeout = 10 * time.Minute

// PostMergeGit is the git subset the post-merge smoke needs. It is
// satisfied by workflow.GitRunner.
type PostMergeGit interface {
	// MainWorktree is the checkout that owns .worktrees/. It matters
	// because finish runs from the issue's linked worktree.
	MainWorktree(dir string) (string, error)
	EnsureCommit(repoDir, sha string) error
	WorktreeAddDetached(repoDir, path, commit string) error
	WorktreeRemove(repoDir, path string, force bool) error
	Revert(dir, sha string) error
	Push(dir, remote, refspec string) error
}

// PostMergeOptions controls one post-merge smoke.
type PostMergeOptions struct {
	Issue int
	PR    int
	// MergeSHA is the commit to smoke and revert. Empty looks it up from
	// the PR (gh mergeCommit).
	MergeSHA string
	// WorkDir is a checkout inside the repository.
	WorkDir string
	// Base is the branch the revert is pushed to (default: the repo's
	// default branch, else main).
	Base string
	// Repo is "owner/repo" for the default-branch lookup; empty skips it.
	Repo string
	// Smoke overrides AgentSmoke (tests).
	Smoke func(ctx context.Context, dir string) error
	// Agent runs headless LLM smoke verification when Smoke is nil.
	Agent AgentRunner
	// AgentName selects the agent CLI binary (default: "agy").
	AgentName string
	// LogDir stores post-merge smoke agent logs (defaults to os.TempDir()).
	LogDir string
	// Timeout caps the smoke (default 10m).
	Timeout time.Duration
	// Git runs the worktree/revert/push steps; required.
	Git PostMergeGit
}

// AgentRunner invokes a headless coding agent in dir and returns its stdout.
type AgentRunner interface {
	Run(ctx context.Context, dir string, argv []string, logPath string) ([]byte, error)
}

// PostMergeResult reports one post-merge smoke.
type PostMergeResult struct {
	Passed bool
	// SmokeFailed is true when the build/smoke itself failed (as opposed
	// to a setup error before it ran).
	SmokeFailed bool
	Reverted    bool
	// RevertOf is the merge commit the revert targets.
	RevertOf string
	// RevertPR is the PR that carries the revert (main is protected).
	RevertPR int
	Incident int
	Message  string
}

// PostMerge builds main at the merge commit in a temporary worktree and
// runs the smoke. A failure reverts the merge, pushes the revert, and
// files an incident issue (issue #275). Revert failures stop with an
// incident that says manual recovery is required: main stays broken.
func PostMerge(ctx context.Context, gh ports.GhClient, opts PostMergeOptions) (PostMergeResult, error) {
	if opts.Git == nil {
		return PostMergeResult{}, errors.New("postmerge: no git runner")
	}
	sha := opts.MergeSHA
	if sha == "" {
		if opts.PR <= 0 {
			return PostMergeResult{}, errors.New("postmerge: PR number is required")
		}
		got, err := gh.PrMergeCommit(ctx, opts.PR)
		if err != nil {
			return PostMergeResult{}, fmt.Errorf("postmerge: PR #%d: merge commit: %w", opts.PR, err)
		}
		if got == "" {
			return PostMergeResult{}, fmt.Errorf("postmerge: PR #%d has no merge commit yet; cannot smoke or revert", opts.PR)
		}
		sha = got
	}
	base := opts.Base
	if base == "" {
		base = "main"
		if opts.Repo != "" {
			if def, err := gh.RepoDefaultBranch(ctx, opts.Repo); err == nil && def != "" {
				base = def
			}
		}
	}
	root, err := opts.Git.MainWorktree(opts.WorkDir)
	if err != nil {
		return PostMergeResult{}, fmt.Errorf("postmerge: main worktree: %w", err)
	}
	if err := opts.Git.EnsureCommit(root, sha); err != nil {
		return PostMergeResult{}, fmt.Errorf("postmerge: merge commit %s is not available: %w", shortSHA(sha), err)
	}
	dir := filepath.Join(root, ".worktrees", fmt.Sprintf("postmerge-pr%d", opts.PR))
	// An interrupted earlier run may have left this path registered.
	if err := opts.Git.WorktreeRemove(root, dir, true); err != nil {
		return PostMergeResult{}, fmt.Errorf("postmerge: remove stale worktree %s: %w", dir, err)
	}
	if err := opts.Git.WorktreeAddDetached(root, dir, sha); err != nil {
		return PostMergeResult{}, fmt.Errorf("postmerge: create worktree at %s: %w", dir, err)
	}
	defer func() { _ = opts.Git.WorktreeRemove(root, dir, true) }()

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultPostMergeTimeout
	}
	smoke := opts.Smoke
	if smoke == nil {
		smoke = func(smokeCtx context.Context, smokeDir string) error {
			return AgentSmoke(smokeCtx, opts, smokeDir)
		}
	}
	smokeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	smokeErr := smoke(smokeCtx, dir)
	if smokeErr == nil {
		return PostMergeResult{
			Passed:  true,
			Message: fmt.Sprintf("postmerge: main at %s passed the smoke", shortSHA(sha)),
		}, nil
	}
	var setupErr *smokeSetupError
	if errors.As(smokeErr, &setupErr) {
		return PostMergeResult{
			Message: fmt.Sprintf("postmerge: smoke did not run for PR #%d: %v", opts.PR, smokeErr),
		}, smokeErr
	}
	return revertAndFile(ctx, gh, opts, sha, base, dir, smokeErr)
}

// revertAndFile undoes the merge and files the incident. main is protected
// in this repository (and usually elsewhere), so a direct push is refused:
// the revert goes to its own branch, then a PR, with auto-merge enabled so
// it lands once checks pass. The caller's checkout and branch are untouched.
func revertAndFile(ctx context.Context, gh ports.GhClient, opts PostMergeOptions, sha, base, dir string, smokeErr error) (PostMergeResult, error) {
	res := PostMergeResult{SmokeFailed: true, RevertOf: sha}
	branch := fmt.Sprintf("revert/pr-%d-%s-%d", opts.PR, shortSHA(sha), time.Now().Unix())
	var revertErr, autoMergeErr error
	if err := opts.Git.Revert(dir, sha); err != nil {
		revertErr = fmt.Errorf("revert %s: %w", shortSHA(sha), err)
	} else if err := opts.Git.Push(dir, "origin", "HEAD:refs/heads/"+branch); err != nil {
		revertErr = fmt.Errorf("push the revert branch %s: %w", branch, err)
	} else if pr, err := gh.PrCreate(ctx, branch, base,
		fmt.Sprintf("revert: postmerge smoke failed after PR #%d", opts.PR),
		fmt.Sprintf("Reverts %s.\n\n%s", sha, strings.TrimSpace(smokeErr.Error()))); err != nil {
		revertErr = fmt.Errorf("open the revert PR: %w", err)
	} else {
		res.RevertPR = pr
		if err := gh.PrAutoMerge(ctx, pr); err != nil {
			autoMergeErr = fmt.Errorf("enable auto-merge on revert PR #%d: %w", pr, err)
		} else {
			res.Reverted = true
		}
	}

	title := fmt.Sprintf("[P0] postmerge smoke failed after PR #%d", opts.PR)
	body := incidentBody(opts, sha, base, res.RevertPR, smokeErr, revertErr, autoMergeErr)
	labels := []string{IncidentLabel}
	if err := ensureIncidentLabel(ctx, gh, opts.Repo); err != nil {
		// The incident matters more than its label: file it unlabelled.
		labels = nil
	}
	ref, createErr := gh.IssueCreate(ctx, title, body, labels)
	if createErr != nil {
		return res, fmt.Errorf("postmerge: smoke failed (revert PR: %d) and the incident issue could not be filed: %w", res.RevertPR, createErr)
	}
	res.Incident = ref.Number
	// The change is no longer wanted on main: keep the queue from
	// re-dispatching the issue and leave a pointer on it (best effort).
	_ = gh.IssueRemoveLabel(ctx, opts.Issue, inbox.LabelReady)
	_ = gh.IssueAddLabel(ctx, opts.Issue, inbox.LabelBlocked)
	_ = gh.IssueComment(ctx, opts.Issue, blockComment(opts, sha, res.RevertPR, ref.Number, revertErr, autoMergeErr))
	switch {
	case revertErr != nil:
		res.Message = fmt.Sprintf("postmerge: smoke failed for PR #%d; revert failed (%v), so main is still broken. Manual recovery is required (incident #%d).", opts.PR, revertErr, ref.Number)
		return res, fmt.Errorf("postmerge: smoke failed and the revert failed: %w", revertErr)
	case autoMergeErr != nil:
		res.Message = fmt.Sprintf("postmerge: smoke failed for PR #%d; revert PR #%d is open but auto-merge failed (%v). Merge it manually (incident #%d).", opts.PR, res.RevertPR, autoMergeErr, ref.Number)
		return res, fmt.Errorf("postmerge: smoke failed and the revert PR needs a human: %w", autoMergeErr)
	}
	res.Message = fmt.Sprintf("postmerge: smoke failed for PR #%d; opened revert PR #%d and enabled auto-merge (incident #%d).", opts.PR, res.RevertPR, ref.Number)
	return res, fmt.Errorf("postmerge: smoke failed for PR #%d (revert PR #%d; incident #%d)", opts.PR, res.RevertPR, ref.Number)
}

// incidentBody describes what broke and what was (or was not) done.
func incidentBody(opts PostMergeOptions, sha, base string, revertPR int, smokeErr, revertErr, autoMergeErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "main のマージ後スモークが失敗しました。\n\n")
	fmt.Fprintf(&b, "- Issue: #%d\n", opts.Issue)
	fmt.Fprintf(&b, "- PR: #%d\n", opts.PR)
	fmt.Fprintf(&b, "- Merge commit: %s\n", sha)
	switch {
	case revertErr != nil:
		fmt.Fprintf(&b, "- Revert: 失敗 (%v)\n\n**手動復旧が必要です。** main は壊れたまま残っています。\n", revertErr)
	case autoMergeErr != nil:
		fmt.Fprintf(&b, "- Revert: PR #%d を作成したが auto-merge の有効化に失敗 (%v)。手動でマージしてください。\n", revertPR, autoMergeErr)
	default:
		fmt.Fprintf(&b, "- Revert: PR #%d を作成し、`%s` 向けの auto-merge を有効化\n", revertPR, base)
	}
	fmt.Fprintf(&b, "\n## スモーク失敗\n\n```\n%s\n```\n", strings.TrimSpace(smokeErr.Error()))
	return b.String()
}

// blockComment tells the issue why it left the queue.
func blockComment(opts PostMergeOptions, sha string, revertPR, incident int, revertErr, autoMergeErr error) string {
	switch {
	case revertErr != nil:
		return fmt.Sprintf("postmerge: main のマージ後スモークが失敗し、revert にも失敗しました。手動復旧が必要です (incident #%d, merge %s)。", incident, sha)
	case autoMergeErr != nil:
		return fmt.Sprintf("postmerge: main のマージ後スモークが失敗しました。revert PR #%d を手動でマージしてください (auto-merge 失敗: %v, incident #%d)。", revertPR, autoMergeErr, incident)
	default:
		return fmt.Sprintf("postmerge: main のマージ後スモークが失敗したため、PR #%d の revert PR #%d を作成し auto-merge を有効化しました。incident #%d を確認してください。", opts.PR, revertPR, incident)
	}
}

// ensureIncidentLabel creates the incident label when the repository does
// not have it yet; gh refuses to create an issue with a missing label.
func ensureIncidentLabel(ctx context.Context, gh ports.GhClient, repo string) error {
	labels, err := gh.RepoLabels(ctx, repo)
	if err != nil {
		return err
	}
	if slices.Contains(labels, IncidentLabel) {
		return nil
	}
	return gh.RepoCreateLabel(ctx, repo, ports.LabelDefinition{
		Name:        IncidentLabel,
		Color:       "B60205",
		Description: "Post-merge smoke failed on main (P0)",
	})
}

const maxSmokeAgentsMDBytes = 8192

type smokeSetupError struct {
	err error
}

func (e *smokeSetupError) Error() string { return e.err.Error() }
func (e *smokeSetupError) Unwrap() error { return e.err }

// AgentSmoke delegates post-merge verification to a headless LLM agent running
// inside dir. It fails closed unless the agent exits 0 and ends its output
// with "SMOKE: pass".
func AgentSmoke(ctx context.Context, opts PostMergeOptions, dir string) error {
	if opts.Agent == nil {
		return &smokeSetupError{err: errors.New("postmerge: no smoke agent configured (set Smoke or Agent)")}
	}
	agentName := strings.TrimSpace(opts.AgentName)
	if agentName == "" {
		agentName = "agy"
	}
	prompt := buildSmokePrompt(opts, dir)
	argv, err := agent.HeadlessArgv(agentName, prompt)
	if err != nil {
		return &smokeSetupError{err: fmt.Errorf("postmerge: agent %q: %w", agentName, err)}
	}
	logDir := opts.LogDir
	if logDir == "" {
		logDir = os.TempDir()
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return &smokeSetupError{err: fmt.Errorf("postmerge: log dir: %w", err)}
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("postmerge-pr%d-%d.log", opts.PR, time.Now().UnixNano()))
	out, runErr := opts.Agent.Run(ctx, dir, argv, logPath)
	var bnf *ports.BinaryNotFoundError
	if errors.As(runErr, &bnf) || errors.Is(runErr, exec.ErrNotFound) {
		return &smokeSetupError{err: fmt.Errorf("postmerge: agent %q unavailable: %w", agentName, runErr)}
	}

	verdict := parseSmokeVerdict(string(out))
	switch verdict {
	case "pass":
		if runErr != nil {
			return &smokeSetupError{err: fmt.Errorf("postmerge: agent %q exited with error despite pass verdict: %w\n%s", agentName, runErr, tailLines(string(out), 20))}
		}
		return nil
	case "fail":
		if runErr != nil {
			return fmt.Errorf("postmerge: SMOKE: fail (%v)\n%s", runErr, tailLines(string(out), 20))
		}
		return fmt.Errorf("postmerge: SMOKE: fail\n%s", tailLines(string(out), 20))
	default:
		if runErr != nil {
			return &smokeSetupError{err: fmt.Errorf("postmerge: agent %q failed without smoke verdict: %w\n%s", agentName, runErr, tailLines(string(out), 20))}
		}
		return &smokeSetupError{err: fmt.Errorf("postmerge: missing or unrecognized smoke verdict (last non-empty line must be 'SMOKE: pass' or 'SMOKE: fail')\n%s", tailLines(string(out), 20))}
	}
}

func buildSmokePrompt(opts PostMergeOptions, dir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are verifying the merged default branch checkout at %s (PR #%d, Issue #%d).\n\n", dir, opts.PR, opts.Issue)
	b.WriteString("Tasks:\n")
	b.WriteString("1. Inspect the repository layout, build/test configuration, and any verification docs.\n")
	b.WriteString("2. Run a fast build and lightweight smoke check appropriate for this repository.\n")
	b.WriteString("3. Do NOT modify tracked files, create commits, push branches, or run slow full release suites.\n")
	if data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md")); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		txt := string(data)
		if len(txt) > maxSmokeAgentsMDBytes {
			txt = txt[:maxSmokeAgentsMDBytes] + "\n...[truncated]"
		}
		b.WriteString("\n## Repository AGENTS.md\n\n")
		b.WriteString(txt)
		b.WriteString("\n")
	}
	b.WriteString("\nOutput requirements:\n")
	b.WriteString("- Briefly report the smoke commands run and their outcomes.\n")
	b.WriteString("- The final non-empty line MUST be strictly one of:\n")
	b.WriteString("SMOKE: pass\n")
	b.WriteString("SMOKE: fail\n")
	return b.String()
}

func parseSmokeVerdict(out string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		switch line {
		case "SMOKE: pass":
			return "pass"
		case "SMOKE: fail":
			return "fail"
		default:
			return ""
		}
	}
	return ""
}

// tailLines keeps the end of a noisy command output.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
