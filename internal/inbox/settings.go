package inbox

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Issue creation mode constants.
const (
	IssueCreationDirect = "direct"
	IssueCreationAI     = "ai"
)

// Config represents the persisted user configuration for lead inbox.
type Config struct {
	Agent         string `json:"agent"`
	AgentMode     string `json:"agent_mode"`
	IssueCreation string `json:"issue_creation"`
}

// IssueCreation returns the current issue creation default ("direct" or "ai").
func (m Model) IssueCreation() string {
	if m.issueCreation == "" {
		return IssueCreationDirect
	}
	return m.issueCreation
}

func (m *Model) notifyConfigChange() {
	cfg := Config{
		Agent:         m.Agent(),
		AgentMode:     m.AgentMode(),
		IssueCreation: m.IssueCreation(),
	}
	if m.opts.OnSettingsChange != nil {
		m.opts.OnSettingsChange(cfg)
	}
	if m.opts.OnConfigChange != nil {
		m.opts.OnConfigChange(m.Agent(), m.AgentMode())
	}
}

func (m Model) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = modeList
		return m, nil
	case tea.KeyUp:
		m.settingsCursor = (m.settingsCursor - 1 + 3) % 3
		return m, nil
	case tea.KeyDown:
		m.settingsCursor = (m.settingsCursor + 1) % 3
		return m, nil
	case tea.KeyEnter, tea.KeySpace, tea.KeyRight:
		return m.cycleSetting(1)
	case tea.KeyLeft:
		return m.cycleSetting(-1)
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k":
			m.settingsCursor = (m.settingsCursor - 1 + 3) % 3
			return m, nil
		case "j":
			m.settingsCursor = (m.settingsCursor + 1) % 3
			return m, nil
		case "h":
			return m.cycleSetting(-1)
		case "l", " ":
			return m.cycleSetting(1)
		case "q", ",":
			m.mode = modeList
			return m, nil
		}
	}
	return m, nil
}

func (m Model) cycleSetting(direction int) (tea.Model, tea.Cmd) {
	switch m.settingsCursor {
	case 0: // Issue Creation
		if m.IssueCreation() == IssueCreationDirect {
			m.issueCreation = IssueCreationAI
		} else {
			m.issueCreation = IssueCreationDirect
		}
		m.notifyConfigChange()
		return m, m.setStatus(fmt.Sprintf("Issue creation default: %s", m.issueCreation))
	case 1: // Agent
		if direction >= 0 {
			m.agent = nextAgent(m.Agent())
		} else {
			// cycle backwards
			for i := 0; i < len(availableAgents)-1; i++ {
				m.agent = nextAgent(m.Agent())
			}
		}
		m.notifyConfigChange()
		return m, m.setStatus(fmt.Sprintf("Agent: %s", m.agent))
	case 2: // Agent Mode
		if direction >= 0 {
			m.agentMode = nextAgentMode(m.AgentMode())
		} else {
			for i := 0; i < len(availableAgentModes)-1; i++ {
				m.agentMode = nextAgentMode(m.AgentMode())
			}
		}
		m.notifyConfigChange()
		return m, m.setStatus(fmt.Sprintf("Agent mode: %s", m.agentMode))
	}
	return m, nil
}

func (m Model) viewSettings() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	boxWidth := 74
	if width < boxWidth+4 {
		boxWidth = width - 4
		if boxWidth < 40 {
			boxWidth = 40
		}
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#58A6FF"))
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E6EDF3"))
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#8B949E"))

	activeBadge := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(lipgloss.Color("#1F6FEB")).
		Padding(0, 1)
	inactiveBadge := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#8B949E")).
		Background(lipgloss.Color("#21262D")).
		Padding(0, 1)

	var b strings.Builder
	b.WriteString(titleStyle.Render("⚙ Lead Settings & Preferences"))
	b.WriteString("\n")
	b.WriteString(descStyle.Render("Configure workflow defaults. Changes are saved automatically."))
	b.WriteString("\n\n")

	// Setting 0: Issue Creation
	cursor0 := "  "
	if m.settingsCursor == 0 {
		cursor0 = "▸ "
	}
	b.WriteString(labelStyle.Render(cursor0 + "1. Issue Creation Default [s]"))
	b.WriteString("\n   ")
	if m.IssueCreation() == IssueCreationDirect {
		b.WriteString(activeBadge.Render("Direct (0.5s)") + "  " + inactiveBadge.Render("AI Spec (LLM)"))
	} else {
		b.WriteString(inactiveBadge.Render("Direct (0.5s)") + "  " + activeBadge.Render("AI Spec (LLM)"))
	}
	b.WriteString("\n   ")
	if m.IssueCreation() == IssueCreationDirect {
		b.WriteString(descStyle.Render("Enter: files GitHub issue immediately. Type !prefix for AI spec generation."))
	} else {
		b.WriteString(descStyle.Render("Enter: generates spec via AI agent. Type !prefix for direct issue creation."))
	}
	b.WriteString("\n\n")

	// Setting 1: Coding Agent
	cursor1 := "  "
	if m.settingsCursor == 1 {
		cursor1 = "▸ "
	}
	b.WriteString(labelStyle.Render(cursor1 + "2. Active Coding Agent"))
	b.WriteString("\n   ")
	var agentBadges []string
	for _, ag := range availableAgents {
		if ag == m.Agent() {
			agentBadges = append(agentBadges, activeBadge.Render(ag))
		} else {
			agentBadges = append(agentBadges, inactiveBadge.Render(ag))
		}
	}
	b.WriteString(strings.Join(agentBadges, " "))
	b.WriteString("\n   ")
	b.WriteString(descStyle.Render("Agent used for implementing issues and generating specifications."))
	b.WriteString("\n\n")

	// Setting 2: Agent Mode
	cursor2 := "  "
	if m.settingsCursor == 2 {
		cursor2 = "▸ "
	}
	b.WriteString(labelStyle.Render(cursor2 + "3. Agent Dispatch Mode"))
	b.WriteString("\n   ")
	var modeBadges []string
	for _, md := range availableAgentModes {
		if md == m.AgentMode() {
			modeBadges = append(modeBadges, activeBadge.Render(md))
		} else {
			modeBadges = append(modeBadges, inactiveBadge.Render(md))
		}
	}
	b.WriteString(strings.Join(modeBadges, " "))
	b.WriteString("\n   ")
	switch m.AgentMode() {
	case "batch":
		b.WriteString(descStyle.Render("Auto-dispatches in background when issues are approved to ready."))
	case "dangerous":
		b.WriteString(descStyle.Render("Runs in autonomous mode without interactive confirmation."))
	default:
		b.WriteString(descStyle.Render("Manual confirmation before dispatch."))
	}
	b.WriteString("\n\n")

	// Footer / Key bindings
	footerStyle := descStyle
	keyStyle := titleStyle
	b.WriteString(footerStyle.Render("Navigation: ") +
		keyStyle.Render("↑/k ↓/j") + footerStyle.Render(" Select item  ") +
		keyStyle.Render("Enter/Space/←/→") + footerStyle.Render(" Toggle/Cycle  ") +
		keyStyle.Render("Esc/q/,") + footerStyle.Render(" Back to list"))

	content := b.String()

	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#30363D")).
		Padding(1, 2).
		Width(boxWidth).
		Render(content)

	return lipgloss.Place(
		width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		box,
	)
}
