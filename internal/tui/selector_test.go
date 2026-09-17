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

// issue #214: the picker reports the configured default agent (e.g. the
// inbox settings pick) for ActionWork, resolving to agy when unset.
func TestSelectorModel_DefaultAgent(t *testing.T) {
	issues := []IssueItem{{Number: 1, Title: "First"}}

	m := selectorModel{issues: issues, ctx: context.Background(), defaultAgent: "claude"}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(selectorModel)
	if m.selection.Agent != "claude" {
		t.Errorf("selection.Agent = %q, want configured default claude", m.selection.Agent)
	}

	m = selectorModel{issues: issues, ctx: context.Background()}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(selectorModel)
	if m.selection.Agent != "agy" {
		t.Errorf("selection.Agent = %q, want agy fallback", m.selection.Agent)
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
