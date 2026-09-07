package cli

import (
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
	"github.com/kuwa72/lead-cli/internal/tui"
)

func tuiDeps(t *testing.T, repo string, sel tui.Selection, selErr error) (Deps, *testutil.FakeGhClient, string) {
	t.Helper()
	fake := &testutil.FakeGhClient{
		Summaries: []ports.IssueSummary{{Number: 36, Title: "ports adapter"}},
		Issues: map[int]ports.Issue{
			36: {Number: 36, Title: "ports adapter", Body: "body", State: "OPEN"},
		},
	}
	stateFile := t.TempDir() + "/wf.json"
	deps := Deps{
		Gh: fake, StateFile: stateFile, WorkDir: repo,
		Selector: &tui.FakeSelector{Selection: sel, Err: selErr},
	}
	return deps, fake, stateFile
}

func TestWorkTUI_SelectsIssueAndStartsWork(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := tuiDeps(t, repo,
		tui.Selection{IssueNumber: 36, Agent: "devin", Action: tui.ActionWork}, nil)

	out, err := executeWith(t, deps, "work")
	if err != nil {
		t.Fatalf("TUI work: %v\n%s", err, out)
	}
	ok, err := git.New().BranchExists(repo, "issue/36-ports-adapter")
	if err != nil || !ok {
		t.Errorf("branch created = %v, %v", ok, err)
	}
	all := loadState(t, stateFile)
	if len(all) != 1 || all[0].Issue != 36 {
		t.Errorf("state = %+v, want record for selected #36", all)
	}
}

func TestWorkTUI_BrowserOpensWithoutWork(t *testing.T) {
	repo := initRepo(t)
	deps, fake, stateFile := tuiDeps(t, repo,
		tui.Selection{IssueNumber: 36, Action: tui.ActionBrowse}, nil)

	out, err := executeWith(t, deps, "work")
	if err != nil {
		t.Fatalf("TUI browse: %v\n%s", err, out)
	}
	if len(fake.Browsed) != 1 || fake.Browsed[0] != 36 {
		t.Errorf("browsed = %v, want [36]", fake.Browsed)
	}
	if all := loadState(t, stateFile); len(all) != 0 {
		t.Errorf("state = %+v, want no record on browse", all)
	}
	if !strings.Contains(out, "browser") {
		t.Errorf("output = %q, want browser notice", out)
	}
}

func TestWorkTUI_AbortCancelsCleanly(t *testing.T) {
	repo := initRepo(t)
	deps, fake, stateFile := tuiDeps(t, repo, tui.Selection{}, tui.ErrAborted)

	_, err := executeWith(t, deps, "work")
	if err == nil {
		t.Fatal("aborted TUI work = nil, want abort error")
	}
	if len(fake.ViewCalls) != 0 {
		t.Errorf("gh calls = %v, want none after abort", fake.ViewCalls)
	}
	if all := loadState(t, stateFile); len(all) != 0 {
		t.Errorf("state = %+v, want nothing recorded", all)
	}
}

func TestWorkTUI_EmptyListReportsCleanly(t *testing.T) {
	repo := initRepo(t)
	fake := &testutil.FakeGhClient{Summaries: []ports.IssueSummary{}}
	deps := Deps{Gh: fake, StateFile: t.TempDir() + "/wf.json", WorkDir: repo,
		Selector: &tui.FakeSelector{}}

	out, err := executeWith(t, deps, "work")
	if err != nil {
		t.Fatalf("empty-list work: %v", err)
	}
	if !strings.Contains(out, "no open issues") {
		t.Errorf("output = %q, want empty notice", out)
	}
}

func TestParseTestSelection(t *testing.T) {
	sel, err := parseTestSelection("36:devin")
	if err != nil || sel.IssueNumber != 36 || sel.Agent != "devin" || sel.Action != tui.ActionWork {
		t.Errorf("parse 36:devin = %+v, %v", sel, err)
	}
	sel, err = parseTestSelection("36:browser")
	if err != nil || sel.Action != tui.ActionBrowse {
		t.Errorf("parse 36:browser = %+v, %v", sel, err)
	}
	sel, err = parseTestSelection("36")
	if err != nil || sel.Agent != "agy" {
		t.Errorf("parse bare number defaults agent: %+v, %v", sel, err)
	}
	for _, bad := range []string{"", "abc", "36:", ":agy", "0:agy"} {
		if _, err := parseTestSelection(bad); err == nil {
			t.Errorf("parse %q = nil, want error", bad)
		}
	}
}
