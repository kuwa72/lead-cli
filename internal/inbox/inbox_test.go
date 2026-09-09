package inbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kuwa72/lead-cli/internal/adapters/herdr"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// --- sectioning (pure) -------------------------------------------------------

func TestBuild_SectionsFromLabelsAndState(t *testing.T) {
	needsReview := []ports.IssueSummary{{Number: 92, Title: "fix(order): warn on zero stock"}, {Number: 93, Title: "feat(inbox): ready transition"}}
	blocked := []ports.IssueSummary{{Number: 88, Title: "CI flaky"}, {Number: 92, Title: "dup: also labelled blocked"}}
	merged := []ports.MergedIssue{{Number: 60, Title: "done 60"}, {Number: 61, Title: "done 61"}}
	wfs := []state.Workflow{
		{Issue: 88, Status: state.StatusBlocked, Agent: "claude", Attempts: 3, LogPath: "/logs/issue-88.log", Branch: "issue/88-ci"},
		{Issue: 70, Status: state.StatusInProgress, Agent: "agy", PID: 4242, Branch: "issue/70-x"},
		{Issue: 50, Status: state.StatusPlanned, Branch: "issue/50-planned"},
	}

	got := Build(needsReview, blocked, merged, nil, wfs)
	if len(got) != 4 {
		t.Fatalf("Build returned %d sections, want 4", len(got))
	}
	kinds := []Kind{got[0].Kind, got[1].Kind, got[2].Kind, got[3].Kind}
	if want := []Kind{KindNeedsReview, KindBlocked, KindMerged, KindRunning}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("section order = %v, want %v", kinds, want)
	}
	if nums := numbers(got[0]); !reflect.DeepEqual(nums, []int{92, 93}) {
		t.Errorf("needs-review = %v, want [92 93]", nums)
	}
	if nums := numbers(got[1]); !reflect.DeepEqual(nums, []int{88}) {
		t.Errorf("blocked = %v, want [88] (92 must stay in needs-review only)", nums)
	}
	b := got[1].Items[0]
	if b.Agent != "claude" || b.Attempts != 3 || b.LogPath != "/logs/issue-88.log" {
		t.Errorf("blocked item not enriched from state: %+v", b)
	}
	if nums := numbers(got[2]); !reflect.DeepEqual(nums, []int{60, 61}) {
		t.Errorf("merged = %v, want [60 61]", nums)
	}
	if nums := numbers(got[3]); !reflect.DeepEqual(nums, []int{70}) {
		t.Errorf("running = %v, want [70]", nums)
	}
	if got[3].Items[0].Agent != "agy" || got[3].Items[0].Branch != "issue/70-x" {
		t.Errorf("running item lacks agent/branch: %+v", got[3].Items[0])
	}
	if got[0].Collapsed || got[1].Collapsed || !got[2].Collapsed || !got[3].Collapsed {
		t.Errorf("collapsed flags: %v %v %v %v, want false false true true", got[0].Collapsed, got[1].Collapsed, got[2].Collapsed, got[3].Collapsed)
	}
}

func numbers(s Section) []int {
	out := []int{}
	for _, it := range s.Items {
		out = append(out, it.Number)
	}
	return out
}

// --- key → gh calls (headless bubbletea Update) ------------------------------

// fakeShell records exec requests and runs OnExec instead of a real process.
type fakeShell struct {
	Calls  [][]string
	OnExec func(*exec.Cmd) error
}

func (f *fakeShell) Exec(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
	f.Calls = append(f.Calls, append([]string{}, c.Args...))
	return func() tea.Msg {
		var err error
		if f.OnExec != nil {
			err = f.OnExec(c)
		}
		return done(err)
	}
}

