// Package ghcli implements ports.GhClient via the `gh` CLI
// (transparent auth; go-gh library adoption is deferred).
package ghcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// DefaultListLimit is the default page size for issue listings (issue #138).
const DefaultListLimit = 300

// Client calls the `gh` CLI. Zero value is usable.
type Client struct {
	// Bin is the gh binary name or path. Empty means "gh".
	Bin string
	// Limit overrides the default listing limit (300).
	Limit int
	// LookPath resolves the binary; defaults to exec.LookPath.
	// A missing binary yields *ports.BinaryNotFoundError so callers
	// can fall back gracefully.
	LookPath func(name string) (string, error)
}

// New returns a Client with defaults.
func New() *Client { return &Client{} }

var _ ports.GhClient = (*Client)(nil)

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "gh"
}

func (c *Client) limit() int {
	if c.Limit > 0 {
		return c.Limit
	}
	if env := os.Getenv("LEAD_ISSUE_LIMIT"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			return n
		}
	}
	return DefaultListLimit
}

func (c *Client) lookPath() func(string) (string, error) {
	if c.LookPath != nil {
		return c.LookPath
	}
	return exec.LookPath
}

// run executes bin with args, returning stdout. Exit failures surface
// gh's stderr so callers see the real cause.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	return c.runStdin(ctx, nil, args...)
}

// runStdin is run with stdin attached (nil = no stdin).
func (c *Client) runStdin(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	bin := c.bin()
	if _, err := c.lookPath()(bin); err != nil {
		return nil, &ports.BinaryNotFoundError{Binary: bin}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// ListOpen runs `gh issue list --state open --limit <limit> --json number,title`.
func (c *Client) ListOpen(ctx context.Context) ([]ports.IssueSummary, error) {
	out, err := c.run(ctx, "issue", "list",
		"--state", "open",
		"--limit", strconv.Itoa(c.limit()),
		"--json", "number,title")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, fmt.Errorf("gh issue list: decode JSON: %w", err)
	}
	summaries := make([]ports.IssueSummary, len(raw))
	for i, r := range raw {
		summaries[i] = ports.IssueSummary{Number: r.Number, Title: r.Title}
	}
	return summaries, nil
}

// View runs `gh issue view <n> --json number,title,body,state`.
func (c *Client) View(ctx context.Context, number int) (ports.Issue, error) {
	out, err := c.run(ctx, "issue", "view", strconv.Itoa(number),
		"--json", "number,title,body,state")
	if err != nil {
		return ports.Issue{}, err
	}
	var raw struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return ports.Issue{}, fmt.Errorf("gh issue view %d: decode JSON: %w", number, err)
	}
	return ports.Issue{
		Number: raw.Number,
		Title:  raw.Title,
		Body:   raw.Body,
		State:  raw.State,
	}, nil
}

// PrChecks runs `gh pr checks <pr> --json bucket,name,state`.
func (c *Client) PrChecks(ctx context.Context, pr int) ([]ports.PRCheck, error) {
	out, err := c.run(ctx, "pr", "checks", strconv.Itoa(pr),
		"--json", "bucket,name,state")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name   string `json:"name"`
		Bucket string `json:"bucket"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, fmt.Errorf("gh pr checks %d: decode JSON: %w", pr, err)
	}
	checks := make([]ports.PRCheck, len(raw))
	for i, r := range raw {
		checks[i] = ports.PRCheck{Name: r.Name, Bucket: r.Bucket, State: r.State}
	}
	return checks, nil
}

// PrInfo runs `gh pr view <pr> --json number,state,mergeable,mergeStateStatus`.
func (c *Client) PrInfo(ctx context.Context, pr int) (ports.PRInfo, error) {
	out, err := c.run(ctx, "pr", "view", strconv.Itoa(pr),
		"--json", "number,state,mergeable,mergeStateStatus")
	if err != nil {
		return ports.PRInfo{}, err
	}
	var raw struct {
		Number           int    `json:"number"`
		State            string `json:"state"`
		Mergeable        string `json:"mergeable"`
		MergeStateStatus string `json:"mergeStateStatus"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return ports.PRInfo{}, fmt.Errorf("gh pr view %d: decode JSON: %w", pr, err)
	}
	return ports.PRInfo{
		Number:           raw.Number,
		State:            raw.State,
		Mergeable:        raw.Mergeable,
		MergeStateStatus: raw.MergeStateStatus,
	}, nil
}

