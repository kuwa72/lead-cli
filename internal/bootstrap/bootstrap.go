// Package bootstrap inspects uninitialized project directories and hands
// 0→1 setup off to an interactive LLM coding agent.
package bootstrap

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kuwa72/lead-cli/internal/adapters/agent"
	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/workflow"
)

// Status describes the repository initialization state of a directory.
type Status struct {
	Dir          string
	InGitRepo    bool
	RepoRoot     string
	OriginURL    string
	RepoSlug     string
	HasAgentsMD  bool
	HasWorkflows bool
}

// IsUninitialized reports whether the directory lacks a git repository or a
// GitHub origin remote slug.
func (s Status) IsUninitialized() bool {
	return !s.InGitRepo || s.RepoSlug == ""
}

// AgentLauncher launches an interactive coding agent with a prompt.
type AgentLauncher interface {
	LaunchForMode(ctx context.Context, agentName, prompt, mode string) error
}

// Options configures one bootstrap run.
type Options struct {
	Dir      string
	Goal     string
	Agent    string
	DryRun   bool
	Git      workflow.GitRunner
	Launcher AgentLauncher
	Out      io.Writer
}

// Inspect checks whether dir is inside a git repository with a GitHub origin
// and whether AGENTS.md and CI workflows already exist.
func Inspect(dir string, g workflow.GitRunner) Status {
	st := Status{Dir: dir}
	if g != nil {
		if root, err := g.RepoRoot(dir); err == nil && root != "" {
			st.InGitRepo = true
			st.RepoRoot = root
		}
		if st.InGitRepo {
			st.OriginURL = g.OriginURL(st.RepoRoot)
			if isGitHubRemote(st.OriginURL) {
				st.RepoSlug = git.RepoSlug(st.OriginURL)
			}
		}
	}
	checkDir := dir
	if st.RepoRoot != "" {
		checkDir = st.RepoRoot
	}
	if _, err := os.Stat(filepath.Join(checkDir, "AGENTS.md")); err == nil {
		st.HasAgentsMD = true
	}
	st.HasWorkflows = hasCIWorkflows(checkDir)
	return st
}

func isGitHubRemote(remote string) bool {
	return strings.Contains(strings.ToLower(remote), "github.com")
}

func hasCIWorkflows(dir string) bool {
	entries, err := os.ReadDir(filepath.Join(dir, ".github", "workflows"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".yml" || ext == ".yaml" {
			return true
		}
	}
	return false
}

// Prompt constructs the interactive 0→1 bootstrap prompt from status and goal.
func Prompt(st Status, goal string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are bootstrapping a 0→1 project from %s so `lead` can drive issue-based development.\n\n", st.Dir)
	b.WriteString("## Current Directory State\n")
	fmt.Fprintf(&b, "- git repository: %v\n", st.InGitRepo)
	if st.RepoSlug != "" {
		fmt.Fprintf(&b, "- GitHub origin: %s (%s)\n", st.RepoSlug, st.OriginURL)
	} else {
		b.WriteString("- GitHub origin: missing\n")
	}
	fmt.Fprintf(&b, "- AGENTS.md: %v\n", st.HasAgentsMD)
	fmt.Fprintf(&b, "- .github/workflows: %v\n", st.HasWorkflows)

	if g := strings.TrimSpace(goal); g != "" {
		fmt.Fprintf(&b, "\n## Application Goal\n%s\n", g)
	}

	b.WriteString("\n## Bootstrap Instructions\n")
	b.WriteString("Work interactively with the user to complete any missing steps below (skip steps that are already satisfied):\n")
	b.WriteString("1. Briefly confirm the project scope, language/stack, and architecture with the user if not already specified.\n")
	b.WriteString("2. Initialize git (e.g. `git init -b main`) if not yet a git repository, create a minimal working starter structure, fast tests, and a CI workflow under `.github/workflows/ci.yml`.\n")
	b.WriteString("3. Create or connect the GitHub remote repository (`origin`) using `gh repo create` (or `git remote add origin`) and push the initial `main` commit.\n")
	b.WriteString("4. Run `lead enable` to set up labels, workflow skills, and `AGENTS.md` (include concise fast verification commands in `AGENTS.md` for `lead`'s post-merge smoke agent).\n")
	b.WriteString("5. Create initial well-scoped GitHub Issues (with `## 受入条件`, `## スコープ外`, `## シナリオ`) via `lead new` / `gh issue create` so `lead` / `lead dispatch` can start working immediately.\n")
	return b.String()
}

// Run either prints the dry-run preview or launches an interactive coding agent.
func Run(ctx context.Context, opts Options) error {
	if opts.Dir == "" {
		return fmt.Errorf("bootstrap: working directory is required")
	}
	st := Inspect(opts.Dir, opts.Git)
	prompt := Prompt(st, opts.Goal)
	agentName := agent.Resolve(strings.TrimSpace(opts.Agent))

	if opts.DryRun {
		out := opts.Out
		if out == nil {
			out = os.Stdout
		}
		cmdStr, err := agent.CommandStringForMode(agentName, prompt, "interactive")
		if err != nil {
			return fmt.Errorf("bootstrap: %w", err)
		}
		fmt.Fprintf(out, "Bootstrap state: git=%v origin=%q agents_md=%v workflows=%v\n",
			st.InGitRepo, st.RepoSlug, st.HasAgentsMD, st.HasWorkflows)
		fmt.Fprintf(out, "Command:\n%s\n\nPrompt:\n%s", cmdStr, prompt)
		return nil
	}

	if opts.Launcher == nil {
		return fmt.Errorf("bootstrap: no agent launcher configured")
	}
	return opts.Launcher.LaunchForMode(ctx, agentName, prompt, "interactive")
}
