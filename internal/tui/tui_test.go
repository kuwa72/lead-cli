package tui

import (
	"context"
	"testing"
)

// TestFakeSelectorContract proves the non-TTY path: fixed selection,
// one simulated preview render, and abort propagation.
func TestFakeSelector_ReturnsFixedSelection(t *testing.T) {
	f := &FakeSelector{Selection: Selection{IssueNumber: 36, Agent: "agy", Action: ActionWork}}
	previewCalls := 0
	sel, err := f.SelectIssue(context.Background(),
		[]IssueItem{{Number: 36, Title: "x"}},
		func(ctx context.Context, number int) (string, error) {
			previewCalls++
			if number != 36 {
				t.Errorf("preview number = %d, want 36", number)
			}
			return "preview", nil
		})
	if err != nil {
		t.Fatalf("SelectIssue: %v", err)
	}
	if sel.IssueNumber != 36 || sel.Agent != "agy" || sel.Action != ActionWork {
		t.Errorf("Selection = %+v, want fixed work selection", sel)
	}
	if previewCalls != 1 {
		t.Errorf("preview calls = %d, want 1 (simulated render)", previewCalls)
	}
}

func TestFakeSelector_PropagatesAbort(t *testing.T) {
	f := &FakeSelector{Err: ErrAborted}
	_, err := f.SelectIssue(context.Background(), nil, nil)
	if err != ErrAborted {
		t.Errorf("SelectIssue = %v, want ErrAborted", err)
	}
}