// PrHeadBranch runs `gh pr view <pr> --json headRefName` and returns the branch name.
func (c *Client) PrHeadBranch(ctx context.Context, pr int) (string, error) {
	out, err := c.run(ctx, "pr", "view", strconv.Itoa(pr), "--json", "headRefName")
	if err != nil {
		return "", err
	}
	var raw struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return "", fmt.Errorf("gh pr view %d: decode JSON: %w", pr, err)
	}
	return raw.HeadRefName, nil
}

// PrBody runs `gh pr view <pr> --json body` and returns the body field.
func (c *Client) PrBody(ctx context.Context, pr int) (string, error) {
	out, err := c.run(ctx, "pr", "view", strconv.Itoa(pr), "--json", "body")
	if err != nil {
		return "", err
	}
	var raw struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return "", fmt.Errorf("gh pr view %d: decode JSON: %w", pr, err)
	}
	return raw.Body, nil
}

// PrMerge runs `gh pr merge <pr> --squash --delete-branch` (squash default).
func (c *Client) PrMerge(ctx context.Context, pr int) error {
	_, err := c.run(ctx, "pr", "merge", strconv.Itoa(pr), "--squash", "--delete-branch")
	return err
}

// RepoAllowsAutoMerge runs `gh api repos/<repo> --jq .allow_auto_merge`.
func (c *Client) RepoAllowsAutoMerge(ctx context.Context, repo string) (bool, error) {
	out, err := c.run(ctx, "api", "repos/"+repo, "--jq", ".allow_auto_merge")
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(out)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("gh api repos/%s: unexpected allow_auto_merge %q", repo, strings.TrimSpace(string(out)))
	}
}

// IssueClose runs `gh issue close <n>`.
func (c *Client) IssueClose(ctx context.Context, number int) error {
	_, err := c.run(ctx, "issue", "close", strconv.Itoa(number))
	return err
}

// IssueComment runs `gh issue comment <n> --body <body>`.
func (c *Client) IssueComment(ctx context.Context, number int, body string) error {
	_, err := c.run(ctx, "issue", "comment", strconv.Itoa(number), "--body", body)
	return err
}