func newFixture(t *testing.T) (*testutil.FakeGhClient, *fakeShell, Model) {
	t.Helper()
	gh := &testutil.FakeGhClient{
		Labeled: map[string][]ports.IssueSummary{
			LabelNeedsReview: {{Number: 7, Title: "spec: add warning"}, {Number: 8, Title: "spec: second"}},
			LabelBlocked:     {{Number: 9, Title: "impl stuck"}},
		},
		Issues: map[int]ports.Issue{
			7: {Number: 7, Title: "spec: add warning", Body: "## Acceptance\n- warns on zero", State: "OPEN"},
			9: {Number: 9, Title: "impl stuck", Body: "blocked body", State: "OPEN"},
		},
	}
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	store := &state.Store{Path: stateFile}
	if err := store.Upsert(state.Workflow{Issue: 9, Status: state.StatusBlocked, Agent: "claude", Attempts: 3, LogPath: "/logs/issue-9.log", Branch: "issue/9-stuck", PullRequests: []state.PRRef{{Number: 11, Status: "open"}}}); err != nil {
		t.Fatal(err)
	}
	sh := &fakeShell{}
	m := New(Options{
		Gh:         gh,
		Store:      store,
		Shell:      sh,
		Editor:     "fake-editor",
		AgentsPath: filepath.Join(t.TempDir(), "AGENTS.md"),
		Repo:       "kuwa72/lead-cli",
	})
	m = drive(t, m, nil, m.Init())
	return gh, sh, m
}

func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		msg, err := ParseKey(k)
		if err != nil {
			t.Fatalf("ParseKey(%q): %v", k, err)
		}
		next, cmd := m.Update(msg)
		m = drive(t, next.(Model), nil, cmd)
	}
	return m
}

// drive runs cmds synchronously until the model settles (no real terminal).
func drive(t *testing.T, m Model, _ tea.Msg, cmd tea.Cmd) Model {
	t.Helper()
	next, _ := Drain(m, cmd)
	return next
}

func TestKeyA_ApprovesNeedsReviewIssue(t *testing.T) {
	gh, _, m := newFixture(t)
	m = press(t, m, "a")
	if want := []testutil.LabelCall{{Number: 7, Label: LabelNeedsReview}}; !reflect.DeepEqual(gh.RemovedLabels, want) {
		t.Errorf("RemovedLabels = %+v, want %+v", gh.RemovedLabels, want)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: LabelReady}}; !reflect.DeepEqual(gh.AddedLabels, want) {
		t.Errorf("AddedLabels = %+v, want %+v", gh.AddedLabels, want)
	}
	if len(gh.Closed) != 0 || len(gh.Comments) != 0 || len(gh.EditedBodies) != 0 {
		t.Errorf("approve must not close/comment/edit: %+v %+v %+v", gh.Closed, gh.Comments, gh.EditedBodies)
	}
	if !strings.Contains(m.View(), "#7") || !strings.Contains(m.View(), "ready") {
		t.Errorf("status line should report #7 → ready, got:\n%s", m.View())
	}
}

func TestKeyA_MovesCursorThenApprovesSecond(t *testing.T) {
	gh, _, m := newFixture(t)
	m = press(t, m, "j", "a")
	if want := []testutil.LabelCall{{Number: 8, Label: LabelReady}}; !reflect.DeepEqual(gh.AddedLabels, want) {
		t.Errorf("AddedLabels = %+v, want %+v", gh.AddedLabels, want)
	}
	_ = m
}

func TestKeyA_OnBlockedIssueDoesNothingToGitHub(t *testing.T) {
	gh, _, m := newFixture(t)
	m = press(t, m, "j", "j", "a") // 7 → 8 → 9 (blocked)
	if len(gh.AddedLabels)+len(gh.RemovedLabels) != 0 {
		t.Errorf("blocked issue must not be approved: added=%+v removed=%+v", gh.AddedLabels, gh.RemovedLabels)
	}
	if !strings.Contains(m.View(), "#9") {
		t.Errorf("view should mention #9 in status, got:\n%s", m.View())
	}
}

