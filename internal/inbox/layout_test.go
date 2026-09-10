package inbox

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

func newSizedFixture(t *testing.T, w, h int) Model {
	t.Helper()
	_, _, m := newFixture(t)
	m.width = w
	m.height = h
	return m
}

func resizeWindow(t *testing.T, m Model, w, h int) Model {
	t.Helper()
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

func TestLayout_TooSmallNoticeAndKeyBlock(t *testing.T) {
	m := newSizedFixture(t, 79, 24)
	v := m.View()
	if !strings.Contains(v, "Terminal too small. Resize to 80x24.") {
		t.Fatalf("expected size notice, got:\n%s", v)
	}
	if strings.Contains(v, "#7") {
		t.Errorf("too-small view should not render the inbox list, got:\n%s", v)
	}
	gh := m.opts.Gh.(*testutil.FakeGhClient)
	m = press(t, m, "a")
	if len(gh.AddedLabels) != 0 {
		t.Errorf("a should be blocked while too small")
	}
}

func TestLayout_HeightTooSmall(t *testing.T) {
	m := newSizedFixture(t, 120, 23)
	if !strings.Contains(m.View(), "Terminal too small") {
		t.Errorf("height 23 should show size notice")
	}
}

func TestLayout_WidthModes(t *testing.T) {
	tests := []struct {
		w       int
		split   bool
	}{
		{80, false},
		{119, false},
		{120, true},
		{200, true},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("w%d", tc.w), func(t *testing.T) {
			m := newSizedFixture(t, tc.w, 30)
			v := m.View()
			if tc.split && !strings.Contains(v, "│") {
				t.Errorf("width %d should split (want divider), got:\n%s", tc.w, v)
			}
			if !tc.split && strings.Contains(v, "│") {
				t.Errorf("width %d should be full-width list (no divider), got:\n%s", tc.w, v)
			}
			if !strings.Contains(v, "#7") || !strings.Contains(v, "#8") {
				t.Errorf("width %d should still list issues, got:\n%s", tc.w, v)
			}
		})
	}
}

func TestLayout_ResizePreservesSelection(t *testing.T) {
	gh, _, m := newFixture(t)
	gh.Issues[8] = ports.Issue{Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"}
	m.width = 120
	m.height = 30
	m = press(t, m, "j") // move to #8
	// shrink to too-small then restore
	m = resizeWindow(t, m, 79, 24)
	m = resizeWindow(t, m, 120, 30)
	if m.cursor != 2 { // header(0) #7(1) #8(2) in default fixture
		t.Fatalf("cursor should be restored to #8, got %d", m.cursor)
	}
	found := false
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.HasPrefix(line, "> ") && strings.Contains(line, "#8") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("selection marker should be on #8 after resize:\n%s", m.View())
	}
}

func TestLayout_SummaryLine(t *testing.T) {
	m := newSizedFixture(t, 120, 30)
	v := m.View()
	for _, want := range []string{"レビュー待ち 2", "止まってる 1", "最近マージ 0", "実行中 0"} {
		if !strings.Contains(v, want) {
			t.Errorf("summary missing %q:\n%s", want, v)
		}
	}
}

func TestLayout_ColumnHeaderAndBody(t *testing.T) {
	m := newSizedFixture(t, 200, 40)
	v := m.View()
	for _, want := range []string{"Issue", "Title", "Updated", "Agent", "Tries", "PR"} {
		if !strings.Contains(v, want) {
			t.Errorf("column header missing %q:\n%s", want, v)
		}
	}
	if !strings.Contains(v, "#7") || !strings.Contains(v, "#8") || !strings.Contains(v, "#9") {
		t.Errorf("issues should be rendered:\n%s", v)
	}
	if !strings.Contains(v, "claude") {
		t.Errorf("agent column should render claude for blocked issue:\n%s", v)
	}
}

func TestLayout_UpdatedColumn(t *testing.T) {
	m := newSizedFixture(t, 120, 30)
	now := m.opts.Now()
	m.sections[0].Items[0].UpdatedAt = now.Add(-3 * time.Minute)
	v := m.View()
	if !strings.Contains(v, "3m") {
		t.Errorf("Updated column should show 3m, got:\n%s", v)
	}
}

func TestLayout_BodyHeightRespectsTerminal(t *testing.T) {
	m := newSizedFixture(t, 120, 24)
	v := m.View()
	lines := strings.Split(strings.TrimSuffix(v, "\n"), "\n")
	if len(lines) > 24 {
		t.Errorf("view has %d lines, want at most 24 for 24-high terminal:\n%s", len(lines), v)
	}
}

func TestLayout_MergedTruncationShowsPlus(t *testing.T) {
	var merged []ports.MergedIssue
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= MaxMergedItems+5; i++ {
		merged = append(merged, ports.MergedIssue{Number: i, Title: "done", MergedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	m := newSizedFixture(t, 120, 40)
	m.sections = Build(nil, nil, merged, &SeenState{LastSeenAt: now.Add(-100 * time.Hour)}, nil)
	m.cursor = 0
	m.scrollToCursor()
	v := m.View()
	if !strings.Contains(v, "20+") && !strings.Contains(v, "(20+)") {
		t.Errorf("truncated merged section should show 20+, got:\n%s", v)
	}
}

func TestLayout_StickySectionHeader(t *testing.T) {
	m := newSizedFixture(t, 120, 24)
	now := m.opts.Now()
	// fill review section past body height
	for i := 100; i < 120; i++ {
		m.sections[0].Items = append(m.sections[0].Items, Item{Number: i, Title: fmt.Sprintf("issue %d", i), UpdatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	m.cursor = len(m.visible()) - 1
	m.scrollToCursor()
	v := m.View()
	// The first body line should be a sticky copy of the review header.
	if !strings.Contains(v, "レビュー待ち (22)") {
		t.Errorf("sticky section header should stay visible, got:\n%s", v)
	}
}

func TestLayout_SplitPreview(t *testing.T) {
	m := newSizedFixture(t, 120, 30)
	m.detail = ports.Issue{Number: 7, Title: "spec: add warning", Body: "warns on zero", State: "OPEN"}
	v := m.View()
	if !strings.Contains(v, "warns on zero") {
		t.Errorf("split preview should show selected issue body:\n%s", v)
	}
}
