package inbox

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSettings_OpenAndClose(t *testing.T) {
	_, _, m := newFixture(t)
	if m.mode != modeList {
		t.Fatalf("expected modeList, got %v", m.mode)
	}

	// Press ',' to open settings
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{','}})
	m = newM.(Model)
	if m.mode != modeSettings {
		t.Fatalf("expected modeSettings after ',', got %v", m.mode)
	}

	view := m.View()
	if !strings.Contains(view, "Settings") {
		t.Fatalf("expected 'Settings' in view, got:\n%s", view)
	}
	if !strings.Contains(view, "Issue Creation") {
		t.Fatalf("expected 'Issue Creation' in view, got:\n%s", view)
	}

	// Press Esc to close settings
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newM.(Model)
	if m.mode != modeList {
		t.Fatalf("expected modeList after Esc, got %v", m.mode)
	}
}

func TestSettings_ToggleValuesAndNotify(t *testing.T) {
	var notifiedCfg Config
	notifyCount := 0
	_, _, m := newFixture(t)
	m.agent = "agy"
	m.agentMode = "batch"
	m.issueCreation = IssueCreationDirect
	m.opts.OnSettingsChange = func(cfg Config) {
		notifiedCfg = cfg
		notifyCount++
	}

	// Open settings with ','
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{','}})
	m = newM.(Model)

	// Cursor is at 0: Issue Creation. Press Enter to toggle to 'ai'
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newM.(Model)
	if m.IssueCreation() != IssueCreationAI {
		t.Fatalf("expected issueCreation to be 'ai', got %q", m.IssueCreation())
	}
	if notifyCount != 1 || notifiedCfg.IssueCreation != IssueCreationAI {
		t.Fatalf("expected notification with IssueCreation='ai', got %+v", notifiedCfg)
	}

	// Move down to Agent (cursor 1)
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newM.(Model)
	// Press Space to cycle agent
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = newM.(Model)
	if m.Agent() != "claude" {
		t.Fatalf("expected agent 'claude', got %q", m.Agent())
	}
	if notifiedCfg.Agent != "claude" {
		t.Fatalf("expected notification with Agent='claude', got %+v", notifiedCfg)
	}

	// Move down to Agent Mode (cursor 2)
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newM.(Model)
	// Press Enter to cycle agent mode
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newM.(Model)
	if m.AgentMode() != "dangerous" {
		t.Fatalf("expected agentMode 'dangerous', got %q", m.AgentMode())
	}
	if notifiedCfg.AgentMode != "dangerous" {
		t.Fatalf("expected notification with AgentMode='dangerous', got %+v", notifiedCfg)
	}
}

func TestSettings_IssueCreationDefaultAI_AffectsKeyS(t *testing.T) {
	gh, _, m := newFixture(t)
	var calls []sayCall
	m.opts.Say = fakeSay(&calls)
	m.issueCreation = IssueCreationAI

	// Normal text without ! -> should call Say (AI)
	m = press(t, m, "s", "text:build auth screen", "enter")
	if len(calls) != 1 || calls[0].OneLiner != "build auth screen" {
		t.Fatalf("expected Say to be called with 'build auth screen', got %+v", calls)
	}
	if len(gh.Created) != 0 {
		t.Fatalf("expected direct issue NOT to be created, got: %+v", gh.Created)
	}

	// Reset and type "!direct title" -> should call Gh.IssueCreate directly
	calls = nil
	gh.Created = nil
	m = press(t, m, "s", "text:!direct title", "enter")
	if len(calls) != 0 {
		t.Fatalf("expected Say NOT to be called with !, got %+v", calls)
	}
	if len(gh.Created) != 1 {
		t.Fatalf("expected direct issue created, got: %+v", gh.Created)
	}
	if gh.Created[0].Title != "direct title" {
		t.Fatalf("expected title='direct title', got %q", gh.Created[0].Title)
	}
}
