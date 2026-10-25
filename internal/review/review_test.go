package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

type fakeAgent struct {
	out        []byte
	err        error
	calls      int
	dir        string
	argv       []string
	logPath    string
	prompt     string
	promptPath string
}

func (a *fakeAgent) Run(_ context.Context, dir string, argv []string, logPath string) ([]byte, error) {
	a.calls++
	a.dir, a.argv, a.logPath = dir, argv, logPath
	if len(argv) > 0 {
		last := argv[len(argv)-1]
		if i := strings.LastIndex(last, "file: "); i >= 0 {
			a.promptPath = strings.TrimSpace(last[i+len("file: "):])
			if b, err := os.ReadFile(a.promptPath); err == nil {
				a.prompt = string(b)
			}
		}
	}
	return a.out, a.err
}

type fakeGit struct {
	head    string
	main    string
	headErr error
	addErr  error
	ensured []string
	added   []string
	removed []string
}

func (g *fakeGit) HeadSHA(string) (string, error) { return g.head, g.headErr }
func (g *fakeGit) MainWorktree(string) (string, error) {
	return g.main, nil
}
func (g *fakeGit) EnsureCommit(_, sha string) error {
	g.ensured = append(g.ensured, sha)
	return nil
}
func (g *fakeGit) WorktreeAddDetached(_, path, commit string) error {
	g.added = append(g.added, path+"@"+commit)
	return g.addErr
}
func (g *fakeGit) WorktreeRemove(_, path string, _ bool) error {
	g.removed = append(g.removed, path)
	return nil
}

const testSHA = "0123456789abcdef0123456789abcdef01234567"

func reviewGh() *testutil.FakeGhClient {
	return &testutil.FakeGhClient{
		PrBodies:       map[int]string{7: "Closes #246"},
		PrDiffs:        map[int]string{7: "diff --git a/x b/x\n+new"},
		Checks:         []ports.PRCheck{{Name: "test", Bucket: "pass", State: "SUCCESS"}},
		PRHeadSHA:      testSHA,
		RepoLabelNames: []string{state.ReviewPassLabel, state.ReviewNeedsWorkLabel},
		Issues: map[int]ports.Issue{246: {
			Number: 246, Title: "feat(review): gate", Body: "## 受入条件\n- review:pass",
		}},
	}
}

func newRunner(t *testing.T, gh ports.GhClient, a AgentRunner) *Runner {
	t.Helper()
	dir := t.TempDir()
	return &Runner{
		Gh: gh, Agent: a,
		Git:  &fakeGit{head: testSHA, main: dir},
		Opts: Options{PR: 7, Agent: "claude", LogDir: dir, WorkDir: dir, Repository: "o/r", RepoSlug: "o/r"},
	}
}

func TestReview_PassLabelsPRAndPostsComment(t *testing.T) {
	gh := reviewGh()
	agent := &fakeAgent{out: []byte("## 所見\n受入条件は満たしている\nVERDICT: pass")}
	r := newRunner(t, gh, agent)

	res, err := r.Review(context.Background())
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !res.Passed || res.Verdict != VerdictPass {
		t.Fatalf("Result = %+v, want pass", res)
	}
	if res.Issue != 246 {
		t.Errorf("issue = %d, want 246 derived from the PR body", res.Issue)
	}
	if len(gh.PRComments) != 1 || gh.PRComments[0].Number != 7 {
		t.Fatalf("PR comments = %+v, want one on PR 7", gh.PRComments)
	}
	if !strings.Contains(gh.PRComments[0].Body, "受入条件は満たしている") {
		t.Errorf("comment does not carry the review body:\n%s", gh.PRComments[0].Body)
	}
	if want := "<!-- lead-review sha=" + testSHA + " verdict=pass -->"; !strings.Contains(gh.PRComments[0].Body, want) {
		t.Errorf("comment missing the machine-readable marker %q:\n%s", want, gh.PRComments[0].Body)
	}
	if res.HeadSHA != testSHA {
		t.Errorf("HeadSHA = %q, want the reviewed head %q", res.HeadSHA, testSHA)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: state.ReviewPassLabel}}; !reflect.DeepEqual(gh.AddedLabels, want) {
		t.Errorf("added labels = %+v, want %+v", gh.AddedLabels, want)
	}
	if agent.calls != 1 || len(agent.argv) == 0 || agent.argv[0] != "claude" {
		t.Errorf("agent calls = %d argv = %v, want one claude run", agent.calls, agent.argv)
	}
	if agent.dir != r.Opts.WorkDir {
		t.Errorf("agent dir = %q, want %q", agent.dir, r.Opts.WorkDir)
	}
}

