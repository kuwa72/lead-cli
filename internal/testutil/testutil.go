// Package testutil is the shared test foundation (issue #38):
// dummy-command PATH injection, ports fakes, and golden-file comparison.
// See docs/testing.md for the testing policy.
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// TempBin creates a temp dir and prepends it to PATH for the test.
// Dummy executables placed there take precedence over real CLIs.
func TempBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// EmptyBin creates an empty temp dir and sets it as the ONLY PATH entry
// for the test. Use it for missing-binary tests: no real CLI can leak
// through (unlike TempBin, which prepends and keeps the real PATH).
func EmptyBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	return dir
}

// InstallDummy creates an executable dummy `name` in a fresh temp bin dir
// (prepended to PATH) and returns its arg-log path. Every invocation appends
// one `<arg>` line per argv to the log, then runs body — a shell fragment
// whose exit status becomes the dummy's. Use LogLines/LogText to assert
// the received args (AGENTS.md rule: assert behavior, not source text).
func InstallDummy(t *testing.T, name, body string) (logPath string) {
	t.Helper()
	dir := TempBin(t)
	logPath = filepath.Join(dir, name+".args.log")
	script := "#!/bin/sh\n" +
		"printf '<%s>\\n' \"$@\" >> " + shellQuote(logPath) + "\n" +
		body + "\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("InstallDummy %s: %v", name, err)
	}
	return logPath
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// ClearLog truncates the arg log.
func ClearLog(t *testing.T, logPath string) {
	t.Helper()
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatalf("ClearLog: %v", err)
	}
}

// LogText returns the raw arg log (empty when the dummy never ran).
func LogText(t *testing.T, logPath string) string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("LogText: %v", err)
	}
	return string(b)
}

