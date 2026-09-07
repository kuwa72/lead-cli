// Package tui defines the embedded issue-picker contract (issue #37).
// The terminal UI is a go-fzf adapter (internal/adapters/fzf); all logic
// here is TTY-free and covered by headless tests with FakeSelector.
package tui

import (
	"context"
	"errors"
)

// Action is what to do with the chosen issue.
type Action string

const (
	// ActionWork starts/resumes work with an agent.
	ActionWork Action = "work"
	// ActionBrowse opens the issue in a browser.
	ActionBrowse Action = "browse"
)

// Selection is one picker outcome.
type Selection struct {
	IssueNumber int
	Agent       string // resolved agent for ActionWork
	Action      Action
}

// IssueItem is one pickable row.
type IssueItem struct {
	Number int
	Title  string
}

// PreviewFunc renders preview text for an issue (cache-backed).
type PreviewFunc func(ctx context.Context, number int) (string, error)

// ErrAborted is returned when the user cancels the picker (Esc/Ctrl-C).
var ErrAborted = errors.New("selection aborted")

// Selector picks an issue. Implementations: go-fzf (interactive) or
// FakeSelector (tests, LEAD_TEST_SELECTION).
type Selector interface {
	SelectIssue(ctx context.Context, issues []IssueItem, preview PreviewFunc) (Selection, error)
}

// FakeSelector returns a fixed selection for non-TTY tests. It invokes
// preview once for the selected issue to simulate the preview render
// (which exercises the XDG cache through the same path).
type FakeSelector struct {
	Selection Selection
	Err       error
}

var _ Selector = (*FakeSelector)(nil)

// SelectIssue returns the fixed selection (or Err).
func (f *FakeSelector) SelectIssue(ctx context.Context, issues []IssueItem, preview PreviewFunc) (Selection, error) {
	if f.Err != nil {
		return Selection{}, f.Err
	}
	if preview != nil {
		if _, err := preview(ctx, f.Selection.IssueNumber); err != nil {
			return Selection{}, err
		}
	}
	return f.Selection, nil
}