func TestKeyE_EditsBodyInEditorThenApproves(t *testing.T) {
	gh, sh, m := newFixture(t)
	sh.OnExec = func(c *exec.Cmd) error {
		path := c.Args[len(c.Args)-1]
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(path, append(raw, "\n- edited by human\n"...), 0o644)
	}
	m = press(t, m, "e")
	if len(sh.Calls) != 1 || sh.Calls[0][0] != "fake-editor" {
		t.Fatalf("editor exec = %v, want fake-editor <tmpfile>", sh.Calls)
	}
	wantBody := "## Acceptance\n- warns on zero\n- edited by human\n"
	if want := []testutil.BodyEdit{{Number: 7, Body: wantBody}}; !reflect.DeepEqual(gh.EditedBodies, want) {
		t.Errorf("EditedBodies = %+v, want %+v", gh.EditedBodies, want)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: LabelNeedsReview}}; !reflect.DeepEqual(gh.RemovedLabels, want) {
		t.Errorf("RemovedLabels = %+v, want %+v", gh.RemovedLabels, want)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: LabelReady}}; !reflect.DeepEqual(gh.AddedLabels, want) {
		t.Errorf("AddedLabels = %+v, want %+v", gh.AddedLabels, want)
	}
	_ = m
}

func TestKeyE_EditorFailureLeavesIssueUntouched(t *testing.T) {
	gh, sh, m := newFixture(t)
	sh.OnExec = func(*exec.Cmd) error { return errors.New("editor exited 1") }
	m = press(t, m, "e")
	if len(gh.EditedBodies)+len(gh.AddedLabels)+len(gh.RemovedLabels) != 0 {
		t.Errorf("failed editor must not touch GitHub: %+v %+v %+v", gh.EditedBodies, gh.AddedLabels, gh.RemovedLabels)
	}
	if !strings.Contains(m.View(), "editor exited 1") {
		t.Errorf("view should surface the editor error, got:\n%s", m.View())
	}
}

func TestKeyX_RejectWithReasonCommentsThenCloses(t *testing.T) {
	gh, _, m := newFixture(t)
	m = press(t, m, "x", "text:duplicate of #3", "enter")
	if want := []testutil.IssueComment{{Number: 7, Body: "duplicate of #3"}}; !reflect.DeepEqual(gh.Comments, want) {
		t.Errorf("Comments = %+v, want %+v", gh.Comments, want)
	}
	if !reflect.DeepEqual(gh.Closed, []int{7}) {
		t.Errorf("Closed = %v, want [7]", gh.Closed)
	}
	_ = m
}

func TestKeyX_EmptyReasonClosesWithoutComment(t *testing.T) {
	gh, _, m := newFixture(t)
	press(t, m, "x", "enter")
	if len(gh.Comments) != 0 || !reflect.DeepEqual(gh.Closed, []int{7}) {
		t.Errorf("Comments=%+v Closed=%v, want no comment and Closed=[7]", gh.Comments, gh.Closed)
	}
}

func TestKeyX_EscCancels(t *testing.T) {
	gh, _, m := newFixture(t)
	press(t, m, "x", "text:nope", "esc")
	if len(gh.Comments)+len(gh.Closed) != 0 {
		t.Errorf("esc must cancel: Comments=%+v Closed=%v", gh.Comments, gh.Closed)
	}
}

func TestKeyT_PostsOneLineComment(t *testing.T) {
	gh, _, m := newFixture(t)
	m = press(t, m, "j", "j", "t", "text:retry with -race", "enter") // on blocked #9
	if want := []testutil.IssueComment{{Number: 9, Body: "retry with -race"}}; !reflect.DeepEqual(gh.Comments, want) {
		t.Errorf("Comments = %+v, want %+v", gh.Comments, want)
	}
	if len(gh.Closed) != 0 {
		t.Errorf("comment must not close: %v", gh.Closed)
	}
	_ = m
}

