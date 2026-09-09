// Package ports isolates external CLI dependencies behind interfaces.
// See docs/rfc-22-language-migration.md §5 for the design rationale.
package ports

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// IssueSummary is a single row of `gh issue list`.
type IssueSummary struct {
	Number int
	Title  string
}

// MergedIssue is a recently merged/closed issue with its resolution time.
type MergedIssue struct {
	Number   int
	Title    string
	MergedAt time.Time
}

// Issue is the detail of `gh issue view`.
type Issue struct {
	Number int
	Title  string
	Body   string
	State  string
}

// GhClient abstracts `gh issue list/view` (transparent auth via gh CLI)
// plus the PR/CI operations `finish` needs (issue #41).
type GhClient interface {
	ListOpen(ctx context.Context) ([]IssueSummary, error)
	View(ctx context.Context, number int) (Issue, error)
	// PrChecks mirrors `gh pr checks <pr> --json bucket,name,state`.
	PrChecks(ctx context.Context, pr int) ([]PRCheck, error)
	// PrInfo mirrors `gh pr view <pr> --json number,state,mergeable,mergeStateStatus`.
	PrInfo(ctx context.Context, pr int) (PRInfo, error)
	// PrBody mirrors `gh pr view <pr> --json body`.
	PrBody(ctx context.Context, pr int) (string, error)
	// PrMerge mirrors `gh pr merge <pr> --squash --delete-branch`.
	PrMerge(ctx context.Context, pr int) error
	// RepoAllowsAutoMerge mirrors `gh api repos/<owner>/<repo> --jq .allow_auto_merge`.
	RepoAllowsAutoMerge(ctx context.Context, repo string) (bool, error)
	// IssueClose mirrors `gh issue close <n>`.
	IssueClose(ctx context.Context, number int) error
	// IssueComment mirrors `gh issue comment <n> --body <body>`.
	IssueComment(ctx context.Context, number int, body string) error
	// AuthStatus mirrors `gh auth status` (exit 0 = authenticated).
	AuthStatus(ctx context.Context) error
	// ApiUser mirrors `gh api user --jq .login` (reachability probe).
	ApiUser(ctx context.Context) (string, error)
	// LatestReleaseTag mirrors `gh api repos/<repo>/releases/latest --jq .tag_name`.
	LatestReleaseTag(ctx context.Context, repo string) (string, error)
	// BrowseIssue mirrors `gh issue view <n> --web`.
	BrowseIssue(ctx context.Context, number int) error
	// ListByLabel mirrors `gh issue list --state open --label <l> --json number,title`
	// (dispatch queue: docs/rfc-inbox-ux.md §7).
	ListByLabel(ctx context.Context, label string) ([]IssueSummary, error)
	// ListMergedSince mirrors `gh issue list --state closed --json number,title,closedAt --limit 50`
	// and returns closed issues with closedAt mapped to MergedAt.
	ListMergedSince(ctx context.Context, since time.Time) ([]MergedIssue, error)
	// IssueAddLabel mirrors `gh issue edit <n> --add-label <l>`.
	IssueAddLabel(ctx context.Context, number int, label string) error
	// IssueRemoveLabel mirrors `gh issue edit <n> --remove-label <l>`.
	IssueRemoveLabel(ctx context.Context, number int, label string) error
	// IssueEditBody mirrors `gh issue edit <n> --body-file -` with body on
	// stdin (inbox `e`: docs/rfc-inbox-ux.md §5.2). stdin keeps arbitrary
	// bodies — leading dashes, huge text — out of argv.
	IssueEditBody(ctx context.Context, number int, body string) error
	// PrFiles mirrors `gh pr view <pr> --json files --jq '.files[].path'`
	// (guardrail: docs/rfc-inbox-ux.md §7).
	PrFiles(ctx context.Context, pr int) ([]string, error)
	// RepoDefaultBranch mirrors `gh api repos/<repo> --jq .default_branch`.
	RepoDefaultBranch(ctx context.Context, repo string) (string, error)
	// BranchProtection mirrors `gh api repos/<repo>/branches/<branch>/protection`
	// (404 = no classic protection) merged with the rulesets that apply to the
	// branch (`gh api repos/<repo>/rules/branches/<branch>`). Issue #67.
	BranchProtection(ctx context.Context, repo, branch string) (BranchProtection, error)
	// IssueCreate mirrors `gh issue create --title <t> --body <b> --label <l>...`
	// and returns the created issue (number parsed from the printed URL).
	// Spec AI: docs/rfc-inbox-ux.md §8.
	IssueCreate(ctx context.Context, title, body string, labels []string) (IssueRef, error)
	// IssueComments mirrors `gh issue view <n> --json comments`.
	IssueComments(ctx context.Context, number int) ([]Comment, error)
	// IssueEdit mirrors `gh issue edit <n> --title <t> --body-file <f>`.
	IssueEdit(ctx context.Context, number int, title, body string) error
}

// BranchProtection is what `lead doctor` / `lead dispatch` need to know
// about a branch: is any protection in force, and which status checks
// must pass before merging.
type BranchProtection struct {
	// Protected is true when classic protection or at least one ruleset
	// (pull_request / required_status_checks) applies to the branch.
	Protected bool
	// RequiredChecks lists the required status-check contexts (deduplicated).
	RequiredChecks []string
	// RequiresPR is true when direct pushes are blocked (PR required).
	RequiresPR bool
}

// IssueRef identifies a created issue.
type IssueRef struct {
	Number int
	URL    string
}

// Comment is one row of `gh issue view --json comments`.
type Comment struct {
	Author    string
	Body      string
	CreatedAt string
}

// PRCheck is one CI check row. Bucket is pass/fail/pending/skipping/cancel.
type PRCheck struct {
	Name   string
	Bucket string
	State  string
}

// PRInfo is the subset of `gh pr view` finish needs.
type PRInfo struct {
	Number           int
	State            string
	Mergeable        string
	MergeStateStatus string
}

// Direction is the herdr pane split direction.
type Direction string

const (
	DirectionRight Direction = "right"
	DirectionDown  Direction = "down"
)

// HerdrRunner abstracts `herdr pane split/send-text`.
// The agent command is prepared in the new pane for human review;
// it is never auto-sent (no `pane run`).
// Peek opens an agent log or pane from the inbox (issue #103).
type HerdrRunner interface {
	Split(ctx context.Context, dir Direction, ratio float64) (paneID string, err error)
	SendText(ctx context.Context, paneID string, text string) error
	Peek(ctx context.Context, logPath, pane string) error
}

// AgentLauncher runs a coding agent directly in the current terminal
// (inline fallback when Herdr is unavailable).
type AgentLauncher interface {
	Launch(ctx context.Context, agent string, prompt string) error
}

// BinaryNotFoundError reports a missing external binary on PATH.
// Callers use it to fall back gracefully (e.g. InlineRunner).
type BinaryNotFoundError struct {
	Binary string
}

func (e *BinaryNotFoundError) Error() string {
	return fmt.Sprintf("%s: executable not found on PATH", e.Binary)
}

// IsBinaryNotFound reports whether err wraps a *BinaryNotFoundError.
func IsBinaryNotFound(err error) bool {
	var target *BinaryNotFoundError
	return errors.As(err, &target)
}