// ListByLabel runs `gh issue list --state open --label <l> --limit <limit> --json number,title,updatedAt`.
func (c *Client) ListByLabel(ctx context.Context, label string) ([]ports.IssueSummary, error) {
	out, err := c.run(ctx, "issue", "list",
		"--state", "open",
		"--label", label,
		"--limit", strconv.Itoa(c.limit()),
		"--json", "number,title,updatedAt")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number    int       `json:"number"`
		Title     string    `json:"title"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, fmt.Errorf("gh issue list --label %s: decode JSON: %w", label, err)
	}
	summaries := make([]ports.IssueSummary, len(raw))
	for i, r := range raw {
		summaries[i] = ports.IssueSummary{Number: r.Number, Title: r.Title, UpdatedAt: r.UpdatedAt}
	}
	return summaries, nil
}

// ListMergedSince runs `gh issue list --state closed --json number,title,closedAt --limit <limit>`
// and returns issues whose closedAt is treated as the merge time.
func (c *Client) ListMergedSince(ctx context.Context, since time.Time) ([]ports.MergedIssue, error) {
	out, err := c.run(ctx, "issue", "list",
		"--state", "closed",
		"--limit", strconv.Itoa(c.limit()),
		"--json", "number,title,closedAt")
	if err != nil {
		return nil, err
	}
	type rawIssue struct {
		Number   int       `json:"number"`
		Title    string    `json:"title"`
		ClosedAt time.Time `json:"closedAt"`
	}
	var raw []rawIssue
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, fmt.Errorf("gh issue list --state closed: decode JSON: %w", err)
	}
	outIssues := make([]ports.MergedIssue, 0, len(raw))
	for _, r := range raw {
		if !since.IsZero() && !r.ClosedAt.After(since) {
			continue
		}
		outIssues = append(outIssues, ports.MergedIssue{Number: r.Number, Title: r.Title, MergedAt: r.ClosedAt})
	}
	return outIssues, nil
}

// IssueAddLabel runs `gh issue edit <n> --add-label <l>`.
func (c *Client) IssueAddLabel(ctx context.Context, number int, label string) error {
	_, err := c.run(ctx, "issue", "edit", strconv.Itoa(number), "--add-label", label)
	return err
}

// IssueRemoveLabel runs `gh issue edit <n> --remove-label <l>`.
func (c *Client) IssueRemoveLabel(ctx context.Context, number int, label string) error {
	_, err := c.run(ctx, "issue", "edit", strconv.Itoa(number), "--remove-label", label)
	return err
}

// IssueEditBody runs `gh issue edit <n> --body-file -` feeding body on stdin.
func (c *Client) IssueEditBody(ctx context.Context, number int, body string) error {
	_, err := c.runStdin(ctx, strings.NewReader(body), "issue", "edit", strconv.Itoa(number), "--body-file", "-")
	return err
}

// PrFiles runs `gh pr view <pr> --json files --jq .files[].path`
// (one path per line).
func (c *Client) PrFiles(ctx context.Context, pr int) ([]string, error) {
	out, err := c.run(ctx, "pr", "view", strconv.Itoa(pr), "--json", "files", "--jq", ".files[].path")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// RepoDefaultBranch runs `gh api repos/<repo> --jq .default_branch`.
func (c *Client) RepoDefaultBranch(ctx context.Context, repo string) (string, error) {
	out, err := c.run(ctx, "api", "repos/"+repo, "--jq", ".default_branch")
	if err != nil {
		return "", err
	}
	b := strings.TrimSpace(string(out))
	if b == "" {
		return "", fmt.Errorf("gh api repos/%s: empty default_branch", repo)
	}
	return b, nil
}

// isHTTP404 reports whether a gh api failure was a 404 (gh prints
// "... (HTTP 404)" on stderr, which run surfaces in the error).
func isHTTP404(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 404")
}

// BranchProtection runs the classic protection endpoint (404 = unprotected)
// and then the rulesets endpoint, merging both into one view.
func (c *Client) BranchProtection(ctx context.Context, repo, branch string) (ports.BranchProtection, error) {
	var bp ports.BranchProtection
	seen := map[string]bool{}
	addCheck := func(ctxName string) {
		if ctxName != "" && !seen[ctxName] {
			seen[ctxName] = true
			bp.RequiredChecks = append(bp.RequiredChecks, ctxName)
		}
	}

	protPath := "repos/" + repo + "/branches/" + branch + "/protection"
	out, err := c.run(ctx, "api", protPath)
	switch {
	case err == nil:
		var raw struct {
			RequiredStatusChecks *struct {
				Contexts []string `json:"contexts"`
				Checks   []struct {
					Context string `json:"context"`
				} `json:"checks"`
			} `json:"required_status_checks"`
			RequiredPullRequestReviews *struct{} `json:"required_pull_request_reviews"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
			return bp, fmt.Errorf("gh api %s: decode JSON: %w", protPath, err)
		}
		bp.Protected = true
		bp.RequiresPR = raw.RequiredPullRequestReviews != nil
		if raw.RequiredStatusChecks != nil {
			for _, ctxName := range raw.RequiredStatusChecks.Contexts {
				addCheck(ctxName)
			}
			for _, ch := range raw.RequiredStatusChecks.Checks {
				addCheck(ch.Context)
			}
		}
	case isHTTP404(err):
		// no classic protection; rulesets may still apply
	default:
		return bp, err
	}

	rulesPath := "repos/" + repo + "/rules/branches/" + branch
	out, err = c.run(ctx, "api", rulesPath)
	if err != nil {
		if isHTTP404(err) {
			return bp, nil
		}
		return bp, err
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredStatusChecks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &rules); err != nil {
		return bp, fmt.Errorf("gh api %s: decode JSON: %w", rulesPath, err)
	}
	for _, r := range rules {
		switch r.Type {
		case "pull_request":
			bp.Protected = true
			bp.RequiresPR = true
		case "required_status_checks":
			bp.Protected = true
			for _, ch := range r.Parameters.RequiredStatusChecks {
				addCheck(ch.Context)
			}
		}
	}
	return bp, nil
}

