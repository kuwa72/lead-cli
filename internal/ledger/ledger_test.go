package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	start := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
	rec := Record{
		Repo: "o/r", Issue: 274, Agent: "devin", Mode: "implement",
		StartedAt: start, EndedAt: start.Add(90 * time.Minute),
		Outcome: OutcomeCompleted, Attempts: 1, PR: 282, Scenarios: "2/2",
	}
	if err := Append(path, rec); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	if got[0].Issue != 274 || got[0].Outcome != OutcomeCompleted || got[0].Scenarios != "2/2" {
		t.Fatalf("record = %+v", got[0])
	}
}

func TestReadMissingFileIsEmpty(t *testing.T) {
	recs, err := Read(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil || len(recs) != 0 {
		t.Fatalf("recs=%v err=%v, want empty", recs, err)
	}
}

func TestReadSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	body := `{"issue":1,"outcome":"completed"}
not json
{"issue":2,"outcome":"blocked"}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Issue != 1 || recs[1].Issue != 2 {
		t.Fatalf("recs = %+v", recs)
	}
}

func TestAggregateSuccessRate(t *testing.T) {
	recs := []Record{
		{Outcome: OutcomeCompleted},
		{Outcome: OutcomeCompleted},
		{Outcome: OutcomeBlocked},
	}
	s := Aggregate(recs)
	if s.Runs != 3 || s.Completed != 2 || s.Failed != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if got := s.SuccessRate; got < 0.666 || got > 0.667 {
		t.Fatalf("success rate = %v, want ~0.667", got)
	}
	if !strings.Contains(s.String(), "success rate: 66.7%") {
		t.Fatalf("String() = %q", s.String())
	}
}

func TestAggregateEmpty(t *testing.T) {
	s := Aggregate(nil)
	if s.Runs != 0 || s.SuccessRate != 0 {
		t.Fatalf("stats = %+v, want zeros", s)
	}
	if !strings.Contains(s.String(), "success rate: 0.0%") {
		t.Fatalf("String() = %q", s.String())
	}
}

func TestAggregateMedianAndScenarios(t *testing.T) {
	base := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	recs := []Record{
		{StartedAt: base, EndedAt: base.Add(10 * time.Minute), Outcome: OutcomeCompleted, Scenarios: "1/2"},
		{StartedAt: base, EndedAt: base.Add(30 * time.Minute), Outcome: OutcomeCompleted, Scenarios: "2/2"},
		{StartedAt: base, EndedAt: base.Add(20 * time.Minute), Outcome: OutcomePostMergeFailed},
	}
	s := Aggregate(recs)
	if s.MedianTime != 20*time.Minute {
		t.Fatalf("median = %v, want 20m", s.MedianTime)
	}
	if s.Scenarios != 3 || s.ScenarioAll != 4 {
		t.Fatalf("scenarios = %d/%d, want 3/4", s.Scenarios, s.ScenarioAll)
	}
	if s.Reverts != 1 {
		t.Fatalf("reverts = %d, want 1", s.Reverts)
	}
}

func TestPathIsNextToStateFile(t *testing.T) {
	if got := Path("/home/u/.local/state/lead/workflows.json"); got != "/home/u/.local/state/lead/runs.jsonl" {
		t.Fatalf("Path = %q", got)
	}
}