// LogLines returns the arg log split into lines (no trailing empty entry).
func LogLines(t *testing.T, logPath string) []string {
	t.Helper()
	text := strings.TrimSuffix(LogText(t, logPath), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// IssueComment records one FakeGhClient.IssueComment call.
type IssueComment struct {
	Number int
	Body   string
}

// FakeGhClient is an in-memory ports.GhClient for core-logic unit tests.
type FakeGhClient struct {
	Summaries []ports.IssueSummary
	Issues    map[int]ports.Issue
	ListErr   error
	ViewErr   error

	// Checks is returned by every PrChecks call; ChecksSeq (when non-empty)
	// returns ChecksSeq[min(call,last)] to model pending→pass transitions.
	Checks    []ports.PRCheck
	ChecksSeq [][]ports.PRCheck
	ChecksErr error

	PR    ports.PRInfo
	PRErr error

	MergeErr error

	// AutoMergeAllowed gates policy auto (repo setting).
	AutoMergeAllowed bool
	AutoMergeErr     error

	CloseErr   error
	CommentErr error

	AuthErr      error
	ApiUserLogin string
	ApiUserErr   error
	LatestTag    string
	LatestErr    error

	ListCalls   int
	ViewCalls   []int
	ChecksCalls int
	PRCalls     []int
	MergeCalls  []int
	Closed      []int
	Comments    []IssueComment
}

var _ ports.GhClient = (*FakeGhClient)(nil)

// ListOpen returns Summaries (or ListErr) and records the call.
func (f *FakeGhClient) ListOpen(ctx context.Context) ([]ports.IssueSummary, error) {
	f.ListCalls++
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	return f.Summaries, nil
}

// View returns Issues[number] (or ViewErr / unknown-number error)
// and records the call.
func (f *FakeGhClient) View(ctx context.Context, number int) (ports.Issue, error) {
	f.ViewCalls = append(f.ViewCalls, number)
	if f.ViewErr != nil {
		return ports.Issue{}, f.ViewErr
	}
	iss, ok := f.Issues[number]
	if !ok {
		return ports.Issue{}, fmt.Errorf("fake gh: issue #%d not found", number)
	}
	return iss, nil
}

// PrChecks returns the sequenced or static checks and records the call.
func (f *FakeGhClient) PrChecks(ctx context.Context, pr int) ([]ports.PRCheck, error) {
	call := f.ChecksCalls
	f.ChecksCalls++
	if f.ChecksErr != nil {
		return nil, f.ChecksErr
	}
	if len(f.ChecksSeq) > 0 {
		if call >= len(f.ChecksSeq) {
			call = len(f.ChecksSeq) - 1
		}
		return f.ChecksSeq[call], nil
	}
	return f.Checks, nil
}

// PrInfo returns the canned PR and records the call.
func (f *FakeGhClient) PrInfo(ctx context.Context, pr int) (ports.PRInfo, error) {
	f.PRCalls = append(f.PRCalls, pr)
	if f.PRErr != nil {
		return ports.PRInfo{}, f.PRErr
	}
	return f.PR, nil
}

// PrMerge records the call (or returns MergeErr).
func (f *FakeGhClient) PrMerge(ctx context.Context, pr int) error {
	f.MergeCalls = append(f.MergeCalls, pr)
	return f.MergeErr
}

// RepoAllowsAutoMerge returns the canned repo setting.
func (f *FakeGhClient) RepoAllowsAutoMerge(ctx context.Context, repo string) (bool, error) {
	return f.AutoMergeAllowed, f.AutoMergeErr
}

// IssueClose records the call (or returns CloseErr).
func (f *FakeGhClient) IssueClose(ctx context.Context, number int) error {
	f.Closed = append(f.Closed, number)
	return f.CloseErr
}

// IssueComment records the call (or returns CommentErr).
func (f *FakeGhClient) IssueComment(ctx context.Context, number int, body string) error {
	f.Comments = append(f.Comments, IssueComment{Number: number, Body: body})
	return f.CommentErr
}

// AuthStatus returns AuthErr (nil = authenticated).
func (f *FakeGhClient) AuthStatus(ctx context.Context) error {
	return f.AuthErr
}

// ApiUser returns the canned login (or ApiUserErr).
func (f *FakeGhClient) ApiUser(ctx context.Context) (string, error) {
	return f.ApiUserLogin, f.ApiUserErr
}

// LatestReleaseTag returns the canned tag (or LatestErr).
func (f *FakeGhClient) LatestReleaseTag(ctx context.Context, repo string) (string, error) {
	return f.LatestTag, f.LatestErr
}

// SplitCall records one FakeHerdrRunner.Split invocation.
type SplitCall struct {
	Dir   ports.Direction
	Ratio float64
}

// SendCall records one FakeHerdrRunner.SendText invocation.
type SendCall struct {
	PaneID string
	Text   string
}

// FakeHerdrRunner is an in-memory ports.HerdrRunner.
type FakeHerdrRunner struct {
	// PaneID returned by Split; defaults to "fake-pane" when empty.
	PaneID   string
	SplitErr error
	SendErr  error

	Splits []SplitCall
	Sends  []SendCall
}

var _ ports.HerdrRunner = (*FakeHerdrRunner)(nil)

// Split records the call and returns PaneID (or SplitErr).
func (f *FakeHerdrRunner) Split(ctx context.Context, dir ports.Direction, ratio float64) (string, error) {
	f.Splits = append(f.Splits, SplitCall{Dir: dir, Ratio: ratio})
	if f.SplitErr != nil {
		return "", f.SplitErr
	}
	if f.PaneID != "" {
		return f.PaneID, nil
	}
	return "fake-pane", nil
}

// SendText records the call (or returns SendErr).
func (f *FakeHerdrRunner) SendText(ctx context.Context, paneID string, text string) error {
	f.Sends = append(f.Sends, SendCall{PaneID: paneID, Text: text})
	return f.SendErr
}

// LaunchCall records one FakeAgentLauncher.Launch invocation.
type LaunchCall struct {
	Agent  string
	Prompt string
}

// FakeAgentLauncher is an in-memory ports.AgentLauncher.
type FakeAgentLauncher struct {
	LaunchErr error

	Launches []LaunchCall
}

var _ ports.AgentLauncher = (*FakeAgentLauncher)(nil)

// Launch records the call (or returns LaunchErr).
func (f *FakeAgentLauncher) Launch(ctx context.Context, agent string, prompt string) error {
	f.Launches = append(f.Launches, LaunchCall{Agent: agent, Prompt: prompt})
	return f.LaunchErr
}

// CheckGolden compares got against the golden file at goldenPath.
// With UPDATE_GOLDEN=1 it (re)writes the file — including missing parents —
// and returns nil. Without it, a missing file or any byte difference is
// an error (missing files hint at UPDATE_GOLDEN=1).
func CheckGolden(goldenPath string, got []byte) error {
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if dir := filepath.Dir(goldenPath); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("golden %s: mkdir: %w", goldenPath, err)
			}
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			return fmt.Errorf("golden %s: update: %w", goldenPath, err)
		}
		return nil
	}
	want, err := os.ReadFile(goldenPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("golden %s: file missing; run with UPDATE_GOLDEN=1 to create it", goldenPath)
	}
	if err != nil {
		return fmt.Errorf("golden %s: read: %w", goldenPath, err)
	}
	if string(want) != string(got) {
		return fmt.Errorf("golden mismatch %s: want %d bytes, got %d bytes; diff (-want +got):\n%s",
			goldenPath, len(want), len(got), lineDiff(string(want), string(got)))
	}
	return nil
}

// AssertGolden fails the test when CheckGolden reports a difference.
func AssertGolden(t *testing.T, goldenPath string, got []byte) {
	t.Helper()
	if err := CheckGolden(goldenPath, got); err != nil {
		t.Fatal(err)
	}
}

// AssertGoldenString is AssertGolden for string content.
func AssertGoldenString(t *testing.T, goldenPath, got string) {
	t.Helper()
	AssertGolden(t, goldenPath, []byte(got))
}

func lineDiff(want, got string) string {
	wLines := strings.Split(want, "\n")
	gLines := strings.Split(got, "\n")
	var sb strings.Builder
	n := len(wLines)
	if len(gLines) > n {
		n = len(gLines)
	}
	if n > 20 {
		n = 20 // keep failure output readable; byte counts already pinpoint size
	}
	for i := 0; i < n; i++ {
		var w, g string
		if i < len(wLines) {
			w = wLines[i]
		}
		if i < len(gLines) {
			g = gLines[i]
		}
		mark := " "
		if w != g {
			mark = "!"
		}
		fmt.Fprintf(&sb, "%s L%d want=%q got=%q\n", mark, i+1, w, g)
	}
	return sb.String()
}
