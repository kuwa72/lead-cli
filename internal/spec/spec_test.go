package spec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// fakeAgent records the launch and prints canned output.
type fakeAgent struct {
	Output string
	Err    error
	Calls  []struct {
		Dir     string
		Argv    []string
		LogPath string
	}
}

func (f *fakeAgent) Run(ctx context.Context, dir string, argv []string, logPath string) ([]byte, error) {
	f.Calls = append(f.Calls, struct {
		Dir     string
		Argv    []string
		LogPath string
	}{dir, argv, logPath})
	return []byte(f.Output), f.Err
}

const twoDrafts = `[{"title":"feat(cli): add lead say for one-liner issues","body":"## Mode\nimplement\n\n## Purpose\nfile issues"},
{"title":"docs: describe lead say in docs/prompts.md","body":"## Mode\ndocs\n\n## Purpose\ndocument it"}]`

func newRunner(t *testing.T, out string) (*Runner, *testutil.FakeGhClient, *fakeAgent, *bytes.Buffer) {
	t.Helper()
	gh := &testutil.FakeGhClient{Issues: map[int]ports.Issue{}}
	ag := &fakeAgent{Output: out}
	var buf bytes.Buffer
	r := &Runner{Gh: gh, Agent: ag, Out: &buf, Opts: Options{
		Agent: "claude", LogDir: t.TempDir(), WorkDir: t.TempDir(), Repository: "o/r", Rules: "TDD required",
	}}
	return r, gh, ag, &buf
}

// --- prompt goldens ---------------------------------------------------------

func TestBuildPrompt_Create_MatchesGolden(t *testing.T) {
	out, err := BuildPrompt(PromptInput{Repository: "o/r", OneLiner: "lead say で一言から Issue を起票したい", Rules: "TDD required"})
	if err != nil {
		t.Fatal(err)
	}
	testutil.AssertGoldenString(t, "testdata/prompt_create.golden", out)
}

func TestBuildPrompt_FollowUp_MatchesGolden(t *testing.T) {
	out, err := BuildPrompt(PromptInput{Repository: "o/r", OneLiner: "実機で --agent 省略時に落ちる", Rules: "TDD required",
		Parent: &ports.Issue{Number: 42, Title: "feat: dispatch", Body: "ready を払い出す"}})
	if err != nil {
		t.Fatal(err)
	}
	testutil.AssertGoldenString(t, "testdata/prompt_followup.golden", out)
}

