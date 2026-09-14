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

// logPaneEntries is the number of event log lines in the always-on log
// pane below the list (issue #196).
const logPaneEntries = 5

// logPaneHeight is the total rows the pane occupies: header + entries +
// closing rule. viewList reserves this from the body height.
const logPaneHeight = logPaneEntries + 2

// viewLogPane renders the always-on message/log pane: a header plus the
// latest entries, oldest first. Empty slots are blank so the pane keeps
// a constant height.
func (m Model) viewLogPane(w int) string {
	var b strings.Builder
	ch := "─"
	if m.theme != nil && m.theme.Mode() == ModePlain {
		ch = "-"
	}
	title := ch + " Log "
	header := title + strings.Repeat(ch, max(0, w-len([]rune(title))))
	if m.theme != nil {
		header = m.theme.Render(header, TokenFgSecondary, TokenBgCanvas, false, false, false)
	}
	b.WriteString(header + "\n")
	entries := m.logs
	if len(entries) > logPaneEntries {
		entries = entries[len(entries)-logPaneEntries:]
	}
	for _, e := range entries {
		line := padRight(truncTail(e, w), w)
		if m.theme != nil {
			line = m.theme.Render(line, TokenFgSecondary, TokenBgCanvas, false, false, false)
		}
		b.WriteString(line + "\n")
	}
	for i := len(entries); i < logPaneEntries; i++ {
		b.WriteString(padRight("", w) + "\n")
	}
	return b.String()
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
