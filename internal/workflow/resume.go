package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

// ResumeOptions controls one Resume call.
type ResumeOptions struct {
	Target         string // target identifier: issue number, branch name, or PR number
	WorkDir        string // repo context (required)
	Worktree       string // "" = reuse existing, "auto" or <path> = attach worktree if none
	Agent          string // override agent
	AgentMode      string // interactive|batch|dangerous
	PromptTemplate string
	Repair         bool // repair state file if corrupt
}

// ResumeResult describes the resumed workflow.
type ResumeResult struct {
	Issue      ports.Issue
	Branch     string
	Worktree   string
	Status     state.Status
	Repository string
	Mode       string
	RepoRoot   string
	Pane       string
	Agent      string
	AgentMode  string
}

// ResumeCandidate represents a detected existing workflow.
type ResumeCandidate struct {
	Issue        int
	Title        string
	Branch       string
	Worktree     string
	PullRequests []state.PRRef
	Mode         string
	Part         string
	Status       state.Status
	Agent        string
	AgentMode    string
	Pane         string
	MergePolicy  string
	PolicyReason string
}

func branchMatchesNumber(branch string, num int) bool {
	prefix := fmt.Sprintf("issue/%d-", num)
	exact := fmt.Sprintf("issue/%d", num)
	prefixBare := fmt.Sprintf("%d-", num)
	return strings.HasPrefix(branch, prefix) || branch == exact || strings.HasPrefix(branch, prefixBare) || branch == strconv.Itoa(num)
}

func pathMatchesNumber(path string, num int) bool {
	base := filepath.Base(path)
	prefix := fmt.Sprintf("issue-%d", num)
	return base == prefix || strings.HasPrefix(base, prefix+"-")
}

func extractIssueNumber(branch string) int {
	parts := strings.Split(branch, "/")
	name := parts[len(parts)-1]
	sub := strings.Split(name, "-")[0]
	n, err := strconv.Atoi(sub)
	if err == nil && n > 0 {
		return n
	}
	return 0
}

