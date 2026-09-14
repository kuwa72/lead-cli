package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// Names of the repository-side safety checks (issue #67, RFC inbox §9).
// `lead doctor` renders them as optional warnings; since issue #200 a gap
// no longer blocks dispatch, it only makes unattended merges less safe.
const (
	CheckBranchProtection = "branch protection"
	CheckRequiredChecks   = "required checks"
	CheckAutoMerge        = "auto-merge"
)

// Protection is the inspected state of a repository's default branch.
type Protection struct {
	Repo          string
	DefaultBranch string
	Branch        ports.BranchProtection
	AutoMerge     bool
}

// InspectProtection asks GitHub about the default branch of repo
// ("owner/name"): protection/rulesets, required status checks, and the
// repository allow_auto_merge setting.
func InspectProtection(ctx context.Context, gh ports.GhClient, repo string) (Protection, error) {
	p := Protection{Repo: repo}
	branch, err := gh.RepoDefaultBranch(ctx, repo)
	if err != nil {
		return p, fmt.Errorf("default branch of %s: %w", repo, err)
	}
	p.DefaultBranch = branch
	bp, err := gh.BranchProtection(ctx, repo, branch)
	if err != nil {
		return p, fmt.Errorf("protection of %s@%s: %w", repo, branch, err)
	}
	p.Branch = bp
	allowed, err := gh.RepoAllowsAutoMerge(ctx, repo)
	if err != nil {
		return p, fmt.Errorf("auto-merge setting of %s: %w", repo, err)
	}
	p.AutoMerge = allowed
	return p, nil
}

// Checks renders the inspection as doctor rows. All three are optional:
// gaps warn (unattended merges are safer with them) but never fail doctor
// since issue #200. auto-merge off only changes `lead finish` behavior
// (it falls back to a manual --merge).
func (p Protection) Checks() []Check {
	b := p.DefaultBranch
	var out []Check
	if p.Branch.Protected {
		how := "protected"
		if p.Branch.RequiresPR {
			how += ", PR required"
		}
		out = append(out, Check{Name: CheckBranchProtection, OK: true,
			Detail: fmt.Sprintf("%s is %s", b, how)})
	} else {
		out = append(out, Check{Name: CheckBranchProtection,
			Detail: fmt.Sprintf("%s is not protected; add a branch protection rule or ruleset requiring a pull request (Settings > Branches / Rules); recommended for safe unattended dispatch", b)})
	}
	if n := len(p.Branch.RequiredChecks); n > 0 {
		out = append(out, Check{Name: CheckRequiredChecks, OK: true,
			Detail: fmt.Sprintf("%s requires: %s", b, strings.Join(p.Branch.RequiredChecks, ", "))})
	} else {
		out = append(out, Check{Name: CheckRequiredChecks,
			Detail: fmt.Sprintf("%s has no required status checks; mark the CI job as required so a failing build cannot be merged; recommended for safe unattended dispatch", b)})
	}
	if p.AutoMerge {
		out = append(out, Check{Name: CheckAutoMerge, OK: true, Detail: "allow_auto_merge enabled"})
	} else {
		out = append(out, Check{Name: CheckAutoMerge,
			Detail: "allow_auto_merge disabled; `lead finish` will pause for a manual --merge (enable in Settings > General)"})
	}
	return out
}

// Missing returns the detail lines of the failing branch-protection and
// required-checks rows (empty when the repository is safe for unattended
// merges). Used only for the dispatch warning (issue #200); the rows
// themselves are optional in doctor output.
func (p Protection) Missing() []string {
	var m []string
	for _, c := range p.Checks() {
		if c.OK {
			continue
		}
		switch c.Name {
		case CheckBranchProtection, CheckRequiredChecks:
			m = append(m, c.Detail)
		}
	}
	return m
}

// protectionChecks produces the doctor rows for Deps.Repo, or explains why
// they were skipped (offline, no GitHub origin, unreachable).
func protectionChecks(ctx context.Context, d Deps) []Check {
	skipped := func(reason string) []Check {
		return []Check{
			{Name: CheckBranchProtection, Detail: reason},
			{Name: CheckRequiredChecks, Detail: reason},
			{Name: CheckAutoMerge, Detail: reason},
		}
	}
	switch {
	case d.Offline:
		return skipped("skipped (--offline)")
	case d.Repo == "":
		return skipped("skipped (no GitHub origin in this directory)")
	case d.Gh == nil:
		return skipped("gh client unavailable")
	}
	p, err := InspectProtection(ctx, d.Gh, d.Repo)
	if err != nil {
		return []Check{
			{Name: CheckBranchProtection, Detail: fmt.Sprintf("unknown: %v", err)},
			{Name: CheckRequiredChecks, Detail: fmt.Sprintf("unknown: %v", err)},
			{Name: CheckAutoMerge, Detail: fmt.Sprintf("unknown: %v", err)},
		}
	}
	return p.Checks()
}
