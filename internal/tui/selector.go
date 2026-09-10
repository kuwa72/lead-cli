package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kuwa72/lead-cli/internal/adapters/agent"
)

type selectorModel struct {
	issues    []IssueItem
	cursor    int
	previewFn PreviewFunc
	ctx       context.Context
	selection Selection
	aborted   bool
	width     int
	height    int
	preview   string
}

func (m selectorModel) Init() tea.Cmd {
	return m.fetchPreviewCmd()
}

func (m selectorModel) fetchPreviewCmd() tea.Cmd {
	if m.previewFn == nil || len(m.issues) == 0 {
		return nil
	}
	num := m.issues[m.cursor].Number
	return func() tea.Msg {
		text, err := m.previewFn(m.ctx, num)
		if err != nil {
			return previewMsg{num: num, text: fmt.Sprintf("preview error: %v", err)}
		}
		return previewMsg{num: num, text: text}
	}
}

type previewMsg struct {
	num  int
	text string
}

func (m selectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case previewMsg:
		if len(m.issues) > 0 && m.issues[m.cursor].Number == msg.num {
			m.preview = msg.text
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.aborted = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				return m, m.fetchPreviewCmd()
			}
		case "down", "j":
			if m.cursor < len(m.issues)-1 {
				m.cursor++
				return m, m.fetchPreviewCmd()
			}
		case "enter":
			if len(m.issues) > 0 {
				m.selection = Selection{
					IssueNumber: m.issues[m.cursor].Number,
					Agent:       agent.Resolve(""),
					Action:      ActionWork,
				}
			}
			return m, tea.Quit
		case "o":
			if len(m.issues) > 0 {
				m.selection = Selection{
					IssueNumber: m.issues[m.cursor].Number,
					Action:      ActionBrowse,
				}
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m selectorModel) View() string {
	if m.aborted {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Select an issue (Enter: work, o: browse, Esc/q: cancel):\n\n")
	for i, iss := range m.issues {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		sb.WriteString(fmt.Sprintf("%s#%d %s\n", cursor, iss.Number, iss.Title))
	}
	if m.preview != "" {
		sb.WriteString("\nPreview:\n")
		sb.WriteString(m.preview)
		sb.WriteString("\n")
	}
	return sb.String()
}

// BubbleteaSelector implements Selector using a Bubble Tea model.
type BubbleteaSelector struct{}

var _ Selector = (*BubbleteaSelector)(nil)

// NewBubbleteaSelector returns a new BubbleteaSelector.
func NewBubbleteaSelector() *BubbleteaSelector {
	return &BubbleteaSelector{}
}

func (s *BubbleteaSelector) SelectIssue(ctx context.Context, issues []IssueItem, preview PreviewFunc) (Selection, error) {
	if len(issues) == 0 {
		return Selection{}, fmt.Errorf("no issues to select")
	}
	m := selectorModel{
		issues:    issues,
		previewFn: preview,
		ctx:       ctx,
	}
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return Selection{}, err
	}
	res := finalModel.(selectorModel)
	if res.aborted {
		return Selection{}, ErrAborted
	}
	return res.selection, nil
}