// The head must be captured before the diff: a push between the two calls
// would otherwise let the marker claim a commit the reviewer never saw
// (issue #249).
func TestReview_BindsHeadBeforeReadingDiff(t *testing.T) {
	gh := reviewGh()
	r := newRunner(t, gh, &fakeAgent{out: []byte("VERDICT: pass")})

	if _, err := r.Review(context.Background()); err != nil {
		t.Fatalf("Review: %v", err)
	}
	headIdx, diffIdx := -1, -1
	for i, call := range gh.CallOrder {
		switch call {
		case "PrHeadSHA":
			headIdx = i
		case "PrDiff":
			diffIdx = i
		}
	}
	if headIdx < 0 || diffIdx < 0 || headIdx > diffIdx {
		t.Fatalf("CallOrder = %v; want PrHeadSHA before PrDiff", gh.CallOrder)
	}
}

// issue #250: when the cwd is not the PR head, the reviewer runs in a
// temporary worktree under <repo>/.worktrees and the worktree is removed.
func TestReview_RunsInPRHeadWorktreeWhenCwdDiffers(t *testing.T) {
	gh := reviewGh()
	agent := &fakeAgent{out: []byte("VERDICT: pass")}
	mainRoot := t.TempDir()
	g := &fakeGit{head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", main: mainRoot}
	r := &Runner{
		Gh: gh, Agent: agent, Git: g,
		Opts: Options{PR: 7, Agent: "claude", LogDir: t.TempDir(), WorkDir: "/elsewhere", Repository: "o/r", RepoSlug: "o/r"},
	}

	res, err := r.Review(context.Background())
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	wantDir := filepath.Join(mainRoot, ".worktrees", "review-pr7")
	if agent.dir != wantDir {
		t.Errorf("agent dir = %q, want the PR-head worktree %q", agent.dir, wantDir)
	}
	if want := []string{wantDir + "@" + testSHA}; !reflect.DeepEqual(g.added, want) {
		t.Errorf("added worktrees = %v, want %v", g.added, want)
	}
	if want := []string{testSHA}; !reflect.DeepEqual(g.ensured, want) {
		t.Errorf("ensured commits = %v, want %v", g.ensured, want)
	}
	if want := []string{wantDir, wantDir}; !reflect.DeepEqual(g.removed, want) {
		t.Errorf("removed worktrees = %v, want the stale-cleanup and post-review removals %v", g.removed, want)
	}
	if res.HeadSHA != testSHA {
		t.Errorf("HeadSHA = %q, want %q", res.HeadSHA, testSHA)
	}
}

// issue #250: a cwd already at the PR head is reused as-is.
func TestReview_ReusesCwdWhenHeadMatches(t *testing.T) {
	gh := reviewGh()
	agent := &fakeAgent{out: []byte("VERDICT: pass")}
	g := &fakeGit{head: testSHA, main: t.TempDir()}
	dir := t.TempDir()
	r := &Runner{
		Gh: gh, Agent: agent, Git: g,
		Opts: Options{PR: 7, Agent: "claude", LogDir: dir, WorkDir: dir, Repository: "o/r", RepoSlug: "o/r"},
	}

	if _, err := r.Review(context.Background()); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if agent.dir != dir {
		t.Errorf("agent dir = %q, want the existing cwd %q", agent.dir, dir)
	}
	if len(g.added) != 0 || len(g.removed) != 0 {
		t.Errorf("worktrees added = %v removed = %v, want no worktree churn", g.added, g.removed)
	}
}

func TestReview_WorktreeFailureStopsBeforeAgent(t *testing.T) {
	gh := reviewGh()
	agent := &fakeAgent{out: []byte("VERDICT: pass")}
	g := &fakeGit{head: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", main: t.TempDir(), addErr: errors.New("add boom")}
	r := &Runner{
		Gh: gh, Agent: agent, Git: g,
		Opts: Options{PR: 7, Agent: "claude", LogDir: t.TempDir(), WorkDir: "/elsewhere", Repository: "o/r", RepoSlug: "o/r"},
	}

	if _, err := r.Review(context.Background()); err == nil || !strings.Contains(err.Error(), "add boom") {
		t.Fatalf("err = %v, want the worktree failure", err)
	}
	if agent.calls != 0 {
		t.Errorf("agent calls = %d, want none when the worktree cannot be created", agent.calls)
	}
	if len(gh.PRComments) != 0 || len(gh.AddedLabels) != 0 {
		t.Errorf("comments = %+v labels = %+v, want no publication", gh.PRComments, gh.AddedLabels)
	}
}

// issue #250: the prompt goes through a temporary file, so a diff larger
// than Linux's MAX_ARG_STRLEN (128 KiB) still starts the agent.
func TestReview_PromptPassedViaFileForLargeDiff(t *testing.T) {
	gh := reviewGh()
	big := "diff --git a/big b/big\n+" + strings.Repeat("x", 200*1024) + "\n"
	gh.PrDiffs[7] = big
	agent := &fakeAgent{out: []byte("VERDICT: pass")}
	r := newRunner(t, gh, agent)

	if _, err := r.Review(context.Background()); err != nil {
		t.Fatalf("Review with a 200 KiB diff: %v", err)
	}
	if !strings.Contains(agent.prompt, "diff --git a/big b/big") {
		t.Errorf("prompt file missing the diff header")
	}
	if len(agent.prompt) < 128*1024 {
		t.Errorf("prompt file is %d bytes, want the full diff", len(agent.prompt))
	}
	for _, arg := range agent.argv {
		if len(arg) > 128*1024 {
			t.Errorf("argv contains a %d-byte argument; the prompt must not be in argv", len(arg))
		}
	}
	if _, err := os.Stat(agent.promptPath); !os.IsNotExist(err) {
		t.Errorf("prompt file %q was not removed after the review", agent.promptPath)
	}
}

// issue #250: a repository without the verdict labels gets them created
// before the label is applied.
func TestReview_CreatesMissingVerdictLabel(t *testing.T) {
	gh := reviewGh()
	gh.RepoLabelNames = []string{"needs-review"}
	r := newRunner(t, gh, &fakeAgent{out: []byte("VERDICT: pass")})

	if _, err := r.Review(context.Background()); err != nil {
		t.Fatalf("Review: %v", err)
	}
	want := []ports.LabelDefinition{{Name: state.ReviewPassLabel, Color: "0E8A16", Description: "Independent review passed"}}
	if !reflect.DeepEqual(gh.CreatedLabels, want) {
		t.Errorf("created labels = %+v, want %+v", gh.CreatedLabels, want)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: state.ReviewPassLabel}}; !reflect.DeepEqual(gh.AddedLabels, want) {
		t.Errorf("added labels = %+v, want %+v", gh.AddedLabels, want)
	}
}

func TestReview_ExistingVerdictLabelIsNotRecreated(t *testing.T) {
	gh := reviewGh()
	r := newRunner(t, gh, &fakeAgent{out: []byte("VERDICT: pass")})

	if _, err := r.Review(context.Background()); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(gh.CreatedLabels) != 0 {
		t.Errorf("created labels = %+v, want none when the label already exists", gh.CreatedLabels)
	}
}

// issue #250: label creation failure must stop before the PR comment so a
// comment never claims a verdict that was not labelled.
func TestReview_LabelCreationFailureStopsBeforeComment(t *testing.T) {
	gh := reviewGh()
	gh.RepoLabelNames = nil
	gh.CreateLabelErr = errors.New("create boom")
	r := newRunner(t, gh, &fakeAgent{out: []byte("VERDICT: pass")})

	_, err := r.Review(context.Background())
	if err == nil || !strings.Contains(err.Error(), "create boom") {
		t.Fatalf("err = %v, want the label creation failure", err)
	}
	if len(gh.PRComments) != 0 {
		t.Errorf("PR comments = %+v, want none before the label exists", gh.PRComments)
	}
	if len(gh.AddedLabels) != 0 {
		t.Errorf("added labels = %+v, want none", gh.AddedLabels)
	}
}

func TestReview_NeedsWorkLabelsAndPostsComment(t *testing.T) {
	gh := reviewGh()
	agent := &fakeAgent{out: []byte("## 所見\n- テストが実装の写経\nVERDICT: needs-work")}
	r := newRunner(t, gh, agent)

	res, err := r.Review(context.Background())
	if err != nil {
		t.Fatalf("Review needs-work returned an error (the CLI turns it into exit 1): %v", err)
	}
	if res.Passed || res.Verdict != VerdictNeedsWork {
		t.Fatalf("Result = %+v, want needs-work", res)
	}
	if len(gh.PRComments) != 1 || !strings.Contains(gh.PRComments[0].Body, "写経") {
		t.Fatalf("PR comments = %+v, want the findings", gh.PRComments)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: state.ReviewNeedsWorkLabel}}; !reflect.DeepEqual(gh.AddedLabels, want) {
		t.Errorf("added labels = %+v, want %+v", gh.AddedLabels, want)
	}
}

