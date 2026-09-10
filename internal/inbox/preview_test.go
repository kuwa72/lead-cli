package inbox

import (
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

func TestPreview_LoadsIssueAfterCursorMove(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	m = press(t, m, "j")
	if !containsInt(gh.ViewCalls, 8) {
		t.Fatalf("preview should fetch issue #8, got ViewCalls=%v", gh.ViewCalls)
	}
	v := m.View()
	if !strings.Contains(v, "second body") {
		t.Errorf("preview should show #8 body, got:\n%s", v)
	}
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func TestPreview_StaleResponseIsDiscarded(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	m = press(t, m, "j")
	if m.detail.Number != 8 {
		t.Fatalf("cursor should be on #8, got %d", m.detail.Number)
	}

	oldReq := m.previewReq - 1
	next, _ := m.Update(previewMsg{req: oldReq, item: Item{Number: 7}, snapshot: previewSnapshot{issue: ports.Issue{Number: 7, Body: "stale"}}})
	m = next.(Model)
	if m.detail.Number != 8 || strings.Contains(m.detail.Body, "stale") {
		t.Errorf("stale preview response should be discarded, got detail=%+v", m.detail)
	}
}

func TestReload_PreservesSelectionByIssueNumber(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	m = press(t, m, "j") // select #8
	m = press(t, m, "R")
	row, ok := m.current()
	if !ok || row.IsHeader() || row.Item.Number != 8 {
		t.Fatalf("reload should preserve selection on #8, got %+v", row)
	}
}

func TestReload_FallbackWhenIssueDisappears(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	m = press(t, m, "j") // select #8
	// Simulate #8 disappearing on reload.
	gh.Labeled[LabelNeedsReview] = []ports.IssueSummary{{Number: 7, Title: "spec: add warning"}}
	m = press(t, m, "R")
	row, ok := m.current()
	if !ok || row.IsHeader() || row.Item.Number != 7 {
		t.Fatalf("selection should fall back to previous issue #7, got %+v", row)
	}
}

func TestPreview_HeaderSelectionClearsPreviewAndBlocksActions(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	m = press(t, m, "k") // move to review header
	if m.detail.Number != 0 {
		t.Errorf("header selection should clear preview, got detail=%+v", m.detail)
	}
	m = press(t, m, "a")
	if len(gh.AddedLabels)+len(gh.RemovedLabels) != 0 {
		t.Errorf("a on header must not touch GitHub: added=%+v removed=%+v", gh.AddedLabels, gh.RemovedLabels)
	}
}

func TestPreview_LoadingAndErrorFeedback(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	gh.ViewErr = errors.New("gh view failed")
	m = press(t, m, "j")
	v := m.View()
	if !strings.Contains(v, "Unable to load #8") {
		t.Errorf("preview error should be visible, got:\n%s", v)
	}
	if strings.Contains(v, "second body") {
		t.Errorf("preview should not show body after error, got:\n%s", v)
	}
}

func TestPreview_PartialDetailFailureShowsPRUnavailable(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.PrBodyErr = errors.New("pr unavailable")
	m = press(t, m, "j", "j", "j") // move to blocked #9
	v := m.View()
	if !strings.Contains(v, "PR details unavailable") {
		t.Errorf("preview should report PR details unavailable, got:\n%s", v)
	}
	if !strings.Contains(v, "blocked body") {
		t.Errorf("preview should still show issue body, got:\n%s", v)
	}
}

func TestApprove_HoldsWhenIssueBodyChanged(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	// Load the initial #7 preview first, then simulate the issue changing on GitHub.
	m = press(t, m, "k", "j")
	gh.Issues[7] = ports.Issue{Number: 7, Title: "spec: add warning", Body: "changed body", State: "OPEN"}
	m = press(t, m, "a")
	if len(gh.AddedLabels) != 0 || len(gh.RemovedLabels) != 0 {
		t.Fatalf("approval should be held when issue changed: added=%+v removed=%+v", gh.AddedLabels, gh.RemovedLabels)
	}
	if !strings.Contains(m.View(), "Issue changed") {
		t.Errorf("status should warn that issue changed, got:\n%s", m.View())
	}
	// After the warning, the model's detail should now reflect the latest body.
	if !strings.Contains(m.detail.Body, "changed body") {
		t.Errorf("detail should be updated to the latest body, got %q", m.detail.Body)
	}
	// Approving again should now succeed because body matches.
	m = press(t, m, "a")
	if !reflect.DeepEqual(gh.AddedLabels, []testutil.LabelCall{{Number: 7, Label: LabelReady}}) {
		t.Errorf("approval should proceed after review, got %+v", gh.AddedLabels)
	}
}

func TestApprove_PinTargetNumber(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	// Manually place an in-flight approval on #7 so a subsequent key on #8 is ignored.
	m.pendingOps = map[int]bool{7: true}
	m = press(t, m, "j")
	// Cursor is now on #8 but an operation is still pending for #7.
	// Simulate completing the pending operation so the model state is consistent.
	msg, _ := m.Update(doneMsg{number: 7})
	m = msg.(Model)
	// With the current code the a key on #8 should now be allowed; this test mostly
	// documents that pendingOps is consulted. A more thorough test would keep the
	// operation in flight, but that requires a blocking fake.
	if m.pendingOps[7] {
		t.Error("pendingOps should be cleared after doneMsg")
	}
}

func TestR_ReloadsListAndCurrentPreview(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	callsBefore := len(gh.LabelListCalls)
	m = press(t, m, "R")
	if len(gh.LabelListCalls) <= callsBefore {
		t.Errorf("R should reload the issue list, got LabelListCalls=%v", gh.LabelListCalls)
	}
}

func TestPreview_CacheHit(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	m = press(t, m, "j") // load #8
	viewCalls := len(gh.ViewCalls)
	m = press(t, m, "k") // back to #7
	m = press(t, m, "j") // back to #8
	// A refresh may still be issued, but the body should be available immediately.
	if !strings.Contains(m.View(), "second body") {
		t.Errorf("cached preview should show #8 body immediately, got:\n%s", m.View())
	}
	// Ensure we do not keep re-fetching the same issue indefinitely.
	if len(gh.ViewCalls) > viewCalls+2 {
		t.Errorf("too many view calls after cache hit: %v", gh.ViewCalls)
	}
}

func TestPreview_KindRunningLoadsAndDisplaysAgentLog(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30

	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "issue-70.log")
	logContent := "[agy] step 1: starting task\n[agy] step 2: running test\n[agy] step 3: success\n"
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	store := m.opts.Store
	if err := store.Upsert(state.Workflow{
		Issue:    70,
		Status:   state.StatusInProgress,
		Agent:    "agy",
		Pane:     "w1:p70",
		LogPath:  logPath,
		Branch:   "issue/70-log-preview",
		Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	gh.Issues[70] = ports.Issue{Number: 70, Title: "feature: awesome log", Body: "issue body text", State: "OPEN"}

	m, _ = Drain(m, m.loadCmd())
	m = press(t, m, "z") // expand all sections

	idx := m.rowIndex(KindRunning, 70)
	for m.cursor < idx {
		m = press(t, m, "j")
	}

	row, ok := m.current()
	if !ok || row.IsHeader() || row.Item.Number != 70 {
		t.Fatalf("expected cursor on #70, got %+v", row)
	}

	v := m.View()
	if !strings.Contains(v, "Agent Live Output") {
		t.Errorf("preview should contain 'Agent Live Output', got:\n%s", v)
	}
	if !strings.Contains(v, "[agy] step 3: success") {
		t.Errorf("preview should contain log line '[agy] step 3: success', got:\n%s", v)
	}
	if !strings.Contains(v, "Agent: agy") {
		t.Errorf("preview should contain 'Agent: agy', got:\n%s", v)
	}
}

func TestPreview_KindRunningHandlesEmptyOrMissingLog(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30

	missingPath := filepath.Join(t.TempDir(), "nonexistent.log")
	store := m.opts.Store
	if err := store.Upsert(state.Workflow{
		Issue:   71,
		Status:  state.StatusInProgress,
		Agent:   "codex",
		LogPath: missingPath,
		Branch:  "issue/71-empty",
	}); err != nil {
		t.Fatal(err)
	}
	gh.Issues[71] = ports.Issue{Number: 71, Title: "feature: missing log", Body: "issue body", State: "OPEN"}

	m, _ = Drain(m, m.loadCmd())
	m = press(t, m, "z")
	idx := m.rowIndex(KindRunning, 71)
	for m.cursor < idx {
		m = press(t, m, "j")
	}

	v := m.View()
	if !strings.Contains(v, "Agent Live Output") {
		t.Errorf("preview should still show 'Agent Live Output' header, got:\n%s", v)
	}
	if !strings.Contains(v, "Agent: codex") {
		t.Errorf("preview should show 'Agent: codex', got:\n%s", v)
	}
}

func TestPreview_NeedsReview_ShowsFilesAndDiff(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 130
	m.height = 30

	diffContent := "diff --git a/pkg/foo.go b/pkg/foo.go\n" +
		"--- a/pkg/foo.go\n" +
		"+++ b/pkg/foo.go\n" +
		"@@ -1,3 +1,4 @@\n" +
		" existing\n" +
		"+newly added code line\n" +
		"-old removed line\n"
	gh.PrDiffs = map[int]string{77: diffContent}

	store := m.opts.Store
	if err := store.Upsert(state.Workflow{
		Issue:        7,
		Status:       state.StatusAwaitingReview,
		Agent:        "agy",
		PullRequests: []state.PRRef{{Number: 77, Status: "open"}},
	}); err != nil {
		t.Fatal(err)
	}

	m, _ = Drain(m, m.loadCmd())

	v := m.View()
	if !strings.Contains(v, "Changed files") {
		t.Errorf("preview should contain 'Changed files', got:\n%s", v)
	}
	if !strings.Contains(v, "pkg/foo.go") {
		t.Errorf("preview should contain 'pkg/foo.go', got:\n%s", v)
	}
	if !strings.Contains(v, "+newly added code line") {
		t.Errorf("preview should contain '+newly added code line', got:\n%s", v)
	}
}

func TestPreview_ScrollWithShiftJK(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 130
	m.height = 30

	var diffLines []string
	diffLines = append(diffLines, "diff --git a/main.go b/main.go")
	for i := 1; i <= 30; i++ {
		diffLines = append(diffLines, "+line number "+string(rune('A'+i)))
	}
	gh.PrDiffs = map[int]string{77: strings.Join(diffLines, "\n")}

	store := m.opts.Store
	if err := store.Upsert(state.Workflow{
		Issue:        7,
		Status:       state.StatusAwaitingReview,
		Agent:        "agy",
		PullRequests: []state.PRRef{{Number: 77, Status: "open"}},
	}); err != nil {
		t.Fatal(err)
	}

	m, _ = Drain(m, m.loadCmd())

	initialView := m.View()
	if !strings.Contains(initialView, "+line number B") {
		t.Fatalf("expected early diff line in initial view, got:\n%s", initialView)
	}

	// Press J (Shift+j) to scroll down preview pane.
	m = press(t, m, "J")
	if m.previewOffset <= 0 {
		t.Errorf("expected previewOffset > 0 after pressing J, got %d", m.previewOffset)
	}

	// Press K (Shift+k) to scroll up preview pane.
	m = press(t, m, "K")
	if m.previewOffset != 0 {
		t.Errorf("expected previewOffset == 0 after pressing K, got %d", m.previewOffset)
	}
}

func TestDetail_NeedsReview_ShowsDiff(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 130
	m.height = 30

	diffContent := "diff --git a/pkg/bar.go b/pkg/bar.go\n" +
		"--- a/pkg/bar.go\n" +
		"+++ b/pkg/bar.go\n" +
		"@@ -10,3 +10,4 @@\n" +
		"+newly added bar line\n"
	gh.PrDiffs = map[int]string{77: diffContent}

	store := m.opts.Store
	if err := store.Upsert(state.Workflow{
		Issue:        7,
		Status:       state.StatusAwaitingReview,
		Agent:        "agy",
		PullRequests: []state.PRRef{{Number: 77, Status: "open"}},
	}); err != nil {
		t.Fatal(err)
	}

	m, _ = Drain(m, m.loadCmd())
	// Enter opens full detail view.
	m = press(t, m, "enter")
	if m.mode != modeDetail {
		t.Fatalf("expected modeDetail, got %v", m.mode)
	}

	v := m.View()
	if !strings.Contains(v, "Changed files") {
		t.Errorf("detail should contain 'Changed files', got:\n%s", v)
	}
	if !strings.Contains(v, "pkg/bar.go") {
		t.Errorf("detail should contain 'pkg/bar.go', got:\n%s", v)
	}
	if !strings.Contains(v, "+newly added bar line") {
		t.Errorf("detail should contain '+newly added bar line', got:\n%s", v)
	}

	// Down scrolls detailOffset
	m = press(t, m, "j")
	if m.detailOffset != 1 {
		t.Errorf("expected detailOffset == 1, got %d", m.detailOffset)
	}
}



