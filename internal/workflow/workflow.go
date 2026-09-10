// Package workflow implements the branch/worktree/state half of `lead work`
// (issue #40 core, shared by the CLI and the #48 socket server so both
// entry points produce identical records).
package workflow

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

// GitRunner abstracts the git operations Start needs.
// *git.Runner implements it.
type GitRunner interface {
	RepoRoot(dir string) (string, error)
	CreateBranch(repoDir, branch string) error
	WorktreeAdd(repoDir, path, branch string) error
	WorktreeRemove(repoDir, path string, force bool) error
	OriginURL(repoDir string) string
}

// StartOptions controls one Start call.
type StartOptions struct {
	Issue     ports.Issue // resolved issue (number and title)
	Mode      string      // default implement
	Branch    string      // "" = issue/<n>-<slug>
	Part      string
	Worktree  string // "" = none, "auto" = <repo>/.worktrees/issue-<n>[-<part>]
	WorkDir   string // repo context (required)
	AgentMode string // interactive|batch|dangerous
}

// StartResult describes the established workflow.
type StartResult struct {
	Branch     string
	Worktree   string
	Status     state.Status
	Repository string
	Mode       string
	RepoRoot   string
	Pane       string
	AgentMode  string
}

// Start creates/checks out the branch, optionally creates a worktree, and
// records the workflow. Re-running with the same branch is idempotent:
// progress status and unre-specified fields are preserved.
func Start(ctx context.Context, g GitRunner, store *state.Store, opts StartOptions) (StartResult, error) {
	_ = ctx
	number := opts.Issue.Number
	if number <= 0 {
		return StartResult{}, fmt.Errorf("work: invalid issue number %d", number)
	}
	mode := opts.Mode
	if mode == "" {
		mode = state.ModeImplement
	}
	if opts.WorkDir == "" {
		return StartResult{}, fmt.Errorf("work #%d: working directory is required", number)
	}
	repoRoot, err := g.RepoRoot(opts.WorkDir)
	if err != nil {
		return StartResult{}, fmt.Errorf("work #%d: %w", number, err)
	}
	branch := opts.Branch
	if branch == "" {
		branch = git.BranchName(number, opts.Issue.Title)
	}
	wantWorktree := opts.Worktree != ""
	if !wantWorktree {
		if err := g.CreateBranch(repoRoot, branch); err != nil {
			return StartResult{}, fmt.Errorf("work #%d: branch %s: %w", number, branch, err)
		}
	}
	worktree := ""
	if wantWorktree {
		// NOTE: create the branch inside the new worktree (`git worktree add
		// -b`); checking it out in the main repo first would make the branch
		// busy and the add would fail.
		worktree = opts.Worktree
		if worktree == "" || worktree == "auto" {
			worktree = filepath.Join(repoRoot, ".worktrees", fmt.Sprintf("issue-%d", number))
			if opts.Part != "" {
				worktree += "-" + opts.Part
			}
		}
		if err := g.WorktreeAdd(repoRoot, worktree, branch); err != nil {
			return StartResult{}, fmt.Errorf("work #%d: worktree %s: %w", number, worktree, err)
		}
	}

	existing, ok, err := store.Get(number, opts.Part)
	if err != nil {
		return StartResult{}, fmt.Errorf("work #%d: %w", number, err)
	}
	repo := g.OriginURL(repoRoot)
	if repo == "" {
		repo = "local"
	}
	w := state.Workflow{
		Repository: repo,
		Issue:      number,
		Mode:       mode,
		Part:       opts.Part,
		Branch:     branch,
		Worktree:   worktree,
		Status:     state.StatusInProgress,
		AgentMode:  opts.AgentMode,
	}
	if ok {
		if err := state.CheckTransition(existing.Status, state.StatusInProgress); err != nil && existing.Status != state.StatusInProgress {
			return StartResult{}, fmt.Errorf("work #%d: %w", number, err)
		}
		w.Status = existing.Status
		if worktree == "" {
			w.Worktree = existing.Worktree
		}
		w.Pane = existing.Pane
		w.PullRequests = existing.PullRequests
		w.MergePolicy = existing.MergePolicy
		w.PolicyReason = existing.PolicyReason
		w.Artifacts = existing.Artifacts
		w.Agent = existing.Agent
		if opts.AgentMode == "" {
			w.AgentMode = existing.AgentMode
		}
		w.Attempts = existing.Attempts
		w.PID = existing.PID
		w.LogPath = existing.LogPath
	}
	if err := store.Upsert(w); err != nil {
		return StartResult{}, fmt.Errorf("work #%d: %w", number, err)
	}
	return StartResult{
		Branch: branch, Worktree: w.Worktree, Status: w.Status,
		Repository: repo, Mode: mode, RepoRoot: repoRoot, Pane: w.Pane,
		AgentMode: w.AgentMode,
	}, nil
}