// Rerunning a review must drop the stale opposite label: a pass followed by
// needs-work would otherwise keep review:pass and let finish merge.
func TestReview_SwapsStaleVerdictLabel(t *testing.T) {
	gh := reviewGh()
	gh.PRLabels = []string{state.ReviewPassLabel}
	agent := &fakeAgent{out: []byte("VERDICT: needs-work")}
	r := newRunner(t, gh, agent)

	if _, err := r.Review(context.Background()); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: state.ReviewPassLabel}}; !reflect.DeepEqual(gh.RemovedLabels, want) {
		t.Errorf("removed labels = %+v, want stale review:pass removed", gh.RemovedLabels)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: state.ReviewNeedsWorkLabel}}; !reflect.DeepEqual(gh.AddedLabels, want) {
		t.Errorf("added labels = %+v, want %+v", gh.AddedLabels, want)
	}
}

// A missing or ambiguous verdict line must never pass.
func TestReview_InvalidVerdictFailsClosed(t *testing.T) {
	for _, out := range []string{"looks good to me", "VERDICT: LGTM", "VERDICT: pass\n```", ""} {
		gh := reviewGh()
		agent := &fakeAgent{out: []byte(out)}
		r := newRunner(t, gh, agent)

		res, err := r.Review(context.Background())
		if err == nil || !errors.Is(err, ErrInvalidVerdict) {
			t.Fatalf("output %q: err = %v, want ErrInvalidVerdict", out, err)
		}
		if res.Passed {
			t.Errorf("output %q: Passed = true, want fail", out)
		}
		for _, call := range gh.AddedLabels {
			if call.Label == state.ReviewPassLabel {
				t.Errorf("output %q: review:pass added on an invalid verdict", out)
			}
		}
	}
}

