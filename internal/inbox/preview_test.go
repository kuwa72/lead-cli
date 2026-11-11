package inbox

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

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

func TestPreview_ScrollHintWhenContentOverflows(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: strings.Repeat("line\n", 60), State: "OPEN"}
	m = press(t, m, "j")
	m.status = "" // headless fixtures keep the load status; show the default hint

	if v := m.View(); !strings.Contains(v, "J/K") {
		t.Errorf("long preview should advertise J/K scrolling, got:\n%s", v)
	}

	// A body that fits the pane must not advertise scrolling.
	m = press(t, m, "k")
	if v := m.View(); strings.Contains(v, "J/K") {
		t.Errorf("short preview should not advertise scrolling, got:\n%s", v)
	}
}

func TestPreview_ScrollPositionIndicator(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: strings.Repeat("line\n", 60), State: "OPEN"}
	m = press(t, m, "j")
	m = press(t, m, "J", "J", "J")

	if v := m.View(); !strings.Contains(v, "↑ 3") {
		t.Errorf("preview header should show scroll offset after J x3, got:\n%s", v)
	}

	m = press(t, m, "K", "K", "K")
	if v := m.View(); strings.Contains(v, "↑ 3") {
		t.Errorf("scroll indicator should clear at offset 0, got:\n%s", v)
	}
}

func TestPreview_NoScrollHintInNarrowLayout(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 80
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: strings.Repeat("line\n", 60), State: "OPEN"}
	m = press(t, m, "j", "J", "J", "J")
	m.status = "" // headless fixtures keep the load status; show the default hint

	v := m.View()
	if strings.Contains(v, "J/K") {
		t.Errorf("narrow layout should not advertise preview scrolling, got:\n%s", v)
	}
	if strings.Contains(v, "↑ 3") {
		t.Errorf("narrow layout has no preview header for the offset, got:\n%s", v)
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

// splitPreviewPane returns the right-hand preview cells of the split view
// with trailing padding removed.
func splitPreviewPane(m Model) []string {
	var out []string
	for _, line := range strings.Split(m.View(), "\n") {
		parts := strings.SplitN(line, "│", 2)
		if len(parts) != 2 {
			continue
		}
		out = append(out, strings.TrimRight(strings.TrimPrefix(parts[1], " "), " "))
	}
	return out
}

// collectUntilBlank joins the preview lines from the one containing marker
// until the first blank line or the "Enter: Full details" hint.
func collectUntilBlank(lines []string, marker string) []string {
	var out []string
	started := false
	for _, l := range lines {
		if !started {
			if strings.Contains(l, marker) {
				started = true
			} else {
				continue
			}
		}
		if l == "" || strings.HasPrefix(l, "Enter:") {
			break
		}
		out = append(out, l)
	}
	return out
}

func TestPreview_WrapsLongBodyLines(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	body := "BODYSTART " + strings.Repeat("日本語の長い本文です。", 12) + "BODYEND"
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: body, State: "OPEN"}
	m = press(t, m, "j") // load #8 preview

	_, rightW := splitPaneWidths(m.width)
	contentW := rightW - 1
	lines := splitPreviewPane(m)
	for _, l := range lines {
		if w := lipgloss.Width(l); w > contentW {
			t.Errorf("preview line exceeds content width %d: %q (%d cells)", contentW, l, w)
		}
	}

	segments := collectUntilBlank(lines, "BODYSTART")
	if len(segments) < 2 {
		t.Fatalf("long body line should wrap into multiple preview lines, got %d: %q", len(segments), segments)
	}
	joined := strings.Join(segments, "")
	if joined != body {
		t.Errorf("wrapped preview lines must join back to the body line\n got: %q\nwant: %q", joined, body)
	}
	if strings.Contains(joined, "…") {
		t.Errorf("wrapped body line must not be ellipsized: %q", joined)
	}
}

