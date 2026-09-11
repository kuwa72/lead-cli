// Package inbox is lead's default screen (issue #91, docs/rfc-inbox-ux.md §5):
// the human-only work queue, sectioned by GitHub label plus local state.
//
// Sectioning is a pure function (Build) so the dummy-`gh` acceptance test
// in the RFC can assert it without a terminal. The bubbletea model in
// inbox.go maps single keys to exact gh calls; headless.go drives it
// without a TTY (unit tests and the LEAD_TEST_INBOX_KEYS hook).
package inbox

import (
	"sort"
	"strings"
	"time"

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
	KindBacklog
)

// Title is the section heading shown on screen.
func (k Kind) Title() string {
	switch k {
	case KindNeedsReview:
		return "Needs review"
	case KindBlocked:
		return "Blocked"
	case KindMerged:
		return "Merged"
	case KindRunning:
		return "Running"
	case KindBacklog:
		return "Backlog"
	}
	return "?"
}

// IsIssueQueue reports whether items in this section accept the
// approve/reject keys (only label-driven sections do).
func (k Kind) IsIssueQueue() bool { return k == KindNeedsReview || k == KindBlocked || k == KindBacklog }

// MaxMergedItems caps the "Merged" section to keep the inbox readable.
const MaxMergedItems = 20

// Item is one row. Number/Title come from gh; the rest from workflows.json.
type Item struct {
	Number    int
	Title     string
	UpdatedAt time.Time
	Kind      Kind
	Agent     string
	Attempts  int
	PID       int
	LogPath   string
	Pane      string
	Branch    string
	PRNumber  int
	MergedAt  time.Time
}

// Section is one heading plus its rows.
type Section struct {
	Kind      Kind
	Items     []Item
	Collapsed bool
	// Truncated is true when Items was capped at a display limit (e.g.
	// MaxMergedItems); the count is rendered as "20+" so the cap is visible.
	Truncated bool
}

// BuildOptions configures filtering and context for Build.
type BuildOptions struct {
	Repo        string               // "owner/repo" slug; if set, wfs from other repositories are excluded
	OpenNumbers map[int]bool         // set of open issue numbers; if non-nil, issues not in this set are excluded from Running
	OpenIssues  []ports.IssueSummary // open issues in repository for the Backlog section
}

// Build sections issues: `needs-review` and `blocked` come from gh label
// listings (an issue carrying both stays in needs-review only); `merged`
// comes from `ListMergedSince` filtered by the read-state; `running` is
// derived locally from workflows.json (RFC §13.1); `backlog` contains
// other open issues without lead state labels.
func Build(needsReview, blocked []ports.IssueSummary, merged []ports.MergedIssue, seen *SeenState, wfs []state.Workflow, opts ...BuildOptions) []Section {
	var bo BuildOptions
	if len(opts) > 0 {
		bo = opts[0]
	}
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
			it.Agent, it.Attempts, it.PID, it.LogPath, it.Pane, it.Branch, it.PRNumber = w.Agent, w.Attempts, w.PID, w.LogPath, w.Pane, w.Branch, firstPRNumber(w.PullRequests)
		}
		return it
	}

	review := Section{Kind: KindNeedsReview}
	seenNumbers := map[int]bool{}
	for _, s := range needsReview {
		if seenNumbers[s.Number] {
			continue
		}
		seenNumbers[s.Number] = true
		review.Items = append(review.Items, enrich(Item{Number: s.Number, Title: s.Title, Kind: KindNeedsReview, UpdatedAt: s.UpdatedAt}))
	}
	stuck := Section{Kind: KindBlocked}
	for _, s := range blocked {
		if seenNumbers[s.Number] {
			continue
		}
		seenNumbers[s.Number] = true
		stuck.Items = append(stuck.Items, enrich(Item{Number: s.Number, Title: s.Title, Kind: KindBlocked, UpdatedAt: s.UpdatedAt}))
	}

	confirmed := make(map[int]bool)
	lastSeen := time.Time{}
	if seen != nil {
		confirmed = seen.ConfirmedSet()
		lastSeen = seen.LastSeenAt
	}
	mergedSection := Section{Kind: KindMerged, Collapsed: true}
	for _, it := range FilterUnseenMerged(merged, lastSeen, confirmed) {
		mergedSection.Items = append(mergedSection.Items, enrich(Item{
			Number:   it.Number,
			Title:    it.Title,
			Kind:     KindMerged,
			MergedAt: it.MergedAt,
		}))
	}
	if len(mergedSection.Items) > MaxMergedItems {
		mergedSection.Items = mergedSection.Items[:MaxMergedItems]
		mergedSection.Truncated = true
	}

	running := Section{Kind: KindRunning, Collapsed: true}
	for _, w := range wfs {
		if w.Status != state.StatusInProgress {
			continue
		}
		if bo.Repo != "" && w.Repository != "" && w.Repository != "local" {
			if slug := RepoSlug(w.Repository); slug != "" && slug != bo.Repo {
				continue
			}
		}
		if bo.OpenNumbers != nil && !bo.OpenNumbers[w.Issue] {
			continue
		}
		it := Item{Number: w.Issue, Title: strings.TrimSpace(w.Branch), Agent: w.Agent, Attempts: w.Attempts, PID: w.PID, LogPath: w.LogPath, Pane: w.Pane, Branch: w.Branch, PRNumber: firstPRNumber(w.PullRequests), Kind: KindRunning}
		running.Items = append(running.Items, it)
	}

	backlogSection := Section{Kind: KindBacklog, Collapsed: true}
	for _, s := range bo.OpenIssues {
		if seenNumbers[s.Number] {
			continue
		}
		seenNumbers[s.Number] = true
		backlogSection.Items = append(backlogSection.Items, enrich(Item{
			Number:    s.Number,
			Title:     s.Title,
			Kind:      KindBacklog,
			UpdatedAt: s.UpdatedAt,
		}))
	}

	return []Section{review, stuck, mergedSection, running, backlogSection}
}

func firstPRNumber(prs []state.PRRef) int {
	if len(prs) == 0 {
		return 0
	}
	return prs[0].Number
}

// FilterUnseenMerged returns merged issues with MergedAt after lastSeen and
// whose number is not in confirmed, sorted newest-first.
func FilterUnseenMerged(merged []ports.MergedIssue, lastSeen time.Time, confirmed map[int]bool) []ports.MergedIssue {
	out := make([]ports.MergedIssue, 0, len(merged))
	for _, it := range merged {
		if confirmed != nil && confirmed[it.Number] {
			continue
		}
		if !lastSeen.IsZero() && !it.MergedAt.After(lastSeen) {
			continue
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MergedAt.After(out[j].MergedAt) })
	return out
}