func TestReview_AgentFailureIsAnError(t *testing.T) {
	gh := reviewGh()
	agent := &fakeAgent{err: errors.New("agent boom")}
	r := newRunner(t, gh, agent)

	res, err := r.Review(context.Background())
	if err == nil || !strings.Contains(err.Error(), "agent boom") {
		t.Fatalf("err = %v, want the agent failure surfaced", err)
	}
	if res.Passed {
		t.Error("agent failure must not pass")
	}
	if len(gh.AddedLabels) != 0 || len(gh.PRComments) != 0 {
		t.Errorf("labels = %+v comments = %+v, want no verdict published on agent failure", gh.AddedLabels, gh.PRComments)
	}
}

func TestReview_DerivesIssueFromBranchWhenBodyHasNoCloser(t *testing.T) {
	gh := reviewGh()
	gh.PrBodies[7] = "no issue reference"
	gh.PR.HeadRefName = "issue/246-feat-review"
	r := newRunner(t, gh, &fakeAgent{out: []byte("VERDICT: pass")})

	res, err := r.Review(context.Background())
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Issue != 246 {
		t.Errorf("issue = %d, want 246 from the branch", res.Issue)
	}
}

func TestReview_UnknownIssueFails(t *testing.T) {
	gh := reviewGh()
	gh.PrBodies[7] = "no issue reference"
	gh.PR.HeadRefName = "topic/foo"
	r := newRunner(t, gh, &fakeAgent{out: []byte("VERDICT: pass")})

	if _, err := r.Review(context.Background()); err == nil || !strings.Contains(err.Error(), "--issue") {
		t.Fatalf("err = %v, want guidance to pass --issue", err)
	}
}