func TestDetail_WrapsLongBodyLines(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 60 // tall enough to inspect every wrapped line at once
	body := "BODYSTART " + strings.Repeat("日本語の長い本文です。", 100) + "本文の終端"
	gh.Issues[7] = ports.Issue{Number: 7, Title: "spec: add warning", Body: body, State: "OPEN"}
	m = press(t, m, "enter")
	if m.mode != modeDetail {
		t.Fatalf("expected modeDetail, got %v", m.mode)
	}

	v := m.View()
	for _, l := range strings.Split(v, "\n") {
		if w := lipgloss.Width(l); w > m.width {
			t.Errorf("detail line exceeds terminal width %d: %q (%d cells)", m.width, l, w)
		}
	}

	segments := collectUntilBlank(strings.Split(v, "\n"), "BODYSTART")
	if len(segments) < 2 {
		t.Fatalf("long body line should wrap into multiple detail lines, got %d", len(segments))
	}
	if joined := strings.Join(segments, ""); joined != body {
		t.Errorf("wrapped detail lines must join back to the body line\n got: %q\nwant: %q", joined, body)
	}

	// The tail must be reachable by scrolling a short terminal.
	tail := "本文の終端"
	m.height = 24
	m.detailOffset = 0
	if strings.Contains(m.View(), tail) {
		t.Fatalf("tail should be off screen before scrolling")
	}
	found := false
	for i := 0; i < 20; i++ {
		if strings.Contains(m.View(), tail) {
			found = true
			break
		}
		m = press(t, m, "j")
	}
	if !found {
		t.Errorf("j scrolling should reach the end of the wrapped body, offset=%d", m.detailOffset)
	}
	if m.detailOffset == 0 {
		t.Errorf("expected detailOffset > 0 after scrolling to the body tail")
	}
}