// ResolveTarget resolves target from local state, Git branches, Worktrees, or GitHub PR / Issue.
// Safe behavior: never creates a branch or PR.
func ResolveTarget(ctx context.Context, g GitRunner, store *state.Store, gh ports.GhClient, repoRoot, target string, repair bool) (*ResumeCandidate, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("resume: target is required (issue number, branch name, or PR number)")
	}

	cleanTarget := strings.TrimPrefix(target, "#")
	num, isNumErr := strconv.Atoi(cleanTarget)
	isNumeric := isNumErr == nil && num > 0

	var candidates []*ResumeCandidate

	findOrAdd := func(branch string, issue int) *ResumeCandidate {
		for _, c := range candidates {
			if branch != "" && c.Branch == branch {
				if issue > 0 && c.Issue == 0 {
					c.Issue = issue
				}
				return c
			}
			if issue > 0 && c.Issue == issue && (branch == "" || c.Branch == "") {
				if branch != "" && c.Branch == "" {
					c.Branch = branch
				}
				return c
			}
		}
		c := &ResumeCandidate{
			Issue:  issue,
			Branch: branch,
		}
		candidates = append(candidates, c)
		return c
	}

	// 1. Check local state (workflows.json)
	if store != nil {
		wfs, err := store.FindByTarget(target)
		if err != nil {
			if !repair {
				return nil, err
			}
			// if repair, ignore corrupt state error and proceed with git / gh discovery
		} else {
			for _, w := range wfs {
				c := findOrAdd(w.Branch, w.Issue)
				c.Worktree = w.Worktree
				c.Mode = w.Mode
				c.Part = w.Part
				c.Status = w.Status
				c.PullRequests = w.PullRequests
				c.Agent = w.Agent
				c.AgentMode = w.AgentMode
				c.Pane = w.Pane
				c.MergePolicy = w.MergePolicy
				c.PolicyReason = w.PolicyReason
			}
		}
	}

	// 2. Check Git worktrees
	wtList, err := g.WorktreeList(repoRoot)
	if err == nil {
		for _, wt := range wtList {
			wtBranch := strings.TrimPrefix(wt.Branch, "refs/heads/")
			matched := false
			if isNumeric {
				if branchMatchesNumber(wtBranch, num) || pathMatchesNumber(wt.Path, num) {
					matched = true
				}
			} else {
				if wtBranch == target || wt.Path == target || filepath.Base(wt.Path) == target {
					matched = true
				}
			}
			if matched {
				issNum := extractIssueNumber(wtBranch)
				if issNum == 0 && isNumeric {
					issNum = num
				}
				c := findOrAdd(wtBranch, issNum)
				if c.Worktree == "" {
					c.Worktree = wt.Path
				}
			}
		}
	}

	// 3. Check Git branches
	branches, err := g.ListBranches(repoRoot)
	if err == nil {
		for _, b := range branches {
			matched := false
			if isNumeric {
				if branchMatchesNumber(b, num) {
					matched = true
				}
			} else {
				if b == target {
					matched = true
				}
			}
			if matched {
				issNum := extractIssueNumber(b)
				if issNum == 0 && isNumeric {
					issNum = num
				}
				findOrAdd(b, issNum)
			}
		}
	}

	// 4. Check GitHub (Issue and PR)
	if gh != nil {
		if isNumeric {
			// Check if PR exists with this number
			headBranch, prErr := gh.PrHeadBranch(ctx, num)
			if prErr == nil && headBranch != "" {
				issNum := extractIssueNumber(headBranch)
				c := findOrAdd(headBranch, issNum)
				hasPR := false
				for _, p := range c.PullRequests {
					if p.Number == num {
						hasPR = true
						break
					}
				}
				if !hasPR {
					c.PullRequests = append(c.PullRequests, state.PRRef{Number: num, Status: "open"})
				}
			}

			// Check if Issue exists with this number
			iss, issErr := gh.View(ctx, num)
			if issErr == nil && iss.Number == num {
				for _, c := range candidates {
					if c.Issue == num && c.Title == "" {
						c.Title = iss.Title
					}
				}
				hasCandidate := false
				for _, c := range candidates {
					if c.Issue == num {
						hasCandidate = true
						break
					}
				}
				if !hasCandidate {
					expectedBranch := git.BranchName(num, iss.Title)
					if exists, _ := g.BranchExists(repoRoot, expectedBranch); exists {
						c := findOrAdd(expectedBranch, num)
						c.Title = iss.Title
					}
				}
			}
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("resume: cannot resolve %q from local state, git branches, worktrees, or GitHub; refusing to create new branch or PR", target)
	}

	// Enrich candidates with worktree and existing state records
	for _, c := range candidates {
		if c.Worktree == "" {
			for _, wt := range wtList {
				wtBranch := strings.TrimPrefix(wt.Branch, "refs/heads/")
				if wtBranch == c.Branch {
					c.Worktree = wt.Path
					break
				}
			}
		}
		if store != nil {
			if wfs, err := store.List(); err == nil {
				for _, w := range wfs {
					if w.Branch == c.Branch {
						if c.Worktree == "" && w.Worktree != "" {
							c.Worktree = w.Worktree
						}
						if c.Issue == 0 && w.Issue > 0 {
							c.Issue = w.Issue
						}
						if c.Mode == "" && w.Mode != "" {
							c.Mode = w.Mode
						}
						if c.Status == "" && w.Status != "" {
							c.Status = w.Status
						}
						if c.Agent == "" && w.Agent != "" {
							c.Agent = w.Agent
						}
						if c.AgentMode == "" && w.AgentMode != "" {
							c.AgentMode = w.AgentMode
						}
					}
				}
			}
		}
	}

	if len(candidates) > 1 {
		return nil, fmt.Errorf("resume: multiple candidates found for %q; specify exact branch or issue to resume safely", target)
	}

	candidate := candidates[0]
	if candidate.Branch == "" {
		return nil, fmt.Errorf("resume: no existing branch found for %q; resume will never create a new branch", target)
	}

	return candidate, nil
}