func TestParseVerdict_OnlyLooksAtTheLastLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"pass", "findings\nVERDICT: pass", VerdictPass, true},
		{"needs-work", "findings\nVERDICT: needs-work", VerdictNeedsWork, true},
		{"trailing blanks", "findings\nVERDICT: pass\n\n \n", VerdictPass, true},
		{"crlf", "findings\r\nVERDICT: needs-work\r\n", VerdictNeedsWork, true},
		{"earlier verdict ignored", "VERDICT: pass\nfindings\nVERDICT: needs-work", VerdictNeedsWork, true},
		{"prose after verdict", "VERDICT: pass\nthanks", "", false},
		{"no space", "VERDICT:pass", "", false},
		{"wrong case", "verdict: pass", "", false},
		{"fenced", "findings\n```\nVERDICT: pass\n```", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseVerdict([]byte(tc.in))
			if got != tc.want || ok != tc.ok {
				t.Errorf("ParseVerdict(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The marker is machine-readable and must round-trip the reviewed head.
// Since issue #250 only the first marker in the newest comment counts:
// a reviewer quoting a marker from the diff must not override the real
// verdict that CommentBody writes first.
func TestLatestMarker_TakesTheNewestOccurrence(t *testing.T) {
	older := testSHA
	newer := "fedcba9876543210fedcba9876543210fedcba98"
	cases := []struct {
		name     string
		comments []ports.Comment
		want     Marker
		ok       bool
	}{
		{"none", nil, Marker{}, false},
		{"plain comments only", []ports.Comment{{Body: "LGTM"}}, Marker{}, false},
		{"single", []ports.Comment{{Body: "<!-- lead-review sha=" + older + " verdict=pass -->"}},
			Marker{SHA: older, Verdict: VerdictPass}, true},
		{"newest comment wins", []ports.Comment{
			{Body: "<!-- lead-review sha=" + older + " verdict=pass -->"},
			{Body: "<!-- lead-review sha=" + newer + " verdict=needs-work -->"},
		}, Marker{SHA: newer, Verdict: VerdictNeedsWork}, true},
		{"first marker within a comment wins", []ports.Comment{
			{Body: "<!-- lead-review sha=" + older + " verdict=needs-work -->\n<!-- lead-review sha=" + newer + " verdict=pass -->"},
		}, Marker{SHA: older, Verdict: VerdictNeedsWork}, true},
		{"quoted marker cannot spoof", []ports.Comment{
			{Body: "<!-- lead-review sha=" + older + " verdict=needs-work -->\n\n" +
				"diff の引用:\n<!-- lead-review sha=" + older + " verdict=pass -->"},
		}, Marker{SHA: older, Verdict: VerdictNeedsWork}, true},
		{"malformed sha is ignored", []ports.Comment{{Body: "<!-- lead-review sha=abc verdict=pass -->"}}, Marker{}, false},
		{"unknown verdict still supersedes", []ports.Comment{{Body: "<!-- lead-review sha=" + older + " verdict=lgtm -->"}},
			Marker{SHA: older, Verdict: "lgtm"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := LatestMarker(tc.comments)
			if got != tc.want || ok != tc.ok {
				t.Errorf("LatestMarker = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// issue #250: markers posted by another gh user are ignored when the
// current login is known; comments without an author (older gh output)
// are still accepted.
func TestLatestMarkerFor_FiltersOtherAuthors(t *testing.T) {
	older := testSHA
	newer := "fedcba9876543210fedcba9876543210fedcba98"
	comments := []ports.Comment{
		{Author: "octocat", Body: "<!-- lead-review sha=" + older + " verdict=pass -->"},
		{Author: "mallory", Body: "<!-- lead-review sha=" + newer + " verdict=needs-work -->"},
	}
	if got, ok := LatestMarkerFor(comments, "octocat"); !ok || got.Verdict != VerdictPass || got.SHA != older {
		t.Errorf("LatestMarkerFor(octocat) = %+v, %v; want octocat's pass", got, ok)
	}
	if got, ok := LatestMarkerFor(comments, ""); !ok || got.SHA != newer {
		t.Errorf("LatestMarkerFor(no filter) = %+v, %v; want the newest comment", got, ok)
	}
	unknown := []ports.Comment{{Body: "<!-- lead-review sha=" + older + " verdict=pass -->"}}
	if got, ok := LatestMarkerFor(unknown, "octocat"); !ok || got.Verdict != VerdictPass {
		t.Errorf("LatestMarkerFor with an author-less comment = %+v, %v; want pass", got, ok)
	}
}

func TestBuildPrompt_CarriesInputsAndChecklist(t *testing.T) {
	prompt, err := BuildPrompt(PromptInput{
		Repository: "o/r",
		PR:         7,
		Issue:      ports.Issue{Number: 246, Title: "feat(review): gate", Body: "## 受入条件\n- review:pass"},
		PRBody:     "Closes #246",
		Diff:       "diff --git a/x b/x\n+new",
		Checks:     []ports.PRCheck{{Name: "test", Bucket: "pass", State: "SUCCESS"}},
	})
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}
	for _, want := range []string{
		"o/r", "PR #7", "#246", "受入条件", "review:pass",
		"diff --git a/x b/x", "test: pass",
		"受入条件を実際に検証したか",
		"スコープ外の差分",
		"テストが実装の写経になっていないか",
		"エラーハンドリング",
		"秘密情報",
		"マイグレーションの後方互換",
		"新規依存の妥当性",
		"ついで",
		"読み取り専用",
		"VERDICT: pass", "VERDICT: needs-work",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