func TestKeyT_BackspaceEditsInput(t *testing.T) {
	gh, _, m := newFixture(t)
	press(t, m, "t", "text:abcd", "backspace", "backspace", "text:x", "enter")
	if want := []testutil.IssueComment{{Number: 7, Body: "abx"}}; !reflect.DeepEqual(gh.Comments, want) {
		t.Errorf("Comments = %+v, want %+v", gh.Comments, want)
	}
}

func TestKeyO_OpensBrowser(t *testing.T) {
	gh, _, m := newFixture(t)
	press(t, m, "o")
	if !reflect.DeepEqual(gh.Browsed, []int{7}) {
		t.Errorf("Browsed = %v, want [7]", gh.Browsed)
	}
}

func TestKeyR_OpensAgentsMdInEditor(t *testing.T) {
	gh, sh, m := newFixture(t)
	press(t, m, "r")
	if want := [][]string{{"fake-editor", m.opts.AgentsPath}}; !reflect.DeepEqual(sh.Calls, want) {
		t.Errorf("exec calls = %v, want %v", sh.Calls, want)
	}
	if len(gh.AddedLabels)+len(gh.RemovedLabels)+len(gh.Comments)+len(gh.Closed) != 0 {
		t.Error("r must not touch GitHub")
	}
}

func TestEnter_ShowsBodyFullscreenAndEscReturns(t *testing.T) {
	gh, _, m := newFixture(t)
	m = press(t, m, "enter")
	if !reflect.DeepEqual(gh.ViewCalls, []int{7}) {
		t.Fatalf("ViewCalls = %v, want [7]", gh.ViewCalls)
	}
	v := m.View()
	if !strings.Contains(v, "warns on zero") || strings.Contains(v, "spec: second") {
		t.Errorf("detail view should show #7 body only, got:\n%s", v)
	}
	m = press(t, m, "esc")
	if v := m.View(); !strings.Contains(v, "spec: second") {
		t.Errorf("esc should return to the list, got:\n%s", v)
	}
}

func TestDetail_MergesPrBody(t *testing.T) {
	gh, _, m := newFixture(t)
	gh.PrBodies = map[int]string{11: "## Acceptance\n- [ ] task A\n- [x] task B\n"}
	m = press(t, m, "j", "j", "enter")
	if !reflect.DeepEqual(gh.ViewCalls, []int{9}) {
		t.Fatalf("ViewCalls = %v, want [9]", gh.ViewCalls)
	}
	if !reflect.DeepEqual(gh.PrBodyCalls, []int{11}) {
		t.Fatalf("PrBodyCalls = %v, want [11]", gh.PrBodyCalls)
	}
	v := m.View()
	if !strings.Contains(v, "blocked body") {
		t.Errorf("detail view missing issue body, got:\n%s", v)
	}
	if !strings.Contains(v, "## PR 本文") {
		t.Errorf("detail view missing PR header, got:\n%s", v)
	}
	if !strings.Contains(v, "- [ ] task A") || !strings.Contains(v, "- [x] task B") {
		t.Errorf("detail view missing PR checklist, got:\n%s", v)
	}
	if strings.Contains(v, "spec: second") {
		t.Errorf("detail view should not show other issue, got:\n%s", v)
	}
}

func TestBuild_EnrichesPaneFromState(t *testing.T) {
	wfs := []state.Workflow{
		{Issue: 88, Status: state.StatusBlocked, Pane: "w1:p9", LogPath: "/logs/88.log", Branch: "issue/88"},
	}
	blocked := []ports.IssueSummary{{Number: 88, Title: "stuck"}}
	got := Build(nil, blocked, nil, nil, wfs)
	if len(got[1].Items) != 1 {
		t.Fatalf("blocked section = %d items, want 1", len(got[1].Items))
	}
	it := got[1].Items[0]
	if it.Pane != "w1:p9" || it.LogPath != "/logs/88.log" {
		t.Errorf("item not enriched: pane=%q log=%q", it.Pane, it.LogPath)
	}
}

