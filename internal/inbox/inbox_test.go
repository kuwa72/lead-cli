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
	"github.com/muesli/termenv"

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
		Gh:           gh,
		Store:        store,
		Shell:        sh,
		Editor:       "fake-editor",
		AgentsPath:   filepath.Join(t.TempDir(), "AGENTS.md"),
		Repo:         "kuwa72/lead-cli",
		PreviewDelay: 0,
		Headless:     true,
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
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	m = press(t, m, "j", "a")
	if want := []testutil.LabelCall{{Number: 8, Label: LabelReady}}; !reflect.DeepEqual(gh.AddedLabels, want) {
		t.Errorf("AddedLabels = %+v, want %+v", gh.AddedLabels, want)
	}
	_ = m
}

func TestKeyA_OnBlockedIssueDoesNothingToGitHub(t *testing.T) {
	gh, _, m := newFixture(t)
	m = press(t, m, "j", "j", "j", "a") // 7 → 8 → header → 9 (blocked)
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
	m = press(t, m, "j", "j", "j", "t", "text:retry with -race", "enter") // on blocked #9
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
	m.cursor = 4 // blocked #9
	m = press(t, m, "enter")
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
	if !strings.Contains(v, "## PR body") {
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
	if !strings.Contains(m.View(), "running or blocked") && !strings.Contains(m.View(), "agent") {
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
	m = press(t, m, "j", "j", "j", "p")
	if !strings.Contains(m.View(), "has no agent log or pane") {
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
	m = press(t, m, "j", "j", "j", "p")

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
	if !strings.Contains(m.View(), "Opened #9 agent log in herdr tab") {
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
	m.cursor = 7 // running #70
	m = press(t, m, "p")
	if len(fh.PeekCalls) != 1 || fh.PeekCalls[0].Pane != "w1:p70" || fh.PeekCalls[0].LogPath != "" {
		t.Errorf("Peek calls = %+v, want Pane=w1:p70", fh.PeekCalls)
	}
	if !strings.Contains(m.View(), "Opened #70 agent pane in herdr tab") {
		t.Errorf("status should report herdr tab, got:\n%s", m.View())
	}
}

func TestKeyP_FallsBackToPagerWhenHerdrUnavailable(t *testing.T) {
	t.Setenv("PAGER", "")
	lessLog := testutil.InstallDummy(t, "less", "")

	_, sh, m := newFixture(t)
	m.opts.Herdr = nil
	sh.OnExec = func(c *exec.Cmd) error { return c.Run() }
	m = press(t, m, "j", "j", "j", "p")

	if want := [][]string{{"less", "/logs/issue-9.log"}}; !reflect.DeepEqual(sh.Calls, want) {
		t.Errorf("shell calls = %v, want %v", sh.Calls, want)
	}
	logText := testutil.LogText(t, lessLog)
	if !strings.Contains(logText, "</logs/issue-9.log>") {
		t.Errorf("less did not receive log path, got:\n%s", logText)
	}
	if !strings.Contains(m.View(), "Opened #9 agent log") {
		t.Errorf("status should report log opened, got:\n%s", m.View())
	}
}

func TestKeyP_UsesPAGERWhenSet(t *testing.T) {
	t.Setenv("PAGER", "fake-pager --flag")
	pagerLog := testutil.InstallDummy(t, "fake-pager", "")

	_, sh, m := newFixture(t)
	m.opts.Herdr = nil
	sh.OnExec = func(c *exec.Cmd) error { return c.Run() }
	m = press(t, m, "j", "j", "j", "p")

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
		return "Filed #101 as needs-review", nil
	}
}

func TestKeyP_PaneNotFoundShowsStatus(t *testing.T) {
	_, _, m := newFixture(t)
	store := m.opts.Store
	if err := store.Upsert(state.Workflow{Issue: 9, Status: state.StatusBlocked, Pane: "wQ:pP", LogPath: "", Branch: "issue/9-stuck"}); err != nil {
		t.Fatal(err)
	}
	m, _ = Drain(m, m.loadCmd())
	m.opts.Herdr = &testutil.FakeHerdrRunner{PeekErr: &ports.PaneNotFoundError{Pane: "wQ:pP"}}
	m = press(t, m, "j", "j", "j", "p") // blocked #9 with pane only
	if v := m.View(); !strings.Contains(v, "agent pane not found") {
		t.Errorf("pane not found should show a friendly status, got:\n%s", v)
	}
}

func TestKeysNS_WithoutSayAreNoOps(t *testing.T) {
	gh, sh, m := newFixture(t)
	press(t, m, "n", "s")
	if len(gh.Comments)+len(gh.Closed)+len(gh.AddedLabels)+len(sh.Calls) != 0 {
		t.Errorf("n/s without Say must be no-ops: %+v %v %+v %v", gh.Comments, gh.Closed, gh.AddedLabels, sh.Calls)
	}
}

func TestKeyS_DirectCreate(t *testing.T) {
	gh, _, m := newFixture(t)
	m = press(t, m, "s", "text:warn when stock hits zero", "enter")
	if len(gh.Created) != 1 {
		t.Fatalf("expected 1 created issue, got: %+v", gh.Created)
	}
	if gh.Created[0].Title != "warn when stock hits zero" {
		t.Errorf("title = %q, want %q", gh.Created[0].Title, "warn when stock hits zero")
	}
	if !strings.Contains(m.View(), "#101") {
		t.Errorf("status should report the created issue, got:\n%s", m.View())
	}
}

func TestKeyS_FilesNeedsReviewIssueViaSay(t *testing.T) {
	gh, _, m := newFixture(t)
	var calls []sayCall
	m.opts.Say = fakeSay(&calls)
	m = press(t, m, "s", "text:!warn when stock hits zero", "enter")
	if want := []sayCall{{OneLiner: "warn when stock hits zero", FollowUp: 0}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("Say calls = %+v, want %+v", calls, want)
	}
	if !strings.Contains(m.View(), "#101") {
		t.Errorf("status should report the created issue, got:\n%s", m.View())
	}
	if len(gh.Created) != 0 {
		t.Errorf("inbox must not create issues itself when using ! prefix: %+v", gh.Created)
	}
}

func TestKeyS_ImmediateFeedback(t *testing.T) {
	_, _, m := newFixture(t)
	m = press(t, m, "s", "text:urgent bug")
	// Send Enter without draining the resulting async command.
	msg, _ := ParseKey("enter")
	next, _ := m.Update(msg)
	m = next.(Model)
	if !m.loading {
		t.Errorf("expected loading=true immediately after Enter")
	}
	if !strings.Contains(m.status, "urgent bug") {
		t.Errorf("expected status to mention 'urgent bug', got: %q", m.status)
	}
}

func TestKeyS_WorksWithoutSay(t *testing.T) {
	gh, _, m := newFixture(t)
	m.opts.Say = nil
	m = press(t, m, "s", "text:direct issue without say", "enter")
	if len(gh.Created) != 1 {
		t.Fatalf("expected direct issue create when Say is nil, got: %+v", gh.Created)
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
	if v := m.View(); !strings.Contains(v, "Canceled") {
		t.Errorf("esc should show cancel status, got:\n%s", v)
	}
}

func TestKeyN_FilesFollowUpReferencingSelectedIssue(t *testing.T) {
	_, _, m := newFixture(t)
	var calls []sayCall
	m.opts.Say = fakeSay(&calls)
	press(t, m, "j", "j", "j", "n", "text:still broken on main", "enter") // on blocked #9
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

func TestFooter_ContextDependentActions(t *testing.T) {
	want := map[Kind]string{
		KindNeedsReview: "[Enter] Open   [a] Approve   [t] Reply   [m] Mode: batch   [g] Agent: agy   [?] Help   [q] Quit",
		KindBlocked:     "[Enter] Open   [t] Reply   [p] Peek   [m] Mode: batch   [g] Agent: agy   [?] Help   [q] Quit",
		KindMerged:      "[Enter] Open   [n] Report bug   [c] Mark seen   [m] Mode: batch   [g] Agent: agy   [?] Help   [q] Quit",
		KindRunning:     "[Enter] Open   [p] Peek   [o] Browser   [m] Mode: batch   [g] Agent: agy   [?] Help   [q] Quit",
	}
	for kind, expected := range want {
		m := Model{sections: []Section{{Kind: kind, Items: []Item{{Number: 1, Kind: kind}}}}, width: 120, expanded: true}
		m.cursor = 1
		if got := m.footer(); got != expected {
			t.Errorf("footer(%v) = %q, want %q", kind, got, expected)
		}
	}
	empty := Model{sections: []Section{{Kind: KindNeedsReview}}, width: 120}
	if got, want := empty.footer(), "[s] New   [m] Mode: batch   [g] Agent: agy   [?] Help   [q] Quit"; got != want {
		t.Errorf("empty footer = %q, want %q", got, want)
	}
}

func TestFooter_WrapsToTerminalWidth(t *testing.T) {
	m := Model{sections: []Section{{Kind: KindMerged, Items: []Item{{Number: 1, Kind: KindMerged}}}}, width: 35, expanded: true}
	m.cursor = 1
	got := m.footer()
	if strings.Count(got, "\n") == 0 {
		t.Fatalf("footer did not wrap at width: %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 35 {
			t.Errorf("footer line exceeds width: %q", line)
		}
	}
}

func TestFirstLaunchHelpIsDismissedAndPersisted(t *testing.T) {
	seen := &SeenStore{Path: filepath.Join(t.TempDir(), "inbox-seen.json")}
	m := New(Options{Seen: seen, Shell: &fakeShell{}, Headless: true})
	if !strings.Contains(m.View(), "Inbox actions") {
		t.Fatalf("first launch did not start in help: %s", m.View())
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(next.(Model).View(), "Inbox actions") {
		t.Fatal("Esc did not leave help")
	}
	if shown, err := seen.HelpShown(); err != nil || !shown {
		t.Fatalf("help dismissal not persisted: %v, %v", shown, err)
	}
	second := New(Options{Seen: seen, Shell: &fakeShell{}, Headless: true})
	if strings.Contains(second.View(), "Inbox actions") {
		t.Fatal("second launch unexpectedly started in help")
	}
}

func TestView_ListsSectionsInOrderWithCounts(t *testing.T) {
	_, _, m := newFixture(t)
	v := m.View()
	for _, want := range []string{"kuwa72/lead-cli", "Needs review (2)", "Blocked (1)", "Merged (0)", "Running (0)", "#7", "#8", "#9", "claude", "[a] Approve"} {
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
	m := New(Options{Gh: gh, Shell: &fakeShell{}, Headless: true})
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
		"pgup":      {Type: tea.KeyPgUp},
		"pgdown":    {Type: tea.KeyPgDown},
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
	if got := relAge(now.Add(-3*time.Minute), now); got != "3m" {
		t.Errorf("relAge = %q, want 3m", got)
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
	if err := seen.MarkHelpShown(); err != nil {
		t.Fatal(err)
	}
	sh := &fakeShell{}
	m := New(Options{
		Gh:           gh,
		Store:        store,
		Seen:         seen,
		Shell:        sh,
		Editor:       "fake-editor",
		AgentsPath:   filepath.Join(t.TempDir(), "AGENTS.md"),
		Repo:         "kuwa72/lead-cli",
		PreviewDelay: 0,
		Headless:     true,
	})
	m = drive(t, m, nil, m.Init())
	return gh, sh, seen, m
}

func TestKeyC_ConfirmsMergedAndRefreshes(t *testing.T) {
	_, _, seen, m := newMergedFixture(t)
	m.expanded = true
	m.cursor = 4 // merged #42
	m = press(t, m, "c")

	st, err := seen.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !st.IsConfirmed(42) {
		t.Errorf("confirmed = %v, want 42", st.Confirmed)
	}
	v := m.View()
	if !strings.Contains(v, "Merged (0)") {
		t.Errorf("merged section should be empty after confirm, got:\n%s", v)
	}
	if !strings.Contains(v, "Marked #42 as seen") {
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
	m.cursor = 4 // merged #42
	var calls []sayCall
	m.opts.Say = fakeSay(&calls)
	m = press(t, m, "n", "text:still broken", "enter")

	if want := []sayCall{{OneLiner: "still broken", FollowUp: 42}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("Say calls = %+v, want %+v", calls, want)
	}
	st, _ := seen.Load()
	if !st.IsConfirmed(42) {
		t.Errorf("n did not confirm merged issue: %+v", st.Confirmed)
	}
	if v := m.View(); !strings.Contains(v, "Merged (0)") {
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

func TestReload_PreservesSectionCollapsed(t *testing.T) {
	_, _, m := newFixture(t)
	m.sections[0].Collapsed = true
	m = press(t, m, "R")
	if !m.sections[0].Collapsed {
		t.Fatalf("reload did not preserve collapsed section: %v", m.sections[0].Collapsed)
	}
	if m.loading {
		t.Error("reload should finish loading")
	}
	v := m.View()
	if strings.Contains(v, "#7") {
		t.Errorf("collapsed section should still be hidden after reload, got:\n%s", v)
	}
}

func TestHeader_EnterTogglesSection(t *testing.T) {
	_, _, m := newFixture(t)
	m.cursor = 0 // Needs review header
	m = press(t, m, "enter")
	if !m.sections[0].Collapsed {
		t.Fatalf("enter on header did not collapse section")
	}
	v := m.View()
	if strings.Contains(v, "#7") || strings.Contains(v, "#8") {
		t.Errorf("collapsed section should not show its issues, got:\n%s", v)
	}
	if !strings.Contains(v, "▸ Needs review (2)") {
		t.Errorf("collapsed header should show closed marker, got:\n%s", v)
	}
}

func TestHeader_ZStillExpandsAll(t *testing.T) {
	_, _, m := newFixture(t)
	m.cursor = 0
	m = press(t, m, "enter") // collapse review
	if !m.sections[0].Collapsed {
		t.Fatalf("enter did not collapse section")
	}
	m = press(t, m, "z")
	v := m.View()
	if !strings.Contains(v, "#7") {
		t.Errorf("z should expand all sections, got:\n%s", v)
	}
}

func TestHeader_LAndRightToggle(t *testing.T) {
	_, _, m := newFixture(t)
	m.cursor = 0
	m = press(t, m, "l")
	if !m.sections[0].Collapsed {
		t.Fatalf("l on header did not collapse section")
	}
	m = press(t, m, "right")
	if m.sections[0].Collapsed {
		t.Fatalf("right on header did not expand section")
	}
}

func TestHeader_JKMoveThroughHeaders(t *testing.T) {
	_, _, m := newFixture(t)
	m = press(t, m, "k") // from #7 to review header
	row, ok := m.current()
	if !ok || !row.IsHeader() || row.Section.Kind != KindNeedsReview {
		t.Fatalf("k should move cursor to review header, got %+v", row)
	}
	m = press(t, m, "j") // back to #7
	row, ok = m.current()
	if !ok || row.IsHeader() || row.Item.Number != 7 {
		t.Fatalf("j should move back to issue #7, got %+v", row)
	}
}

func TestIssueKeysOnHeaderDoNothing(t *testing.T) {
	gh, _, m := newFixture(t)
	m.cursor = 0 // review header
	m = press(t, m, "a")
	if len(gh.AddedLabels)+len(gh.RemovedLabels)+len(gh.Comments)+len(gh.Closed) != 0 {
		t.Errorf("issue keys on header must not touch GitHub")
	}
	if !strings.Contains(m.View(), "Section header selected") {
		t.Errorf("status should explain header is selected, got:\n%s", m.View())
	}
}

func TestCursorMove_UpdatesDetailPreview(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	m = press(t, m, "j") // #7 -> #8
	v := m.View()
	if !strings.Contains(v, "spec: second") {
		t.Errorf("preview should show #8 title, got:\n%s", v)
	}
	if !strings.Contains(v, "spec: add warning") {
		t.Errorf("list should still show #7, got:\n%s", v)
	}
	if strings.Contains(v, "warns on zero") {
		t.Errorf("preview should not show #7 body, got:\n%s", v)
	}
	if !strings.Contains(v, "second body") {
		t.Errorf("preview should show #8 body, got:\n%s", v)
	}
	m = press(t, m, "j", "j") // #8 -> header -> #9
	v = m.View()
	if !strings.Contains(v, "blocked body") {
		t.Errorf("preview should show #9 body after moving, got:\n%s", v)
	}
	if !strings.Contains(v, "spec: second") {
		t.Errorf("list should still show #8 title, got:\n%s", v)
	}
}

func TestEnter_OpensFullscreenDetailFromSplit(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	m = press(t, m, "j") // load #8 preview
	v := m.View()
	if !strings.Contains(v, "spec: second") {
		t.Fatalf("preview not shown before enter, got:\n%s", v)
	}
	m = press(t, m, "enter")
	if m.mode != modeDetail {
		t.Fatalf("enter did not switch to fullscreen detail: mode=%v", m.mode)
	}
	if !strings.Contains(m.View(), "spec: second") || strings.Contains(m.View(), "spec: add warning") {
		t.Errorf("fullscreen detail should show only #8, got:\n%s", m.View())
	}
}

func TestDetail_PgUpPgDnAndArrowsScroll(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[7] = ports.Issue{Number: 7, Title: "spec: add warning", Body: strings.Repeat("line\n", 50), State: "OPEN"}
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "body", State: "OPEN"}
	m = press(t, m, "j", "enter")
	if m.mode != modeDetail {
		t.Fatalf("enter did not switch to detail: mode=%v", m.mode)
	}
	m = press(t, m, "pgdown")
	if m.detailOffset == 0 {
		t.Errorf("pgdown should scroll down, got offset %d", m.detailOffset)
	}
	before := m.detailOffset
	m = press(t, m, "down")
	if m.detailOffset != before+1 {
		t.Errorf("down should increment offset by 1, got %d", m.detailOffset)
	}
	m = press(t, m, "pgup")
	if m.detailOffset >= before || m.detailOffset < 0 {
		t.Errorf("pgup should scroll up by the same page, got %d (before %d)", m.detailOffset, before)
	}
	m = press(t, m, "esc")
	if m.mode != modeList {
		t.Fatalf("esc should return to list: mode=%v", m.mode)
	}
	if m.detailOffset != 0 {
		t.Errorf("esc should reset detail offset, got %d", m.detailOffset)
	}
}

func TestStatus_SuccessClearsAfterFiveSeconds(t *testing.T) {
	m := New(Options{Headless: false})
	cmd := m.setStatus("Approved #1")
	if cmd == nil {
		t.Fatal("setStatus should return a clear command in interactive mode")
	}
	// The returned command is tea.Tick(5s, ...); we cannot easily drive it here,
	// but we can verify the statusClearMsg behavior directly.
	gen := m.statusGen
	next, _ := m.Update(statusClearMsg{gen: gen})
	if next.(Model).status != "" {
		t.Errorf("statusClearMsg with matching gen should clear status, got %q", next.(Model).status)
	}
}

func TestStatus_ClearIgnoredWithStaleGen(t *testing.T) {
	m := New(Options{Headless: true})
	m.setStatus("Approved #1")
	m.setStatus("Approved #2")
	next, _ := m.Update(statusClearMsg{gen: 1})
	if next.(Model).status == "" {
		t.Error("statusClearMsg with stale gen should not clear current status")
	}
}

func TestFooter_InputMode(t *testing.T) {
	m := Model{width: 120, mode: modeInput, input: inputState{action: "send"}}
	if got := m.footer(); !strings.Contains(got, "[Enter] Send") || !strings.Contains(got, "[Esc] Cancel") {
		t.Errorf("input send footer missing labels: %q", got)
	}
	m.input.action = "reject"
	if got := m.footer(); !strings.Contains(got, "[Enter] Reject") || !strings.Contains(got, "[Esc] Cancel") {
		t.Errorf("input reject footer missing labels: %q", got)
	}
}

func TestInbox_SyncsZombieWorkflowsOnLoad(t *testing.T) {
	gh, _, m := newFixture(t)
	store := m.opts.Store
	// Workflow for issue 36 is in_progress locally, but GitHub has no issue 36 open.
	// Issue 7 is open on GitHub.
	gh.Summaries = []ports.IssueSummary{{Number: 7, Title: "spec: add warning"}}
	if err := store.Upsert(state.Workflow{
		Repository: "https://github.com/kuwa72/lead-cli.git",
		Issue:      36,
		Status:     state.StatusInProgress,
		Branch:     "issue/36-ports",
	}); err != nil {
		t.Fatal(err)
	}

	// Trigger loadCmd
	m, _ = Drain(m, m.loadCmd())

	// Issue 36 must not be in running section
	for _, it := range m.sections[KindRunning].Items {
		if it.Number == 36 {
			t.Errorf("closed issue 36 must be excluded from running items: %+v", it)
		}
	}

	// In the store, issue 36 workflow status must be updated to completed
	wf, ok, err := store.Get(36, "")
	if err != nil || !ok {
		t.Fatalf("store.Get(36): ok=%v, err=%v", ok, err)
	}
	if wf.Status != state.StatusCompleted {
		t.Errorf("store status for issue 36 = %q, want %q", wf.Status, state.StatusCompleted)
	}
}

func TestInbox_FiltersRunningByRepoOnLoad(t *testing.T) {
	gh, _, m := newFixture(t)
	store := m.opts.Store
	gh.Summaries = []ports.IssueSummary{
		{Number: 83, Title: "lead issue"},
		{Number: 15, Title: "zenn issue"},
	}
	if err := store.Upsert(state.Workflow{
		Repository: "https://github.com/kuwa72/lead-cli.git",
		Issue:      83,
		Status:     state.StatusInProgress,
		Branch:     "issue/83-lead",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(state.Workflow{
		Repository: "https://github.com/kuwa72/zenn.git",
		Issue:      15,
		Status:     state.StatusInProgress,
		Branch:     "issue/15-zenn",
	}); err != nil {
		t.Fatal(err)
	}

	m, _ = Drain(m, m.loadCmd())

	running := m.sections[KindRunning].Items
	var nums []int
	for _, it := range running {
		nums = append(nums, it.Number)
	}
	for _, n := range nums {
		if n == 15 {
			t.Errorf("zenn.git issue 15 must be excluded from lead-cli inbox running: %v", nums)
		}
	}
}

func TestInputCaretNavigation(t *testing.T) {
	m := Model{
		width: 120,
		mode:  modeInput,
		input: inputState{
			prompt: "Reply > ",
			text:   []rune("hello world"),
			cursor: 11, // at end
		},
	}

	// 1. Move Left 6 times (should be at index 5, space between hello and world)
	for i := 0; i < 6; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
		m = next.(Model)
	}
	if m.input.cursor != 5 {
		t.Fatalf("cursor after 6 Left = %d, want 5", m.input.cursor)
	}

	// 2. Insert characters at cursor position
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("beautiful ")})
	m = next.(Model)
	if got := string(m.input.text); got != "hellobeautiful  world" && got != "hello beautiful world" {
		t.Errorf("text after insertion = %q, want 'hello beautiful world'", got)
	}
	if m.input.cursor != 15 {
		t.Errorf("cursor after insertion = %d, want 15", m.input.cursor)
	}

	// 3. Home key -> cursor 0
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = next.(Model)
	if m.input.cursor != 0 {
		t.Errorf("cursor after Home = %d, want 0", m.input.cursor)
	}

	// 3b. Left key at 0 should stay at 0
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = next.(Model)
	if m.input.cursor != 0 {
		t.Errorf("cursor after Left at 0 = %d, want 0", m.input.cursor)
	}

	// 4. Delete at 0 deletes first character
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDelete})
	m = next.(Model)
	if !strings.HasPrefix(string(m.input.text), "ello") {
		t.Errorf("text after Delete at start = %q, want leading 'ello'", string(m.input.text))
	}
	if m.input.cursor != 0 {
		t.Errorf("cursor after Delete at start = %d, want 0", m.input.cursor)
	}

	// 5. End key -> cursor at len(text)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = next.(Model)
	if m.input.cursor != len(m.input.text) {
		t.Errorf("cursor after End = %d, want %d", m.input.cursor, len(m.input.text))
	}

	// 5b. Right key at end should stay at len(text)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(Model)
	if m.input.cursor != len(m.input.text) {
		t.Errorf("cursor after Right at end = %d, want %d", m.input.cursor, len(m.input.text))
	}

	// 6. View() contains caret/cursor representation
	view := m.View()
	if !strings.Contains(view, "Reply > ") {
		t.Errorf("View should contain prompt, got: %s", view)
	}
}

func TestInputCaretShortcuts(t *testing.T) {
	t.Run("CtrlA and CtrlE", func(t *testing.T) {
		m := Model{
			mode: modeInput,
			input: inputState{
				text:   []rune("abcdef"),
				cursor: 3,
			},
		}
		// Ctrl+A jumps to 0
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
		m = next.(Model)
		if m.input.cursor != 0 {
			t.Errorf("cursor after Ctrl+A = %d, want 0", m.input.cursor)
		}
		// Ctrl+E jumps to end
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
		m = next.(Model)
		if m.input.cursor != 6 {
			t.Errorf("cursor after Ctrl+E = %d, want 6", m.input.cursor)
		}
	})

	t.Run("Backspace and CtrlH", func(t *testing.T) {
		m := Model{
			mode: modeInput,
			input: inputState{
				text:   []rune("hello"),
				cursor: 3, // "hel|lo"
			},
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = next.(Model)
		if string(m.input.text) != "helo" || m.input.cursor != 2 {
			t.Errorf("after Backspace text = %q (cursor %d), want 'helo' (cursor 2)", string(m.input.text), m.input.cursor)
		}
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
		m = next.(Model)
		if string(m.input.text) != "hlo" || m.input.cursor != 1 {
			t.Errorf("after Ctrl+H text = %q (cursor %d), want 'hlo' (cursor 1)", string(m.input.text), m.input.cursor)
		}
		// Backspace at 0 does nothing
		m.input.cursor = 0
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = next.(Model)
		if string(m.input.text) != "hlo" || m.input.cursor != 0 {
			t.Errorf("Backspace at 0 changed text or cursor: %q (%d)", string(m.input.text), m.input.cursor)
		}
	})

	t.Run("Delete and CtrlD", func(t *testing.T) {
		m := Model{
			mode: modeInput,
			input: inputState{
				text:   []rune("world"),
				cursor: 1, // "w|orld"
			},
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDelete})
		m = next.(Model)
		if string(m.input.text) != "wrld" || m.input.cursor != 1 {
			t.Errorf("after Delete text = %q (cursor %d), want 'wrld' (cursor 1)", string(m.input.text), m.input.cursor)
		}
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		m = next.(Model)
		if string(m.input.text) != "wld" || m.input.cursor != 1 {
			t.Errorf("after Ctrl+D text = %q (cursor %d), want 'wld' (cursor 1)", string(m.input.text), m.input.cursor)
		}
		// Delete at end does nothing
		m.input.cursor = len(m.input.text)
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDelete})
		m = next.(Model)
		if string(m.input.text) != "wld" || m.input.cursor != 3 {
			t.Errorf("Delete at end changed text or cursor: %q (%d)", string(m.input.text), m.input.cursor)
		}
	})

	t.Run("CtrlU", func(t *testing.T) {
		m := Model{
			mode: modeInput,
			input: inputState{
				text:   []rune("first second third"),
				cursor: 6, // "first |second third"
			},
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
		m = next.(Model)
		if string(m.input.text) != "second third" || m.input.cursor != 0 {
			t.Errorf("after Ctrl+U text = %q, cursor = %d; want 'second third', 0", string(m.input.text), m.input.cursor)
		}
	})

	t.Run("CtrlK", func(t *testing.T) {
		m := Model{
			mode: modeInput,
			input: inputState{
				text:   []rune("first second third"),
				cursor: 5, // "first| second third"
			},
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
		m = next.(Model)
		if string(m.input.text) != "first" || m.input.cursor != 5 {
			t.Errorf("after Ctrl+K text = %q, cursor = %d; want 'first', 5", string(m.input.text), m.input.cursor)
		}
	})

	t.Run("CtrlW", func(t *testing.T) {
		m := Model{
			mode: modeInput,
			input: inputState{
				text:   []rune("foo bar baz"),
				cursor: 11, // at end
			},
		}
		// Delete "baz"
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
		m = next.(Model)
		if string(m.input.text) != "foo bar " || m.input.cursor != 8 {
			t.Errorf("Ctrl+W word rubout = %q (cursor %d), want 'foo bar ' (cursor 8)", string(m.input.text), m.input.cursor)
		}

		// Trailing spaces: "foo bar   "
		m.input.text = []rune("foo bar   ")
		m.input.cursor = len(m.input.text)
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
		m = next.(Model)
		if string(m.input.text) != "foo " || m.input.cursor != 4 {
			t.Errorf("Ctrl+W with trailing spaces = %q (cursor %d), want 'foo ' (cursor 4)", string(m.input.text), m.input.cursor)
		}

		// Ctrl+W at start does nothing
		m.input.cursor = 0
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
		m = next.(Model)
		if string(m.input.text) != "foo " || m.input.cursor != 0 {
			t.Errorf("Ctrl+W at 0 changed text or cursor: %q (%d)", string(m.input.text), m.input.cursor)
		}
	})

	t.Run("KeySpace and KeyRunes with Japanese characters", func(t *testing.T) {
		m := Model{
			mode: modeInput,
			input: inputState{
				text:   []rune("こんにちは世界"),
				cursor: 5, // before "世界"
			},
		}
		// Space insertion
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
		m = next.(Model)
		if string(m.input.text) != "こんにちは 世界" || m.input.cursor != 6 {
			t.Errorf("after KeySpace text = %q (cursor %d), want 'こんにちは 世界' (cursor 6)", string(m.input.text), m.input.cursor)
		}
		// Insert runes
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("素敵な")})
		m = next.(Model)
		if string(m.input.text) != "こんにちは 素敵な世界" || m.input.cursor != 9 {
			t.Errorf("after KeyRunes text = %q (cursor %d), want 'こんにちは 素敵な世界' (cursor 9)", string(m.input.text), m.input.cursor)
		}
	})
}

func TestInputCaretRendering(t *testing.T) {
	tests := []struct {
		name   string
		mode   Mode
		prof   termenv.Profile
		cursor int
		text   string
	}{
		{"terminal at start", ModeTerminal, termenv.ANSI, 0, "hello"},
		{"terminal in middle", ModeTerminal, termenv.ANSI, 2, "hello"},
		{"terminal at end", ModeTerminal, termenv.ANSI, 5, "hello"},
		{"dark at end", ModeDark, termenv.TrueColor, 5, "hello"},
		{"plain at end", ModePlain, termenv.Ascii, 5, "hello"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{
				width: 80,
				mode:  modeInput,
				theme: NewTheme(tc.mode, tc.prof, nil),
				input: inputState{
					prompt: "> ",
					text:   []rune(tc.text),
					cursor: tc.cursor,
				},
			}
			view := m.View()
			if !strings.Contains(view, "> ") {
				t.Errorf("View missing prompt: %s", view)
			}
			line := m.renderInputLine()
			if !strings.HasPrefix(line, "> ") {
				t.Errorf("renderInputLine missing prompt: %s", line)
			}
			if tc.cursor == 0 && tc.mode == ModeTerminal {
				// At start, first char 'h' should have reverse video
				if !strings.Contains(line, "\x1b[7mh") {
					t.Errorf("expected reverse video on 'h', got %q", line)
				}
			}
			if tc.cursor == len(tc.text) && tc.mode == ModeTerminal {
				// At end, should render reverse space
				if !strings.Contains(line, "\x1b[7m \x1b[0m") {
					t.Errorf("expected reverse space at end, got %q", line)
				}
			}
		})
	}
}

func TestInputModeInitialization(t *testing.T) {
	_, _, m := newFixture(t)
	m.opts.Say = func(ctx context.Context, oneLiner string, followUp int) (string, error) {
		return "ok", nil
	}

	// Press 's' from list mode -> modeInput with cursor 0
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	sModel := next.(Model)
	if sModel.mode != modeInput {
		t.Fatalf("expected modeInput after 's', got %v", sModel.mode)
	}
	if sModel.input.cursor != len(sModel.input.text) {
		t.Errorf("cursor after 's' = %d, want %d", sModel.input.cursor, len(sModel.input.text))
	}

	// Press 't' on selected item -> modeInput with cursor 0
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	tModel := next.(Model)
	if tModel.mode != modeInput {
		t.Fatalf("expected modeInput after 't', got %v", tModel.mode)
	}
	if tModel.input.cursor != len(tModel.input.text) {
		t.Errorf("cursor after 't' = %d, want %d", tModel.input.cursor, len(tModel.input.text))
	}

	// Press 'x' on selected item -> modeInput with cursor 0
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	xModel := next.(Model)
	if xModel.mode != modeInput {
		t.Fatalf("expected modeInput after 'x', got %v", xModel.mode)
	}
	if xModel.input.cursor != len(xModel.input.text) {
		t.Errorf("cursor after 'x' = %d, want %d", xModel.input.cursor, len(xModel.input.text))
	}

	// Move to merged item and press 'n' -> modeInput with cursor 0
	m.cursor = 4 // merged item in fixture
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	nModel := next.(Model)
	if nModel.mode != modeInput {
		t.Fatalf("expected modeInput after 'n', got %v", nModel.mode)
	}
	if nModel.input.cursor != len(nModel.input.text) {
		t.Errorf("cursor after 'n' = %d, want %d", nModel.input.cursor, len(nModel.input.text))
	}
}

func TestInbox_AgentAndModeToggle(t *testing.T) {
	var savedAgent, savedMode string
	m := New(Options{
		Headless:  true,
		Agent:     "agy",
		AgentMode: "batch",
		OnConfigChange: func(agent, mode string) {
			savedAgent = agent
			savedMode = mode
		},
	})
	m.width = 120

	// Initial values
	if m.Agent() != "agy" {
		t.Errorf("initial agent = %q, want 'agy'", m.Agent())
	}
	if m.AgentMode() != "batch" {
		t.Errorf("initial mode = %q, want 'batch'", m.AgentMode())
	}

	// Press 'm' to toggle mode (batch -> dangerous)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = next.(Model)
	if m.AgentMode() != "dangerous" {
		t.Errorf("mode after 1st 'm' = %q, want 'dangerous'", m.AgentMode())
	}
	if savedMode != "dangerous" {
		t.Errorf("savedMode = %q, want 'dangerous'", savedMode)
	}

	// Press 'm' again (dangerous -> interactive)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = next.(Model)
	if m.AgentMode() != "interactive" {
		t.Errorf("mode after 2nd 'm' = %q, want 'interactive'", m.AgentMode())
	}

	// Press 'm' again (interactive -> batch)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = next.(Model)
	if m.AgentMode() != "batch" {
		t.Errorf("mode after 3rd 'm' = %q, want 'batch'", m.AgentMode())
	}

	// Press 'g' to toggle agent (agy -> claude)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = next.(Model)
	if m.Agent() != "claude" {
		t.Errorf("agent after 1st 'g' = %q, want 'claude'", m.Agent())
	}
	if savedAgent != "claude" {
		t.Errorf("savedAgent = %q, want 'claude'", savedAgent)
	}

	// Check footer tokens include Mode and Agent
	footer := m.footer()
	if !strings.Contains(footer, "[m] Mode") {
		t.Errorf("footer should contain '[m] Mode', got: %s", footer)
	}
	if !strings.Contains(footer, "[g] Agent") {
		t.Errorf("footer should contain '[g] Agent', got: %s", footer)
	}
}


