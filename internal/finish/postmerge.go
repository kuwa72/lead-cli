package finish

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
	RepoRoot(dir string) (string, error)
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
	// Base is the branch the revert is pushed to (default main).
	Base string
	// Smoke overrides DefaultSmoke (tests).
	Smoke func(ctx context.Context, dir string) error
	// Timeout caps the smoke (default 10m).
	Timeout time.Duration
	// Git runs the worktree/revert/push steps; required.
	Git PostMergeGit
}

// PostMergeResult reports one post-merge smoke.
type PostMergeResult struct {
	// Skipped is true when the merge commit is unknown: nothing ran, so
	// nothing can be reverted.
	Skipped bool
	Passed  bool
	// SmokeFailed is true when the build/smoke itself failed (as opposed
	// to a setup error before it ran).
	SmokeFailed bool
	Reverted    bool
	// RevertOf is the merge commit the revert targets.
	RevertOf string
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
			return PostMergeResult{
				Skipped: true,
				Message: fmt.Sprintf("postmerge: PR #%d has no merge commit; skipped the smoke", opts.PR),
			}, nil
		}
		sha = got
	}
	base := opts.Base
	if base == "" {
		base = "main"
	}
	root, err := opts.Git.RepoRoot(opts.WorkDir)
	if err != nil {
		return PostMergeResult{}, fmt.Errorf("postmerge: repository root: %w", err)
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
		smoke = DefaultSmoke
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
	return revertAndFile(ctx, gh, opts, sha, base, dir, smokeErr)
}

// revertAndFile undoes the merge in the postmerge worktree and files the
// incident. The revert is committed there and pushed as HEAD:<base>, so
// the caller's checkout and local branch are untouched.
func revertAndFile(ctx context.Context, gh ports.GhClient, opts PostMergeOptions, sha, base, dir string, smokeErr error) (PostMergeResult, error) {
	res := PostMergeResult{SmokeFailed: true, RevertOf: sha}
	var revertErr error
	if err := opts.Git.Revert(dir, sha); err != nil {
		revertErr = fmt.Errorf("revert %s: %w", shortSHA(sha), err)
	} else if err := opts.Git.Push(dir, "origin", "HEAD:"+base); err != nil {
		revertErr = fmt.Errorf("push the revert to %s: %w", base, err)
	}
	if revertErr == nil {
		res.Reverted = true
	}

	title := fmt.Sprintf("[P0] postmerge smoke failed after PR #%d", opts.PR)
	body := incidentBody(opts, sha, base, smokeErr, revertErr)
	labels := []string{IncidentLabel}
	if err := ensureIncidentLabel(ctx, gh); err != nil {
		// The incident matters more than its label: file it unlabelled.
		labels = nil
	}
	ref, createErr := gh.IssueCreate(ctx, title, body, labels)
	if createErr != nil {
		return res, fmt.Errorf("postmerge: smoke failed and the incident issue could not be filed: %w", createErr)
	}
	res.Incident = ref.Number
	if revertErr != nil {
		res.Message = fmt.Sprintf("postmerge: smoke failed for PR #%d; revert failed (%v), so main is still broken. Manual recovery is required (incident #%d).", opts.PR, revertErr, ref.Number)
		return res, fmt.Errorf("postmerge: smoke failed and the revert failed: %w", revertErr)
	}
	res.Message = fmt.Sprintf("postmerge: smoke failed for PR #%d; reverted %s and filed incident #%d.", opts.PR, shortSHA(sha), ref.Number)
	return res, fmt.Errorf("postmerge: smoke failed for PR #%d (reverted %s; incident #%d)", opts.PR, shortSHA(sha), ref.Number)
}

// incidentBody describes what broke and what was (or was not) done.
func incidentBody(opts PostMergeOptions, sha, base string, smokeErr, revertErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "main のマージ後スモークが失敗しました。\n\n")
	fmt.Fprintf(&b, "- Issue: #%d\n", opts.Issue)
	fmt.Fprintf(&b, "- PR: #%d\n", opts.PR)
	fmt.Fprintf(&b, "- Merge commit: %s\n", sha)
	if revertErr == nil {
		fmt.Fprintf(&b, "- Revert: 実行し、`%s` に push 済み\n", base)
	} else {
		fmt.Fprintf(&b, "- Revert: 失敗 (%v)\n\n**手動復旧が必要です。** main は壊れたまま残っています。\n", revertErr)
	}
	fmt.Fprintf(&b, "\n## スモーク失敗\n\n```\n%s\n```\n", strings.TrimSpace(smokeErr.Error()))
	return b.String()
}

// ensureIncidentLabel creates the incident label when the repository does
// not have it yet; gh refuses to create an issue with a missing label.
func ensureIncidentLabel(ctx context.Context, gh ports.GhClient) error {
	labels, err := gh.RepoLabels(ctx, "")
	if err != nil {
		return err
	}
	if slices.Contains(labels, IncidentLabel) {
		return nil
	}
	return gh.RepoCreateLabel(ctx, "", ports.LabelDefinition{
		Name:        IncidentLabel,
		Color:       "B60205",
		Description: "Post-merge smoke failed on main (P0)",
	})
}

// DefaultSmoke builds main and runs the fast shell-test subset.
func DefaultSmoke(ctx context.Context, dir string) error {
	for _, argv := range [][]string{
		{"go", "build", "./..."},
		{"bash", "test/test_cli_skeleton.sh"},
	} {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w\n%s", strings.Join(argv, " "), err, tailLines(string(out), 20))
		}
	}
	return nil
}

// tailLines keeps the end of a noisy command output.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
