// Package inbox is lead's default screen (issue #91, docs/rfc-inbox-ux.md §5):
// the human-only work queue, sectioned by GitHub label plus local state.
//
// Sectioning is a pure function (Build) so the dummy-`gh` acceptance test
// in the RFC can assert it without a terminal. The bubbletea model in
// inbox.go maps single keys to exact gh calls; headless.go drives it
// without a TTY (unit tests and the LEAD_TEST_INBOX_KEYS hook).
package inbox

import (
	"strings"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

// State labels are fixed (RFC §13): one state label per issue, swapped by lead.
const (
	LabelNeedsReview = "needs-review"
	LabelReady       = "ready"
	LabelBlocked     = "blocked"
)

// Kind identifies one inbox section.
type Kind int

// Sections in display order: human work first (RFC §5.1).
const (
	KindNeedsReview Kind = iota
	KindBlocked
	KindMerged
	KindRunning
)

// Title is the section heading shown on screen.
func (k Kind) Title() string {
	switch k {
	case KindNeedsReview:
		return "レビュー待ち"
	case KindBlocked:
		return "止まってる"
	case KindMerged:
		return "最近マージ"
	case KindRunning:
		return "実行中"
	}
	return "?"
}

// IsIssueQueue reports whether items in this section accept the
// approve/reject keys (only label-driven sections do).
func (k Kind) IsIssueQueue() bool { return k == KindNeedsReview || k == KindBlocked }

// Item is one row. Number/Title come from gh; the rest from workflows.json.
type Item struct {
	Number   int
	Title    string
	Kind     Kind
	Agent    string
	Attempts int
	PID      int
	LogPath  string
	Pane     string
	Branch   string
}

// Section is one heading plus its rows.
type Section struct {
	Kind      Kind
	Items     []Item
	Collapsed bool
}

// Build sections issues: `needs-review` and `blocked` come from gh label
// listings (an issue carrying both stays in needs-review only); "merged" and
// "running" are derived locally from workflows.json and never from labels
// (RFC §13.1: a forgotten label must not make the inbox lie).
func Build(needsReview, blocked []ports.IssueSummary, wfs []state.Workflow) []Section {
	byIssue := map[int]state.Workflow{}
	for _, w := range wfs {
		if w.Part == "" {
			byIssue[w.Issue] = w
		} else if _, ok := byIssue[w.Issue]; !ok {
			byIssue[w.Issue] = w
		}
	}
	enrich := func(it Item) Item {
		if w, ok := byIssue[it.Number]; ok {
			it.Agent, it.Attempts, it.PID, it.LogPath, it.Pane, it.Branch = w.Agent, w.Attempts, w.PID, w.LogPath, w.Pane, w.Branch
		}
		return it
	}

	review := Section{Kind: KindNeedsReview}
	seen := map[int]bool{}
	for _, s := range needsReview {
		if seen[s.Number] {
			continue
		}
		seen[s.Number] = true
		review.Items = append(review.Items, enrich(Item{Number: s.Number, Title: s.Title, Kind: KindNeedsReview}))
	}
	stuck := Section{Kind: KindBlocked}
	for _, s := range blocked {
		if seen[s.Number] {
			continue
		}
		seen[s.Number] = true
		stuck.Items = append(stuck.Items, enrich(Item{Number: s.Number, Title: s.Title, Kind: KindBlocked}))
	}

	merged := Section{Kind: KindMerged, Collapsed: true}
	running := Section{Kind: KindRunning, Collapsed: true}
	for _, w := range wfs {
		it := Item{Number: w.Issue, Title: strings.TrimSpace(w.Branch), Agent: w.Agent, Attempts: w.Attempts, PID: w.PID, LogPath: w.LogPath, Pane: w.Pane, Branch: w.Branch}
		switch w.Status {
		case state.StatusCompleted, state.StatusClosed:
			it.Kind = KindMerged
			merged.Items = append(merged.Items, it)
		case state.StatusInProgress:
			it.Kind = KindRunning
			running.Items = append(running.Items, it)
		}
	}
	return []Section{review, stuck, merged, running}
}
