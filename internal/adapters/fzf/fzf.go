// Package fzf implements tui.Selector with embedded go-fzf (issue #37).
// No external fzf command is spawned; the picker runs in-process on the TTY.
//
// Design note (deviation from RFC-16 §3.1 one-stroke selection): go-fzf's
// Find reports selected indexes but not WHICH key chose them, so agent and
// browser dispatch cannot ride distinct keys in one screen. Selection is
// therefore two short steps — issue (with live preview), then action
// (agent work vs. browser open). A single-screen custom model (bubbles/list)
// is the follow-up if one-stroke returns.
package fzf

import (
	"context"
	"errors"
	"fmt"

	gofzf "github.com/koki-develop/go-fzf"

	"github.com/kuwa72/lead-cli/internal/adapters/agent"
	"github.com/kuwa72/lead-cli/internal/tui"
)

// browseEntry is the last row of the action picker.
const browseEntry = "open in browser"

// GoFzfSelector is the interactive tui.Selector. Construct lazily
// (New only validates options; the TTY is touched in Find).
type GoFzfSelector struct{}

var _ tui.Selector = (*GoFzfSelector)(nil)

// New returns the interactive selector.
func New() *GoFzfSelector { return &GoFzfSelector{} }

// SelectIssue runs the two-step picker: issue list with live preview,
// then agent/browser action.
func (s *GoFzfSelector) SelectIssue(ctx context.Context, issues []tui.IssueItem, preview tui.PreviewFunc) (tui.Selection, error) {
	if len(issues) == 0 {
		return tui.Selection{}, fmt.Errorf("no issues to select")
	}
	rows := make([]string, len(issues))
	for i, iss := range issues {
		rows[i] = fmt.Sprintf("#%d %s", iss.Number, iss.Title)
	}
	finder, err := gofzf.New(gofzf.WithPrompt("Issue> "))
	if err != nil {
		return tui.Selection{}, err
	}
	idxs, err := finder.Find(rows, func(i int) string { return rows[i] },
		gofzf.WithPreviewWindow(func(i, _, _ int) string {
			if preview == nil {
				return ""
			}
			text, err := preview(ctx, issues[i].Number)
			if err != nil {
				return fmt.Sprintf("preview unavailable: %v", err)
			}
			return text
		}))
	if err != nil {
		return tui.Selection{}, translateErr(err)
	}
	if len(idxs) == 0 {
		return tui.Selection{}, tui.ErrAborted
	}
	chosen := issues[idxs[0]]

	actions := make([]string, 0, len(agent.KnownAgents)+1)
	for _, name := range agent.KnownAgents {
		actions = append(actions, fmt.Sprintf("%s — work with %s", name, name))
	}
	actions = append(actions, browseEntry)
	actionFinder, err := gofzf.New(gofzf.WithPrompt(fmt.Sprintf("#%d> ", chosen.Number)))
	if err != nil {
		return tui.Selection{}, err
	}
	picked, err := actionFinder.Find(actions, func(i int) string { return actions[i] })
	if err != nil {
		return tui.Selection{}, translateErr(err)
	}
	if len(picked) == 0 {
		return tui.Selection{}, tui.ErrAborted
	}
	if picked[0] == len(actions)-1 {
		return tui.Selection{IssueNumber: chosen.Number, Action: tui.ActionBrowse}, nil
	}
	return tui.Selection{
		IssueNumber: chosen.Number,
		Agent:       agent.KnownAgents[picked[0]],
		Action:      tui.ActionWork,
	}, nil
}

func translateErr(err error) error {
	if errors.Is(err, gofzf.ErrAbort) {
		return tui.ErrAborted
	}
	return err
}