// Resume safely reconnects to existing work without creating any new branch or PR.
func Resume(ctx context.Context, g GitRunner, store *state.Store, gh ports.GhClient, opts ResumeOptions) (ResumeResult, error) {
	if opts.WorkDir == "" {
		return ResumeResult{}, fmt.Errorf("resume: working directory is required")
	}
	repoRoot, err := g.RepoRoot(opts.WorkDir)
	if err != nil {
		return ResumeResult{}, fmt.Errorf("resume: %w", err)
	}

	candidate, err := ResolveTarget(ctx, g, store, gh, repoRoot, opts.Target, opts.Repair)
	if err != nil {
		return ResumeResult{}, err
	}

	// Verify the branch exists in Git
	exists, err := g.BranchExists(repoRoot, candidate.Branch)
	if err != nil {
		return ResumeResult{}, fmt.Errorf("resume: check branch %s: %w", candidate.Branch, err)
	}
	if !exists {
		return ResumeResult{}, fmt.Errorf("resume: branch %q does not exist; resume will never create a new branch", candidate.Branch)
	}

	// Worktree / Checkout handling
	worktree := candidate.Worktree
	if worktree != "" {
		if _, err := os.Stat(worktree); err != nil {
			_ = g.WorktreeAdd(repoRoot, worktree, candidate.Branch)
		}
	} else if opts.Worktree != "" {
		worktree = opts.Worktree
		if worktree == "auto" {
			worktree = filepath.Join(repoRoot, ".worktrees", fmt.Sprintf("issue-%d", candidate.Issue))
			if candidate.Part != "" {
				worktree += "-" + candidate.Part
			}
		}
		if err := g.WorktreeAdd(repoRoot, worktree, candidate.Branch); err != nil {
			return ResumeResult{}, fmt.Errorf("resume: worktree %s: %w", worktree, err)
		}
		candidate.Worktree = worktree
	} else {
		if err := g.CheckoutBranch(repoRoot, candidate.Branch); err != nil {
			return ResumeResult{}, fmt.Errorf("resume: checkout %s: %w", candidate.Branch, err)
		}
	}

	// Status transition: blocked or closed -> in_progress
	if candidate.Status == "" || candidate.Status == state.StatusBlocked || candidate.Status == state.StatusClosed {
		candidate.Status = state.StatusInProgress
		candidate.Pane = ""
	}
	if opts.Agent != "" {
		candidate.Agent = opts.Agent
	}
	if opts.AgentMode != "" {
		candidate.AgentMode = opts.AgentMode
	}
	if candidate.Mode == "" {
		candidate.Mode = state.ModeImplement
	}

	repoURL := g.OriginURL(repoRoot)
	if repoURL == "" {
		repoURL = "local"
	}

	w := state.Workflow{
		Repository:   repoURL,
		Issue:        candidate.Issue,
		Mode:         candidate.Mode,
		Part:         candidate.Part,
		Branch:       candidate.Branch,
		Worktree:     candidate.Worktree,
		Pane:         candidate.Pane,
		PullRequests: candidate.PullRequests,
		Status:       candidate.Status,
		MergePolicy:  candidate.MergePolicy,
		PolicyReason: candidate.PolicyReason,
		AgentMode:    candidate.AgentMode,
		Agent:        candidate.Agent,
		Attempts:     0,
	}

	if opts.Repair {
		if err := store.RepairUpsert(w); err != nil {
			return ResumeResult{}, fmt.Errorf("resume: repair state: %w", err)
		}
	} else {
		if err := store.Upsert(w); err != nil {
			return ResumeResult{}, fmt.Errorf("resume: %w", err)
		}
	}

	issueDetail := ports.Issue{
		Number: candidate.Issue,
		Title:  candidate.Title,
	}
	if candidate.Issue > 0 && gh != nil {
		if iss, err := gh.View(ctx, candidate.Issue); err == nil {
			issueDetail = iss
		}
	}

	return ResumeResult{
		Issue:      issueDetail,
		Branch:     candidate.Branch,
		Worktree:   candidate.Worktree,
		Status:     candidate.Status,
		Repository: repoURL,
		Mode:       candidate.Mode,
		RepoRoot:   repoRoot,
		Pane:       candidate.Pane,
		Agent:      candidate.Agent,
		AgentMode:  candidate.AgentMode,
	}, nil
}