// BrowseIssue runs `gh issue view <n> --web`.
func (c *Client) BrowseIssue(ctx context.Context, number int) error {
	_, err := c.run(ctx, "issue", "view", strconv.Itoa(number), "--web")
	return err
}

// AuthStatus runs `gh auth status` (exit 0 = authenticated).
func (c *Client) AuthStatus(ctx context.Context) error {
	_, err := c.run(ctx, "auth", "status")
	return err
}

// ApiUser runs `gh api user --jq .login`.
func (c *Client) ApiUser(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "api", "user", "--jq", ".login")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// IssueCreate runs `gh issue create --title <t> --body <b> --label <l>...`
// and parses the issue number from the URL gh prints on stdout
// (spec AI: docs/rfc-inbox-ux.md §8).
func (c *Client) IssueCreate(ctx context.Context, title, body string, labels []string) (ports.IssueRef, error) {
	args := []string{"issue", "create", "--title", title, "--body", body}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return ports.IssueRef{}, err
	}
	url := lastNonEmptyLine(string(out))
	n, err := numberFromIssueURL(url)
	if err != nil {
		return ports.IssueRef{URL: url}, fmt.Errorf("gh issue create: %w", err)
	}
	return ports.IssueRef{Number: n, URL: url}, nil
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// numberFromIssueURL extracts the trailing number of .../issues/<n>.
func numberFromIssueURL(url string) (int, error) {
	url = strings.TrimRight(url, "/")
	i := strings.LastIndex(url, "/")
	if i < 0 {
		return 0, fmt.Errorf("cannot parse issue number from %q", url)
	}
	n, err := strconv.Atoi(url[i+1:])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("cannot parse issue number from %q", url)
	}
	return n, nil
}

// IssueComments runs `gh issue view <n> --json comments`.
func (c *Client) IssueComments(ctx context.Context, number int) ([]ports.Comment, error) {
	out, err := c.run(ctx, "issue", "view", strconv.Itoa(number), "--json", "comments")
	if err != nil {
		return nil, err
	}
	var raw struct {
		Comments []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			Body      string `json:"body"`
			CreatedAt string `json:"createdAt"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, fmt.Errorf("gh issue view %d --json comments: decode JSON: %w", number, err)
	}
	comments := make([]ports.Comment, len(raw.Comments))
	for i, r := range raw.Comments {
		comments[i] = ports.Comment{Author: r.Author.Login, Body: r.Body, CreatedAt: r.CreatedAt}
	}
	return comments, nil
}

// IssueEdit runs `gh issue edit <n> --title <t> --body-file <tmp>`.
// The body goes through a temp file so long multiline bodies never hit
// argv limits.
func (c *Client) IssueEdit(ctx context.Context, number int, title, body string) error {
	f, err := os.CreateTemp("", "lead-issue-body-*.md")
	if err != nil {
		return fmt.Errorf("gh issue edit %d: temp body: %w", number, err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return fmt.Errorf("gh issue edit %d: write body: %w", number, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("gh issue edit %d: close body: %w", number, err)
	}
	_, err = c.run(ctx, "issue", "edit", strconv.Itoa(number), "--title", title, "--body-file", f.Name())
	return err
}

// LatestReleaseTag runs `gh api repos/<repo>/releases/latest --jq .tag_name`.
func (c *Client) LatestReleaseTag(ctx context.Context, repo string) (string, error) {
	out, err := c.run(ctx, "api", "repos/"+repo+"/releases/latest", "--jq", ".tag_name")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
