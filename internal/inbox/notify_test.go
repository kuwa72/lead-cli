package inbox

import (
	"path/filepath"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

type fakeNotifier struct {
	calls []notificationCall
}

type notificationCall struct {
	title   string
	message string
}

func (f *fakeNotifier) Notify(title, message string) error {
	f.calls = append(f.calls, notificationCall{title: title, message: message})
	return nil
}

func TestInbox_NotifyOnNewNeedsReviewAndBlocked(t *testing.T) {
	gh := &testutil.FakeGhClient{
		Labeled: map[string][]ports.IssueSummary{
			LabelNeedsReview: {{Number: 1, Title: "first"}},
		},
		Issues: map[int]ports.Issue{
			1: {Number: 1, Title: "first", State: "OPEN"},
			2: {Number: 2, Title: "second ready", State: "OPEN"},
			3: {Number: 3, Title: "third stuck", State: "OPEN"},
		},
	}
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	store := &state.Store{Path: stateFile}

	notifier := &fakeNotifier{}
	m := New(Options{
		Gh:           gh,
		Store:        store,
		PreviewDelay: 0,
		Headless:     true,
		Notifier:     notifier,
	})

	// Initial load: Issue 1 is already needs-review, should not trigger notification.
	m, _ = Drain(m, m.Init())
	if len(notifier.calls) != 0 {
		t.Fatalf("expected 0 notifications on initial load, got %d", len(notifier.calls))
	}

	// Next update: Issue 2 becomes needs-review, Issue 3 becomes blocked.
	gh.Labeled[LabelNeedsReview] = []ports.IssueSummary{
		{Number: 1, Title: "first"},
		{Number: 2, Title: "second ready"},
	}
	gh.Labeled[LabelBlocked] = []ports.IssueSummary{
		{Number: 3, Title: "third stuck"},
	}

	m, _ = Drain(m, m.loadCmd())

	if len(notifier.calls) != 2 {
		t.Fatalf("expected 2 notifications on update, got %d (%+v)", len(notifier.calls), notifier.calls)
	}

	hasReview := false
	hasBlocked := false
	for _, c := range notifier.calls {
		if c.title == "Needs Review: #2 second ready" {
			hasReview = true
		}
		if c.title == "Agent Blocked: #3 third stuck" {
			hasBlocked = true
		}
	}
	if !hasReview {
		t.Errorf("expected notification for Needs Review #2, got: %+v", notifier.calls)
	}
	if !hasBlocked {
		t.Errorf("expected notification for Blocked #3, got: %+v", notifier.calls)
	}
}