func TestKeyP_OnlyRunningOrBlockedAgents(t *testing.T) {
	_, _, m := newFixture(t)
	fh := &testutil.FakeHerdrRunner{}
	m.opts.Herdr = fh
	m = press(t, m, "p")
	if len(fh.PeekCalls) != 0 {
		t.Errorf("p on needs-review must not call Herdr: %+v", fh.PeekCalls)
	}
	if !strings.Contains(m.View(), "実行中/止まってる") && !strings.Contains(m.View(), "エージェント") {
		t.Errorf("p should explain it is only for running/blocked agents, got:\n%s", m.View())
	}
}

func TestKeyP_NoLogOrPaneShowsStatus(t *testing.T) {
	_, _, m := newFixture(t)
	store := m.opts.Store
	if err := store.Upsert(state.Workflow{Issue: 9, Status: state.StatusBlocked, Branch: "issue/9-stuck"}); err != nil {
		t.Fatal(err)
	}
	// Reload so the cleared log/pane is reflected.
	m, _ = Drain(m, m.loadCmd())
	m.opts.Herdr = &testutil.FakeHerdrRunner{}
	m = press(t, m, "j", "j", "p")
	if !strings.Contains(m.View(), "ログもペインもありません") {
		t.Errorf("p without log/pane should show a status message, got:\n%s", m.View())
	}
}

func TestKeyP_HerdrOpensLogInNewTab(t *testing.T) {
	logPath := testutil.InstallDummy(t, "herdr",
		`if [ "$1 $2" = "tab create" ]; then printf '{"result":{"root_pane":{"pane_id":"p-new"}}}';`+
		`elif [ "$1 $2" = "pane run" ]; then :;`+
		`elif [ "$1 $2" = "pane move" ]; then :;`+
		`else echo "unexpected: $@" >&2; exit 3; fi`)

	_, _, m := newFixture(t)
	m.opts.Herdr = herdr.New()
	m = press(t, m, "j", "j", "p")

	logText := testutil.LogText(t, logPath)
	for _, want := range []string{"<tab>", "<create>", "<--focus>"} {
		if !strings.Contains(logText, want) {
			t.Errorf("herdr tab create args missing %q, got:\n%s", want, logText)
		}
	}
	for _, want := range []string{"<pane>", "<run>", "<p-new>", "<tail>", "<-f>", "</logs/issue-9.log>"} {
		if !strings.Contains(logText, want) {
			t.Errorf("herdr pane run args missing %q, got:\n%s", want, logText)
		}
	}
	if !strings.Contains(m.View(), "herdr タブ") {
		t.Errorf("status should report herdr tab, got:\n%s", m.View())
	}
}

func TestKeyP_HerdrAttachesPaneForRunningAgent(t *testing.T) {
	_, _, m := newFixture(t)
	store := m.opts.Store
	if err := store.Upsert(state.Workflow{Issue: 70, Status: state.StatusInProgress, Pane: "w1:p70", Branch: "issue/70-x"}); err != nil {
		t.Fatal(err)
	}
	m, _ = Drain(m, m.loadCmd())
	fh := &testutil.FakeHerdrRunner{}
	m.opts.Herdr = fh
	m.expanded = true
	m = press(t, m, "j", "j", "j", "p")
	if len(fh.PeekCalls) != 1 || fh.PeekCalls[0].Pane != "w1:p70" || fh.PeekCalls[0].LogPath != "" {
		t.Errorf("Peek calls = %+v, want Pane=w1:p70", fh.PeekCalls)
	}
	if !strings.Contains(m.View(), "herdr タブ") {
		t.Errorf("status should report herdr tab, got:\n%s", m.View())
	}
}

