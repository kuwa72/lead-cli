package inbox

import (
	"reflect"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
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
