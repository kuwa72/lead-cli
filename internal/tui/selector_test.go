package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSelectorModel_NavigationAndSelection(t *testing.T) {
	issues := []IssueItem{
		{Number: 1, Title: "First"},
		{Number: 2, Title: "Second"},
	}
	m := selectorModel{
		issues: issues,
		ctx:    context.Background(),
	}

	// 1. Initial cursor is 0
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}

	// 2. Down key moves cursor to 1
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(selectorModel)
	if m.cursor != 1 {
		t.Fatalf("cursor after down = %d, want 1", m.cursor)
	}

	// 3. Enter key selects issue 2 for ActionWork
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(selectorModel)
	if cmd == nil {
		t.Error("enter should return Quit cmd")
	}
	if m.selection.IssueNumber != 2 || m.selection.Action != ActionWork {
		t.Errorf("selection = %+v, want issue 2 ActionWork", m.selection)
	}
}

func TestSelectorModel_BrowseKey(t *testing.T) {
	issues := []IssueItem{
		{Number: 42, Title: "Answer"},
	}
	m := selectorModel{
		issues: issues,
		ctx:    context.Background(),
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = next.(selectorModel)
	if m.selection.IssueNumber != 42 || m.selection.Action != ActionBrowse {
		t.Errorf("selection = %+v, want issue 42 ActionBrowse", m.selection)
	}
}

func TestSelectorModel_Abort(t *testing.T) {
	m := selectorModel{
		issues: []IssueItem{{Number: 1, Title: "Test"}},
		ctx:    context.Background(),
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(selectorModel)
	if !m.aborted {
		t.Error("esc should set aborted=true")
	}
}