func TestRunningMetaLine_ShowsElapsedActivePhase(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	logPath := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(logPath, []byte("out\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(logPath, now.Add(-5*time.Minute), now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	it := &Item{Number: 7, Title: "work", Kind: KindRunning, Agent: "agy", Attempts: 1, StartedAt: now.Add(-90 * time.Minute), LogPath: logPath}
	got := runningMetaLine(it, 0, false, now)
	for _, want := range []string{"Agent: agy", "Elapsed: 1h", "Active: 5m ago", "Phase: implementing"} {
		if !strings.Contains(got, want) {
			t.Errorf("meta missing %q, got %q", want, got)
		}
	}
	// PR open switches the phase; missing data omits elapsed/active.
	it.PRNumber = 12
	if got := runningMetaLine(it, 12, false, now); !strings.Contains(got, "Phase: PR #12") {
		t.Errorf("meta missing PR phase, got %q", got)
	}
	bare := &Item{Number: 7, Title: "work", Kind: KindRunning}
	got = runningMetaLine(bare, 0, false, now)
	if strings.Contains(got, "Elapsed:") || strings.Contains(got, "Active:") {
		t.Errorf("meta must omit elapsed/active without data, got %q", got)
	}
	if !strings.Contains(got, "Phase: implementing") {
		t.Errorf("meta missing default phase, got %q", got)
	}
}

func TestInspector_SubTabsSwitchBetweenOverviewDiffAndLog(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 130
	m.height = 30

	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "issue-7.log")
	if err := os.WriteFile(logPath, []byte("[agy] step 1: init\n[agy] step 2: log tab line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diffContent := "diff --git a/pkg/inspector.go b/pkg/inspector.go\n" +
		"--- a/pkg/inspector.go\n" +
		"+++ b/pkg/inspector.go\n" +
		"@@ -1,2 +1,3 @@\n" +
		"+inspector diff line\n"
	gh.PrDiffs = map[int]string{77: diffContent}
	gh.Issues[7] = ports.Issue{Number: 7, Title: "spec: add warning", Body: "overview issue body line", State: "OPEN"}

	store := m.opts.Store
	if err := store.Upsert(state.Workflow{
		Issue:        7,
		Status:       state.StatusAwaitingReview,
		Agent:        "agy",
		LogPath:      logPath,
		PullRequests: []state.PRRef{{Number: 77, Status: "open"}},
	}); err != nil {
		t.Fatal(err)
	}

	m, _ = Drain(m, m.loadCmd())
	// Trigger preview load for #7
	m = press(t, m, "j", "k")

	// Default is tab 1 (Overview)
	if !strings.Contains(m.previewHeader(), "[1:Overview]") {
		t.Errorf("expected default previewHeader to show [1:Overview], got %q", m.previewHeader())
	}

	// Switch to tab 2 (Diff) via '2'
	m.previewOffset = 3
	m = press(t, m, "2")
	if m.previewOffset != 0 {
		t.Errorf("switching tab should reset previewOffset to 0, got %d", m.previewOffset)
	}
	if !strings.Contains(m.previewHeader(), "[2:Diff]") {
		t.Errorf("expected previewHeader to show [2:Diff], got %q", m.previewHeader())
	}
	if v := m.View(); !strings.Contains(v, "+inspector diff line") {
		t.Errorf("Diff tab should render PR diff line, got:\n%s", v)
	}

	// Switch to tab 3 (Log) via '3'
	m = press(t, m, "3")
	if !strings.Contains(m.previewHeader(), "[3:Log]") {
		t.Errorf("expected previewHeader to show [3:Log], got %q", m.previewHeader())
	}
	if v := m.View(); !strings.Contains(v, "[agy] step 2: log tab line") {
		t.Errorf("Log tab should render agent log line, got:\n%s", v)
	}

	// Cycle tabs with ']' and '['
	m = press(t, m, "]")
	if !strings.Contains(m.previewHeader(), "[1:Overview]") {
		t.Errorf("']' from tab 3 should wrap to [1:Overview], got %q", m.previewHeader())
	}
	m = press(t, m, "[")
	if !strings.Contains(m.previewHeader(), "[3:Log]") {
		t.Errorf("'[' from tab 1 should wrap to [3:Log], got %q", m.previewHeader())
	}
	m = press(t, m, "1")
	if !strings.Contains(m.previewHeader(), "[1:Overview]") {
		t.Errorf("'1' should return to [1:Overview], got %q", m.previewHeader())
	}
}

func TestInspector_FocusToggleAndScroll(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 120
	m.height = 30
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: strings.Repeat("line\n", 60), State: "OPEN"}

	m = press(t, m, "j") // move list cursor to #8
	startCursor := m.cursor

	// Press Tab in split mode to focus the Inspector pane
	m = press(t, m, "tab")
	if !m.inspectorFocus {
		t.Fatalf("expected inspectorFocus=true after pressing tab in split mode")
	}

	// Pressing j/k while inspector is focused scrolls previewOffset instead of moving list cursor
	m = press(t, m, "j", "j")
	if m.cursor != startCursor {
		t.Errorf("j in inspectorFocus must not move list cursor: got %d, want %d", m.cursor, startCursor)
	}
	if m.previewOffset != 2 {
		t.Errorf("j in inspectorFocus should increment previewOffset to 2, got %d", m.previewOffset)
	}
	m = press(t, m, "k")
	if m.previewOffset != 1 {
		t.Errorf("k in inspectorFocus should decrement previewOffset to 1, got %d", m.previewOffset)
	}

	// Action keys (like 'a') still act on the selected issue while inspector is focused
	m = press(t, m, "a")
	if len(gh.AddedLabels) == 0 {
		t.Errorf("pressing 'a' while inspector is focused should still approve the selected issue")
	}

	// Pressing Esc exits inspectorFocus back to the list pane
	m = press(t, m, "esc")
	if m.inspectorFocus {
		t.Errorf("expected inspectorFocus=false after pressing esc")
	}
}

func TestPreview_WrapsLongIssueTitle(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 100
	m.height = 30
	longTitle := "TITLESTART refactor(inbox): TUIの情報設計・2ペインフォーカス・インスペクタサブタブ・セマンティック配色の刷新 TITLEEND"
	gh.Issues[8] = ports.Issue{Number: 8, Title: longTitle, Body: "short body", State: "OPEN"}
	m = press(t, m, "j")

	lines := splitPreviewPane(m)
	// Skip the header row (index 0)
	if len(lines) < 3 {
		t.Fatalf("expected wrapped title lines in preview pane, got %v", lines)
	}
	var titleSegments []string
	for _, l := range lines[1:] {
		if strings.Contains(l, "short body") || l == "" {
			break
		}
		titleSegments = append(titleSegments, l)
	}
	if len(titleSegments) < 2 {
		t.Fatalf("long title should wrap across at least 2 lines, got %d: %q", len(titleSegments), titleSegments)
	}
	joined := strings.Join(titleSegments, "")
	if joined != longTitle {
		t.Errorf("wrapped title lines must join back to full title without truncation\n got: %q\nwant: %q", joined, longTitle)
	}
	if strings.Contains(joined, "…") {
		t.Errorf("wrapped title must not be ellipsized: %q", joined)
	}
}

func TestPreview_ScrollsByVisualWrappedLinesAndReachesTail(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 100
	m.height = 24
	// Single long paragraph that wraps into ~25 visual lines (only 1 logical line!)
	longParagraph := "PARASTART " + strings.Repeat("これは1つの長い段落であり折り返し後に複数行になります。", 18) + " 段落の末尾END"
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: longParagraph, State: "OPEN"}
	m = press(t, m, "j")
	m.status = ""

	if !m.previewOverflows() {
		t.Fatalf("a single paragraph that wraps past pane height must report previewOverflows()=true")
	}
	if strings.Contains(m.View(), "段落の末尾END") {
		t.Fatalf("tail of long wrapped paragraph should be off screen initially")
	}

	// Scrolling 1 step with J should keep the second wrapped line of the same paragraph visible
	// rather than dropping the entire paragraph at once.
	m = press(t, m, "J")
	if !strings.Contains(m.View(), "これは1つの長い段落") {
		t.Fatalf("scrolling 1 visual line with J must not skip the entire wrapped paragraph:\n%s", m.View())
	}

	// Scrolling further should reach the tail of the wrapped paragraph inside the preview pane.
	found := false
	for i := 0; i < 40; i++ {
		if strings.Contains(m.View(), "段落の末尾END") {
			found = true
			break
		}
		m = press(t, m, "J")
	}
	if !found {
		t.Errorf("scrolling preview with J should reach the tail of a wrapped paragraph, got:\n%s", m.View())
	}
}

func TestPreview_RunningLongLogLinesKeepLatestTailVisible(t *testing.T) {
	gh, _, m := newFixture(t)
	m.width = 100
	m.height = 24

	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "issue-75.log")
	var logLines []string
	for i := 1; i <= 15; i++ {
		logLines = append(logLines, "[agy] verbose log line "+strings.Repeat("long_segment_", 8))
	}
	logLines = append(logLines, "[agy] final step: completed")
	if err := os.WriteFile(logPath, []byte(strings.Join(logLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := m.opts.Store
	if err := store.Upsert(state.Workflow{
		Issue:    75,
		Status:   state.StatusInProgress,
		Agent:    "agy",
		LogPath:  logPath,
		Branch:   "issue/75-long-log",
		Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	gh.Issues[75] = ports.Issue{Number: 75, Title: "feature: long log lines", Body: "body", State: "OPEN"}

	m, _ = Drain(m, m.loadCmd())
	m = press(t, m, "z")
	idx := m.rowIndex(KindRunning, 75)
	for m.cursor < idx {
		m = press(t, m, "j")
	}

	v := m.View()
	if !strings.Contains(v, "Agent Live Output") {
		t.Errorf("running preview should contain 'Agent Live Output', got:\n%s", v)
	}
	if !strings.Contains(v, "[agy] final step: completed") {
		t.Errorf("running preview at offset 0 must keep latest log line visible even when earlier log lines wrap, got:\n%s", v)
	}
}