func TestKeyP_FallsBackToPagerWhenHerdrUnavailable(t *testing.T) {
	t.Setenv("PAGER", "")
	lessLog := testutil.InstallDummy(t, "less", "")

	_, sh, m := newFixture(t)
	m.opts.Herdr = nil
	sh.OnExec = func(c *exec.Cmd) error { return c.Run() }
	m = press(t, m, "j", "j", "p")

	if want := [][]string{{"less", "/logs/issue-9.log"}}; !reflect.DeepEqual(sh.Calls, want) {
		t.Errorf("shell calls = %v, want %v", sh.Calls, want)
	}
	logText := testutil.LogText(t, lessLog)
	if !strings.Contains(logText, "</logs/issue-9.log>") {
		t.Errorf("less did not receive log path, got:\n%s", logText)
	}
	if !strings.Contains(m.View(), "エージェントログを開きました") {
		t.Errorf("status should report log opened, got:\n%s", m.View())
	}
}

func TestKeyP_UsesPAGERWhenSet(t *testing.T) {
	t.Setenv("PAGER", "fake-pager --flag")
	pagerLog := testutil.InstallDummy(t, "fake-pager", "")

	_, sh, m := newFixture(t)
	m.opts.Herdr = nil
	sh.OnExec = func(c *exec.Cmd) error { return c.Run() }
	m = press(t, m, "j", "j", "p")

	if want := [][]string{{"fake-pager", "--flag", "/logs/issue-9.log"}}; !reflect.DeepEqual(sh.Calls, want) {
		t.Errorf("shell calls = %v, want %v", sh.Calls, want)
	}
	logText := testutil.LogText(t, pagerLog)
	if !strings.Contains(logText, "<--flag>") || !strings.Contains(logText, "</logs/issue-9.log>") {
		t.Errorf("$PAGER did not receive argv, got:\n%s", logText)
	}
}

// sayCall records one Options.Say invocation.
type sayCall struct {
	OneLiner string
	FollowUp int
}

func fakeSay(calls *[]sayCall) func(context.Context, string, int) (string, error) {
	return func(_ context.Context, oneLiner string, followUp int) (string, error) {
		*calls = append(*calls, sayCall{OneLiner: oneLiner, FollowUp: followUp})
		return "#101 を起票（needs-review）", nil
	}
}

func TestKeysNS_WithoutSayAreNoOps(t *testing.T) {
	gh, sh, m := newFixture(t)
	press(t, m, "n", "s")
	if len(gh.Comments)+len(gh.Closed)+len(gh.AddedLabels)+len(sh.Calls) != 0 {
		t.Errorf("n/s without Say must be no-ops: %+v %v %+v %v", gh.Comments, gh.Closed, gh.AddedLabels, sh.Calls)
	}
}

func TestKeyS_FilesNeedsReviewIssueViaSay(t *testing.T) {
	gh, _, m := newFixture(t)
	var calls []sayCall
	m.opts.Say = fakeSay(&calls)
	m = press(t, m, "s", "text:warn when stock hits zero", "enter")
	if want := []sayCall{{OneLiner: "warn when stock hits zero", FollowUp: 0}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("Say calls = %+v, want %+v", calls, want)
	}
	if !strings.Contains(m.View(), "#101") {
		t.Errorf("status should report the created issue, got:\n%s", m.View())
	}
	if len(gh.Created) != 0 {
		t.Errorf("inbox must not create issues itself (Say owns that): %+v", gh.Created)
	}
}

func TestKeyS_EmptyOneLinerDoesNotCallSay(t *testing.T) {
	_, _, m := newFixture(t)
	var calls []sayCall
	m.opts.Say = fakeSay(&calls)
	press(t, m, "s", "enter")
	if len(calls) != 0 {
		t.Errorf("empty one-liner must not call Say: %+v", calls)
	}
}

func TestKeyS_EscCancels(t *testing.T) {
	_, _, m := newFixture(t)
	var calls []sayCall
	m.opts.Say = fakeSay(&calls)
	m = press(t, m, "s", "text:x", "esc")
	if len(calls) != 0 {
		t.Errorf("esc must cancel: %+v", calls)
	}
	if v := m.View(); !strings.Contains(v, "取り消しました") {
		t.Errorf("esc should show cancel status, got:\n%s", v)
	}
}

