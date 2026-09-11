package inbox

import (
	"reflect"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

func TestFilterUnseenMerged(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	merged := []ports.MergedIssue{
		{Number: 1, Title: "old", MergedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "new", MergedAt: now.Add(-30 * time.Minute)},
		{Number: 3, Title: "confirmed", MergedAt: now.Add(-10 * time.Minute)},
		{Number: 4, Title: "exact", MergedAt: now.Add(-1 * time.Hour)},
	}
	since := now.Add(-1 * time.Hour)
	confirmed := map[int]bool{3: true}

	got := FilterUnseenMerged(merged, since, confirmed)
	want := []ports.MergedIssue{
		{Number: 2, Title: "new", MergedAt: now.Add(-30 * time.Minute)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterUnseenMerged = %+v, want %+v", got, want)
	}
}

func TestFilterUnseenMerged_SortsNewestFirst(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	merged := []ports.MergedIssue{
		{Number: 1, Title: "oldest", MergedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "newest", MergedAt: now.Add(-10 * time.Minute)},
		{Number: 3, Title: "middle", MergedAt: now.Add(-1 * time.Hour)},
	}

	got := FilterUnseenMerged(merged, time.Time{}, nil)
	want := []int{2, 3, 1}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, n := range want {
		if got[i].Number != n {
			t.Errorf("got[%d].Number = %d, want %d", i, got[i].Number, n)
		}
	}
}

func TestFilterUnseenMerged_ZeroSinceIncludesAllUnconfirmed(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	merged := []ports.MergedIssue{
		{Number: 1, Title: "old", MergedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "new", MergedAt: now.Add(-30 * time.Minute)},
	}

	got := FilterUnseenMerged(merged, time.Time{}, nil)
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestBuild_CapsMergedSection(t *testing.T) {
	var merged []ports.MergedIssue
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= MaxMergedItems+5; i++ {
		merged = append(merged, ports.MergedIssue{Number: i, Title: "merged", MergedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	got := Build(nil, nil, merged, &SeenState{LastSeenAt: now.Add(-100 * time.Hour)}, nil)
	if len(got[2].Items) != MaxMergedItems {
		t.Errorf("merged capped = %d, want %d", len(got[2].Items), MaxMergedItems)
	}
}

func TestBuild_FiltersWorkflowsByRepo(t *testing.T) {
	wfs := []state.Workflow{
		{Repository: "https://github.com/kuwa72/lead-cli.git", Issue: 83, Status: state.StatusInProgress, Branch: "issue/83-mode"},
		{Repository: "https://github.com/kuwa72/zenn.git", Issue: 15, Status: state.StatusInProgress, Branch: "issue/15-article"},
		{Repository: "", Issue: 99, Status: state.StatusInProgress, Branch: "issue/99-local"},
	}
	// With repo filter "kuwa72/lead-cli", zenn.git must be excluded, local/empty repo is kept.
	got := Build(nil, nil, nil, nil, wfs, BuildOptions{Repo: "kuwa72/lead-cli"})
	runningItems := got[3].Items
	if len(runningItems) != 2 {
		t.Fatalf("running count = %d, want 2 (zenn.git excluded)", len(runningItems))
	}
	if runningItems[0].Number != 83 || runningItems[1].Number != 99 {
		t.Errorf("running items = %+v, want [83, 99]", runningItems)
	}
}

func TestBuild_FiltersClosedIssueWorkflows(t *testing.T) {
	wfs := []state.Workflow{
		{Issue: 36, Status: state.StatusInProgress, Branch: "issue/36-closed"},
		{Issue: 89, Status: state.StatusInProgress, Branch: "issue/89-open"},
	}
	openSet := map[int]bool{89: true} // 36 is closed on GitHub
	got := Build(nil, nil, nil, nil, wfs, BuildOptions{OpenNumbers: openSet})
	runningItems := got[3].Items
	if len(runningItems) != 1 {
		t.Fatalf("running count = %d, want 1 (issue 36 excluded because closed)", len(runningItems))
	}
	if runningItems[0].Number != 89 {
		t.Errorf("running item = %+v, want issue 89", runningItems[0])
	}
}

func TestBuild_IncludesBacklogSection(t *testing.T) {
	review := []ports.IssueSummary{{Number: 1, Title: "rev"}}
	blocked := []ports.IssueSummary{{Number: 2, Title: "blk"}}
	open := []ports.IssueSummary{
		{Number: 1, Title: "rev"},
		{Number: 2, Title: "blk"},
		{Number: 3, Title: "backlog 1"},
		{Number: 4, Title: "backlog 2"},
	}
	sections := Build(review, blocked, nil, nil, nil, BuildOptions{
		OpenIssues: open,
	})
	if len(sections) != 5 {
		t.Fatalf("expected 5 sections, got %d", len(sections))
	}
	backlog := sections[4]
	if backlog.Kind != KindBacklog {
		t.Errorf("section[4] kind = %v, want KindBacklog", backlog.Kind)
	}
	if !backlog.Collapsed {
		t.Errorf("backlog section should be collapsed by default")
	}
	if len(backlog.Items) != 2 {
		t.Fatalf("backlog items = %d, want 2", len(backlog.Items))
	}
	if backlog.Items[0].Number != 3 || backlog.Items[1].Number != 4 {
		t.Errorf("backlog items = %+v, want [3, 4]", backlog.Items)
	}
}