func TestBuildPrompt_Redraft_MatchesGolden(t *testing.T) {
	out, err := BuildPrompt(PromptInput{Repository: "o/r", OneLiner: "受入条件に exit code を足して", Rules: "TDD required",
		Redraft: &RedraftInput{
			Issue:    ports.Issue{Number: 7, Title: "feat: x", Body: "## Purpose\ndo x"},
			Comments: []ports.Comment{{Author: "kuwa72", CreatedAt: "2026-09-08T00:00:00Z", Body: "exit code も見たい"}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	testutil.AssertGoldenString(t, "testdata/prompt_redraft.golden", out)
}

func TestBuildPrompt_DefaultsAndErrors(t *testing.T) {
	out, err := BuildPrompt(PromptInput{OneLiner: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, DefaultRules) || !strings.Contains(out, "\nlocal\n") {
		t.Errorf("defaults not applied:\n%s", out)
	}
	if _, err := BuildPrompt(PromptInput{}); err == nil {
		t.Error("empty one-liner accepted")
	}
	if _, err := BuildPrompt(PromptInput{OneLiner: "x", Parent: &ports.Issue{Number: 1}, Redraft: &RedraftInput{}}); err == nil {
		t.Error("parent+redraft accepted")
	}
}

// --- JSON parsing -------------------------------------------------------------

func TestParseDrafts(t *testing.T) {
	cases := map[string]struct {
		in      string
		want    int
		wantErr bool
	}{
		"clean array":   {twoDrafts, 2, false},
		"fenced json":   {"Here you go:\n```json\n" + twoDrafts + "\n```\nDone.", 2, false},
		"prose prefix":  {"Sure! The issues are:\n" + twoDrafts, 2, false},
		"single object": {`{"title":"t","body":"b"}`, 1, false},
		"fence no lang": {"```\n" + `[{"title":"t","body":"b"}]` + "\n```", 1, false},
		"empty":         {"", 0, true},
		"no json":       {"I cannot do that.", 0, true},
		"broken json":   {`[{"title":"t","body":}]`, 0, true},
		"wrong type":    {`["just","strings"]`, 0, true},
		"unterminated":  {`[{"title":"t","body":"b"}`, 0, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseDrafts([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Fatalf("got %d drafts, want %d: %+v", len(got), tc.want, got)
			}
		})
	}
}

func TestParseDraft_RequiresExactlyOne(t *testing.T) {
	if _, err := ParseDraft([]byte(twoDrafts)); err == nil {
		t.Error("two drafts accepted for redraft")
	}
	d, err := ParseDraft([]byte("```json\n{\"title\":\"t\",\"body\":\"b\"}\n```"))
	if err != nil || d.Title != "t" || d.Body != "b" {
		t.Errorf("ParseDraft = %+v, %v", d, err)
	}
}

// --- validation -----------------------------------------------------------------

func TestValidate_RejectsBadTitles(t *testing.T) {
	long := strings.Repeat("あ", 257)
	for name, d := range map[string]Draft{
		"empty title":  {Title: " ", Body: "b"},
		"trailing dot": {Title: "fix: thing.", Body: "b"},
		"multiline":    {Title: "a\nb", Body: "b"},
		"too long":     {Title: long, Body: "b"},
		"empty body":   {Title: "ok", Body: ""},
	} {
		if err := Validate([]Draft{d}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := Validate(nil); err == nil {
		t.Error("no drafts accepted")
	}
	if err := Validate([]Draft{{Title: "feat(cli): ok", Body: "b"}}); err != nil {
		t.Errorf("valid draft rejected: %v", err)
	}
}

func TestEnsureReference(t *testing.T) {
	if got := EnsureReference("see #42 for context", 42); got != "see #42 for context" {
		t.Errorf("already referenced body changed: %q", got)
	}
	if got := EnsureReference("see #421", 42); !strings.Contains(got, "元 Issue: #42\n") {
		t.Errorf("#421 counted as #42: %q", got)
	}
	if got := EnsureReference("no ref\n", 42); !strings.HasSuffix(got, "\n\n元 Issue: #42\n") {
		t.Errorf("reference not appended: %q", got)
	}
}

// --- Say: create --------------------------------------------------------------

func TestSay_CreatesIssuesWithLabelAndCrossReferences(t *testing.T) {
	r, gh, ag, out := newRunner(t, twoDrafts)
	res, err := r.Say(context.Background(), "一言")
	if err != nil {
		t.Fatal(err)
	}
	if len(ag.Calls) != 1 {
		t.Fatalf("agent calls = %d", len(ag.Calls))
	}
	call := ag.Calls[0]
	wantArgv := []string{"claude", "-p", "--dangerously-skip-permissions"}
	for i, w := range wantArgv {
		if call.Argv[i] != w {
			t.Fatalf("argv = %q, want prefix %q", call.Argv, wantArgv)
		}
	}
	if !strings.Contains(call.Argv[3], "一言") || !strings.Contains(call.Argv[3], "TDD required") {
		t.Errorf("prompt lacks one-liner/rules")
	}
	if call.Dir != r.Opts.WorkDir || !strings.HasPrefix(call.LogPath, r.Opts.LogDir) || !strings.HasSuffix(call.LogPath, ".log") {
		t.Errorf("dir/log = %q %q", call.Dir, call.LogPath)
	}
	if len(gh.Created) != 2 {
		t.Fatalf("created = %+v", gh.Created)
	}
	c0 := gh.Created[0]
	if c0.Title != "feat(cli): add lead say for one-liner issues" || c0.Body != "## Mode\nimplement\n\n## Purpose\nfile issues" ||
		len(c0.Labels) != 1 || c0.Labels[0] != "needs-review" {
		t.Errorf("create[0] = %+v", c0)
	}
	if gh.Created[1].Title != "docs: describe lead say in docs/prompts.md" {
		t.Errorf("create[1] = %+v", gh.Created[1])
	}
	if len(res.Created) != 2 || res.Created[0].Number != 101 || res.Created[1].Number != 102 {
		t.Errorf("res.Created = %+v", res.Created)
	}
	// cross references via edit
	if len(gh.Edits) != 2 {
		t.Fatalf("edits = %+v", gh.Edits)
	}
	if gh.Edits[0].Number != 101 || !strings.Contains(gh.Edits[0].Body, "- #102\n") || strings.Contains(gh.Edits[0].Body, "- #101\n") {
		t.Errorf("edit[0] = %+v", gh.Edits[0])
	}
	if gh.Edits[1].Number != 102 || !strings.Contains(gh.Edits[1].Body, "- #101\n") {
		t.Errorf("edit[1] = %+v", gh.Edits[1])
	}
	if !strings.Contains(out.String(), "created #101 https://github.com/o/r/issues/101") {
		t.Errorf("output = %q", out.String())
	}
}

func TestSay_SingleIssueHasNoCrossReferenceEdit(t *testing.T) {
	r, gh, _, _ := newRunner(t, `[{"title":"feat: one","body":"b"}]`)
	if _, err := r.Say(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if len(gh.Created) != 1 || len(gh.Edits) != 0 {
		t.Errorf("created=%d edits=%d", len(gh.Created), len(gh.Edits))
	}
}

func TestSay_DryRunMakesNoMutatingCalls(t *testing.T) {
	r, gh, ag, out := newRunner(t, twoDrafts)
	r.Opts.DryRun = true
	res, err := r.Say(context.Background(), "一言")
	if err != nil {
		t.Fatal(err)
	}
	if len(ag.Calls) != 1 {
		t.Errorf("agent should still run in dry-run, calls=%d", len(ag.Calls))
	}
	if len(gh.Created)+len(gh.Edits)+len(gh.Comments)+len(gh.AddedLabels)+len(gh.RemovedLabels)+len(gh.Closed) != 0 {
		t.Errorf("dry-run mutated: %+v", gh)
	}
	if len(res.Drafts) != 2 || len(res.Created) != 0 {
		t.Errorf("res = %+v", res)
	}
	s := out.String()
	if !strings.Contains(s, "[dry-run] would create issue 1/2") || !strings.Contains(s, "labels: needs-review") ||
		!strings.Contains(s, "feat(cli): add lead say for one-liner issues") {
		t.Errorf("dry-run output = %q", s)
	}
}

func TestSay_TitleLintRejectsBeforeCreate(t *testing.T) {
	r, gh, _, _ := newRunner(t, `[{"title":"fix: ends with period.","body":"b"}]`)
	_, err := r.Say(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "period") {
		t.Fatalf("err = %v", err)
	}
	if len(gh.Created) != 0 {
		t.Errorf("created despite lint failure: %+v", gh.Created)
	}
}

func TestSay_InvalidAgentOutputMentionsLog(t *testing.T) {
	r, gh, _, _ := newRunner(t, "sorry, no")
	_, err := r.Say(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "say-") {
		t.Fatalf("err = %v", err)
	}
	if len(gh.Created) != 0 {
		t.Error("created despite bad output")
	}
}

func TestSay_AgentFailurePropagates(t *testing.T) {
	r, _, ag, _ := newRunner(t, "")
	ag.Err = errors.New("exit status 1")
	if _, err := r.Say(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestSay_RejectsEmptyOneLinerAndConflictingFlags(t *testing.T) {
	r, _, ag, _ := newRunner(t, twoDrafts)
	if _, err := r.Say(context.Background(), "  "); err == nil {
		t.Error("empty one-liner accepted")
	}
	r.Opts.FollowUp, r.Opts.Redraft = 1, 2
	if _, err := r.Say(context.Background(), "x"); err == nil {
		t.Error("follow-up + redraft accepted")
	}
	if len(ag.Calls) != 0 {
		t.Error("agent ran despite invalid options")
	}
}

func TestSay_UnsupportedHeadlessAgent(t *testing.T) {
	r, _, ag, _ := newRunner(t, twoDrafts)
	r.Opts.Agent = "cursor"
	if _, err := r.Say(context.Background(), "x"); err == nil {
		t.Error("cursor accepted")
	}
	if len(ag.Calls) != 0 {
		t.Error("agent ran")
	}
}

func TestSay_ReadsAgentsMdFromWorkDir(t *testing.T) {
	r, _, ag, _ := newRunner(t, twoDrafts)
	r.Opts.Rules = ""
	if err := os.WriteFile(filepath.Join(r.Opts.WorkDir, "AGENTS.md"), []byte("# rules\nno grep tests"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Say(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ag.Calls[0].Argv[3], "no grep tests") {
		t.Error("AGENTS.md not inlined into prompt")
	}
	// Missing AGENTS.md → default rules.
	r2, _, ag2, _ := newRunner(t, twoDrafts)
	r2.Opts.Rules = ""
	if _, err := r2.Say(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ag2.Calls[0].Argv[3], DefaultRules) {
		t.Error("default rules not used")
	}
}

// --- Say: follow-up -------------------------------------------------------------

func TestSay_FollowUpFetchesParentAndReferencesIt(t *testing.T) {
	r, gh, ag, _ := newRunner(t, `[{"title":"fix(dispatch): handle missing --agent","body":"## Purpose\nno ref here"}]`)
	gh.Issues[42] = ports.Issue{Number: 42, Title: "feat: dispatch", Body: "ready を払い出す", State: "OPEN"}
	r.Opts.FollowUp = 42
	if _, err := r.Say(context.Background(), "実機で落ちた"); err != nil {
		t.Fatal(err)
	}
	if len(gh.ViewCalls) != 1 || gh.ViewCalls[0] != 42 {
		t.Errorf("view calls = %v", gh.ViewCalls)
	}
	prompt := ag.Calls[0].Argv[3]
	if !strings.Contains(prompt, "#42") || !strings.Contains(prompt, "ready を払い出す") {
		t.Error("prompt lacks parent issue")
	}
	if len(gh.Created) != 1 || !strings.Contains(gh.Created[0].Body, "#42") {
		t.Errorf("created body lacks #42: %+v", gh.Created)
	}
}

func TestSay_FollowUpKeepsAgentReference(t *testing.T) {
	r, gh, _, _ := newRunner(t, `[{"title":"fix: x","body":"元 Issue #42 で NG"}]`)
	gh.Issues[42] = ports.Issue{Number: 42, Title: "t"}
	r.Opts.FollowUp = 42
	if _, err := r.Say(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if gh.Created[0].Body != "元 Issue #42 で NG" {
		t.Errorf("body altered: %q", gh.Created[0].Body)
	}
}

func TestSay_FollowUpMissingParentFailsBeforeAgent(t *testing.T) {
	r, _, ag, _ := newRunner(t, twoDrafts)
	r.Opts.FollowUp = 999
	if _, err := r.Say(context.Background(), "x"); err == nil {
		t.Error("missing parent accepted")
	}
	if len(ag.Calls) != 0 {
		t.Error("agent ran without parent")
	}
}

// --- Say: redraft -----------------------------------------------------------------

func TestSay_RedraftEditsLabelsAndComments(t *testing.T) {
	r, gh, ag, _ := newRunner(t, `{"title":"feat: x (revised)","body":"## Purpose\ndo x\n- exit code is asserted"}`)
	gh.Issues[7] = ports.Issue{Number: 7, Title: "feat: x", Body: "## Purpose\ndo x", State: "OPEN"}
	gh.CommentsByNo = map[int][]ports.Comment{7: {{Author: "kuwa72", Body: "exit code も見たい"}}}
	r.Opts.Redraft = 7
	res, err := r.Say(context.Background(), "受入条件に exit code を足して")
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.CommentsCalls) != 1 || gh.CommentsCalls[0] != 7 {
		t.Errorf("comments calls = %v", gh.CommentsCalls)
	}
	prompt := ag.Calls[0].Argv[3]
	if !strings.Contains(prompt, "exit code も見たい") || !strings.Contains(prompt, "#7") {
		t.Error("prompt lacks issue/comments")
	}
	if len(gh.Edits) != 1 || gh.Edits[0].Number != 7 || gh.Edits[0].Title != "feat: x (revised)" ||
		gh.Edits[0].Body != "## Purpose\ndo x\n- exit code is asserted" {
		t.Errorf("edits = %+v", gh.Edits)
	}
	if len(gh.AddedLabels) != 1 || gh.AddedLabels[0] != (testutil.LabelCall{Number: 7, Label: "needs-review"}) {
		t.Errorf("labels = %+v", gh.AddedLabels)
	}
	if len(gh.Comments) != 1 || gh.Comments[0].Number != 7 {
		t.Fatalf("comments = %+v", gh.Comments)
	}
	c := gh.Comments[0].Body
	if !strings.Contains(c, "受入条件に exit code を足して") || !strings.Contains(c, "`feat: x` → `feat: x (revised)`") ||
		!strings.Contains(c, "+1 行 / -0 行") || !strings.Contains(c, "+ - exit code is asserted") {
		t.Errorf("summary = %q", c)
	}
	if len(gh.Created) != 0 || res.Redrafted != 7 {
		t.Errorf("created=%d redrafted=%d", len(gh.Created), res.Redrafted)
	}
}

func TestSay_RedraftDryRunMutatesNothing(t *testing.T) {
	r, gh, _, out := newRunner(t, `{"title":"feat: x","body":"new"}`)
	gh.Issues[7] = ports.Issue{Number: 7, Title: "feat: x", Body: "old"}
	r.Opts.Redraft, r.Opts.DryRun = 7, true
	if _, err := r.Say(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if len(gh.Edits)+len(gh.Comments)+len(gh.AddedLabels)+len(gh.Created) != 0 {
		t.Errorf("dry-run mutated: %+v", gh)
	}
	if !strings.Contains(out.String(), "[dry-run] would edit #7") || !strings.Contains(out.String(), "タイトル: 変更なし") {
		t.Errorf("output = %q", out.String())
	}
}

func TestChangeSummary_NoChange(t *testing.T) {
	s := ChangeSummary("x", ports.Issue{Title: "t", Body: "a\nb"}, Draft{Title: "t", Body: "b\na\n"})
	if !strings.Contains(s, "変更なし") || !strings.Contains(s, "+0 行 / -0 行") || strings.Contains(s, "<details>") {
		t.Errorf("summary = %q", s)
	}
}

// --- ExecRunner -------------------------------------------------------------------

func TestExecRunner_CapturesStdoutAndLogs(t *testing.T) {
	logPath := testutil.InstallDummy(t, "claude", `echo '[{"title":"t","body":"b"}]'; echo warn >&2`)
	agentLog := filepath.Join(t.TempDir(), "logs", "say.log")
	out, err := (&ExecRunner{}).Run(context.Background(), t.TempDir(), []string{"claude", "-p", "--dangerously-skip-permissions", "prompt"}, agentLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != `[{"title":"t","body":"b"}]` {
		t.Errorf("stdout = %q", out)
	}
	args := testutil.LogLines(t, logPath)
	want := []string{"<-p>", "<--dangerously-skip-permissions>", "<prompt>"}
	if len(args) != 3 || args[0] != want[0] || args[1] != want[1] || args[2] != want[2] {
		t.Errorf("argv = %q", args)
	}
	logged, err := os.ReadFile(agentLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), `"title":"t"`) || !strings.Contains(string(logged), "warn") {
		t.Errorf("log = %q", logged)
	}
}

func TestExecRunner_MissingBinaryAndFailure(t *testing.T) {
	testutil.EmptyBin(t)
	_, err := (&ExecRunner{}).Run(context.Background(), t.TempDir(), []string{"claude", "x"}, filepath.Join(t.TempDir(), "l.log"))
	if !ports.IsBinaryNotFound(err) {
		t.Errorf("err = %v", err)
	}
	testutil.InstallDummy(t, "claude", `exit 3`)
	_, err = (&ExecRunner{}).Run(context.Background(), t.TempDir(), []string{"claude", "x"}, filepath.Join(t.TempDir(), "l.log"))
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("err = %v", err)
	}
}