func TestKeyN_FilesFollowUpReferencingSelectedIssue(t *testing.T) {
	_, _, m := newFixture(t)
	var calls []sayCall
	m.opts.Say = fakeSay(&calls)
	press(t, m, "j", "j", "n", "text:still broken on main", "enter") // on blocked #9
	if want := []sayCall{{OneLiner: "still broken on main", FollowUp: 9}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("Say calls = %+v, want %+v (n must pass the selected issue as followUp)", calls, want)
	}
}

func TestKeyN_SayErrorIsShownNotFatal(t *testing.T) {
	_, _, m := newFixture(t)
	m.opts.Say = func(context.Context, string, int) (string, error) {
		return "", errors.New("spec: agent exploded")
	}
	m = press(t, m, "n", "text:ng", "enter")
	if v := m.View(); !strings.Contains(v, "agent exploded") {
		t.Errorf("say error should surface in the status line, got:\n%s", v)
	}
}

func TestKeyQ_Quits(t *testing.T) {
	_, _, m := newFixture(t)
	msg, _ := ParseKey("q")
	_, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("q returned nil cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q cmd produced %T, want tea.QuitMsg", cmd())
	}
}

func TestView_ListsSectionsInOrderWithCounts(t *testing.T) {
	_, _, m := newFixture(t)
	v := m.View()
	for _, want := range []string{"kuwa72/lead-cli", "レビュー待ち (2)", "止まってる (1)", "最近マージ (0)", "実行中 (0)", "#7", "#8", "#9", "claude", "a 承認"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if i, j := strings.Index(v, "#7"), strings.Index(v, "#9"); i > j {
		t.Errorf("needs-review must render above blocked:\n%s", v)
	}
}

func TestListError_IsShownNotFatal(t *testing.T) {
	gh := &testutil.FakeGhClient{LabelListErr: errors.New("gh: not logged in")}
	m := New(Options{Gh: gh, Shell: &fakeShell{}})
	m, _ = Drain(m, m.Init())
	if !strings.Contains(m.View(), "gh: not logged in") {
		t.Errorf("list error should be visible, got:\n%s", m.View())
	}
}

// --- headless driver -----------------------------------------------------------

func TestParseKey(t *testing.T) {
	cases := map[string]tea.KeyMsg{
		"a":         {Type: tea.KeyRunes, Runes: []rune{'a'}},
		"enter":     {Type: tea.KeyEnter},
		"esc":       {Type: tea.KeyEsc},
		"down":      {Type: tea.KeyDown},
		"up":        {Type: tea.KeyUp},
		"backspace": {Type: tea.KeyBackspace},
		"ctrl+c":    {Type: tea.KeyCtrlC},
	}
	for in, want := range cases {
		got, err := ParseKey(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseKey(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if _, err := ParseKey("no-such-key"); err == nil {
		t.Error("ParseKey(no-such-key) should fail")
	}
}

func TestRunHeadless_FeedsKeysWithoutTerminal(t *testing.T) {
	gh, _, m := newFixture(t)
	var out strings.Builder
	if err := RunHeadless(m, []string{"a", "q"}, &out); err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if want := []testutil.LabelCall{{Number: 7, Label: LabelReady}}; !reflect.DeepEqual(gh.AddedLabels, want) {
		t.Errorf("AddedLabels = %+v, want %+v", gh.AddedLabels, want)
	}
	if !strings.Contains(out.String(), "#7") {
		t.Errorf("headless output should include the final screen, got:\n%s", out.String())
	}
}

func TestRunHeadless_RejectsUnknownKey(t *testing.T) {
	_, _, m := newFixture(t)
	if err := RunHeadless(m, []string{"bogus-key"}, &strings.Builder{}); err == nil {
		t.Error("unknown key token should error")
	}
}

func TestRepoSlug(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:kuwa72/lead-cli.git":     "kuwa72/lead-cli",
		"https://github.com/kuwa72/lead-cli.git": "kuwa72/lead-cli",
		"https://github.com/kuwa72/lead-cli":     "kuwa72/lead-cli",
		"":                                       "",
	} {
		if got := RepoSlug(in); got != want {
			t.Errorf("RepoSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRelAge(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	if got := relAge(now.Add(-3*time.Minute), now); got != "3分前" {
		t.Errorf("relAge = %q, want 3分前", got)
	}
	if got := relAge(time.Time{}, now); got != "" {
		t.Errorf("relAge(zero) = %q, want empty", got)
	}
}

// newMergedFixture creates a model with a visible recently-merged issue #42.
func newMergedFixture(t *testing.T) (*testutil.FakeGhClient, *fakeShell, *SeenStore, Model) {
	t.Helper()
	gh := &testutil.FakeGhClient{
		Labeled: map[string][]ports.IssueSummary{
			LabelNeedsReview: {{Number: 7, Title: "spec: add warning"}},
			LabelBlocked:     {},
		},
		Merged: []ports.MergedIssue{{Number: 42, Title: "feat: done", MergedAt: time.Now().UTC().Add(-5 * time.Minute)}},
	}
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	store := &state.Store{Path: stateFile}
	seenFile := filepath.Join(t.TempDir(), "inbox-seen.json")
	seen := &SeenStore{Path: seenFile}
	sh := &fakeShell{}
	m := New(Options{
		Gh:         gh,
		Store:      store,
		Seen:       seen,
		Shell:      sh,
		Editor:     "fake-editor",
		AgentsPath: filepath.Join(t.TempDir(), "AGENTS.md"),
		Repo:       "kuwa72/lead-cli",
	})
	m = drive(t, m, nil, m.Init())
	return gh, sh, seen, m
}

func TestKeyC_ConfirmsMergedAndRefreshes(t *testing.T) {
	_, _, seen, m := newMergedFixture(t)
	m.expanded = true
	m = press(t, m, "j", "c")

	st, err := seen.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !st.IsConfirmed(42) {
		t.Errorf("confirmed = %v, want 42", st.Confirmed)
	}
	v := m.View()
	if !strings.Contains(v, "最近マージ (0)") {
		t.Errorf("merged section should be empty after confirm, got:\n%s", v)
	}
	if !strings.Contains(v, "#42 を確認しました") {
		t.Errorf("status should report #42 confirmed, got:\n%s", v)
	}
}

func TestKeyC_OnNonMergedDoesNothing(t *testing.T) {
	_, _, seen, m := newMergedFixture(t)
	press(t, m, "c")
	st, _ := seen.Load()
	if len(st.Confirmed) != 0 {
		t.Errorf("c on non-merged must not confirm: %+v", st.Confirmed)
	}
}

func TestKeyN_OnMergedConfirmsAndFollowsUp(t *testing.T) {
	_, _, seen, m := newMergedFixture(t)
	m.expanded = true
	var calls []sayCall
	m.opts.Say = fakeSay(&calls)
	m = press(t, m, "j", "n", "text:still broken", "enter")

	if want := []sayCall{{OneLiner: "still broken", FollowUp: 42}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("Say calls = %+v, want %+v", calls, want)
	}
	st, _ := seen.Load()
	if !st.IsConfirmed(42) {
		t.Errorf("n did not confirm merged issue: %+v", st.Confirmed)
	}
	if v := m.View(); !strings.Contains(v, "最近マージ (0)") {
		t.Errorf("merged section should be empty after n follow-up, got:\n%s", v)
	}
}

func TestLoadCmd_UpdatesLastSeenAt(t *testing.T) {
	gh, _, seen, _ := newMergedFixture(t)
	if gh.MergedCalls != 1 {
		t.Fatalf("ListMergedSince calls = %d, want 1", gh.MergedCalls)
	}
	st, err := seen.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.LastSeenAt.IsZero() {
		t.Error("last_seen_at not updated on inbox open")
	}
}
