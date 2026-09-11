package inbox

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Logs returns a copy of the event log entries.
func (m Model) Logs() []string {
	cp := make([]string, len(m.logs))
	copy(cp, m.logs)
	return cp
}

func (m *Model) addLog(entry string) {
	ts := m.now().Format("15:04:05")
	m.logs = append(m.logs, fmt.Sprintf("[%s] %s", ts, entry))
	if len(m.logs) > 200 {
		m.logs = m.logs[len(m.logs)-200:]
	}
}

func (m Model) updateLog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = modeList
		return m, nil
	case tea.KeyUp:
		if m.logOffset > 0 {
			m.logOffset--
		}
		return m, nil
	case tea.KeyDown:
		if m.logOffset < len(m.logs)-1 {
			m.logOffset++
		}
		return m, nil
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "q", "L":
			m.mode = modeList
			return m, nil
		case "k":
			if m.logOffset > 0 {
				m.logOffset--
			}
			return m, nil
		case "j":
			if m.logOffset < len(m.logs)-1 {
				m.logOffset++
			}
			return m, nil
		case "?":
			m.helpReturnMode = modeLog
			m.mode = modeHelp
			return m, nil
		}
	}
	return m, nil
}

func (m Model) viewLog() string {
	var b strings.Builder
	w := m.screenWidth()
	b.WriteString(m.headerLine(w) + "\n")
	b.WriteString(padRight("Event Log & Status History", w) + "\n")
	b.WriteString(m.ruleW(w) + "\n")

	footerStr := "[↑/↓] Scroll   [Esc/q/L] Back to list"
	footerLines := strings.Count(footerStr, "\n") + 1
	bodyH := m.bodyHeightFor(footerLines)
	if bodyH < 1 {
		bodyH = 10
	}

	if len(m.logs) == 0 {
		b.WriteString(padRight("No log entries yet.", w) + "\n")
		for i := 1; i < bodyH; i++ {
			b.WriteString(padRight("", w) + "\n")
		}
	} else {
		start := m.logOffset
		if start > len(m.logs)-bodyH {
			start = max(0, len(m.logs)-bodyH)
		}
		rendered := 0
		for i := start; i < len(m.logs) && rendered < bodyH; i++ {
			line := padRight(truncTail(m.logs[i], w), w)
			if m.theme != nil {
				line = m.theme.Render(line, TokenFgPrimary, TokenBgCanvas, false, false, false)
			}
			b.WriteString(line + "\n")
			rendered++
		}
		for rendered < bodyH {
			b.WriteString(padRight("", w) + "\n")
			rendered++
		}
	}

	b.WriteString(m.ruleW(w) + "\n")
	if m.theme != nil {
		footerStr = m.theme.Render(footerStr, TokenFgSecondary, TokenBgCanvas, false, false, false)
	}
	b.WriteString(padRight(footerStr, w) + "\n")
	return b.String()
}
