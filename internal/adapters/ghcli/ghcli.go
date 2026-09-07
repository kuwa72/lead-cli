// Package ghcli implements ports.GhClient via the `gh` CLI
// (transparent auth; go-gh library adoption is deferred).
package ghcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// ListLimit mirrors legacy bin/hgf `gh issue list --limit 50`.
const ListLimit = 50

// Client calls the `gh` CLI. Zero value is usable.
type Client struct {
	// Bin is the gh binary name or path. Empty means "gh".
	Bin string
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

func (c *Client) lookPath() func(string) (string, error) {
	if c.LookPath != nil {
		return c.LookPath
	}
	return exec.LookPath
}

// run executes bin with args, returning stdout. Exit failures surface
// gh's stderr so callers see the real cause.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	bin := c.bin()
	if _, err := c.lookPath()(bin); err != nil {
		return nil, &ports.BinaryNotFoundError{Binary: bin}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
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

// ListOpen runs `gh issue list --state open --limit 50 --json number,title`.
func (c *Client) ListOpen(ctx context.Context) ([]ports.IssueSummary, error) {
	out, err := c.run(ctx, "issue", "list",
		"--state", "open",
		"--limit", strconv.Itoa(ListLimit),
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
