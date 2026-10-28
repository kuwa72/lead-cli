// Package ledger appends one line per agent run to runs.jsonl and
// aggregates them for `lead stats` (issue #274).
//
// GitHub stays the source of truth for issues and PRs; workflows.json only
// holds live work. This file is the history workflows.json deletes on
// completion: what ran, how long it took, and how it ended.
package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Phases of a record. Runs are attempts; merge events describe what finish
// did with the PR and never count as another attempt.
const (
	PhaseRun   = "run"
	PhaseMerge = "merge"
)

// Outcomes recorded by dispatch and finish.
const (
	OutcomeCompleted       = "completed"
	OutcomeRetry           = "retry"
	OutcomeBlocked         = "blocked"
	OutcomeGuardrail       = "guardrail"
	OutcomeAwaitingHuman   = "awaiting_human"
	OutcomePostMergeFailed = "postmerge-failed"
	OutcomeError           = "error"
	OutcomePaused          = "paused"
)

// Record is one run. Fields that a caller cannot know stay empty.
type Record struct {
	Repo      string    `json:"repo,omitempty"`
	Issue     int       `json:"issue"`
	// Phase separates the dispatch run ("run", default) from the merge
	// event ("merge"): both are appended, but only runs count as attempts.
	Phase     string    `json:"phase,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Mode      string    `json:"mode,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Outcome   string    `json:"outcome"`
	Attempts  int       `json:"attempts,omitempty"`
	PR        int       `json:"pr,omitempty"`
	Review    string    `json:"review,omitempty"`
	// Scenarios is "passed/total" (e.g. "3/4"); empty when none ran.
	Scenarios string `json:"scenarios,omitempty"`
	HeadSHA   string `json:"head_sha,omitempty"`
	DiffLines int    `json:"diff_lines,omitempty"`
	Files     int    `json:"files,omitempty"`
	RevertOf  string `json:"revert_of,omitempty"`
}

// Path returns the ledger next to the state file.
func Path(stateFile string) string {
	return filepath.Join(filepath.Dir(stateFile), "runs.jsonl")
}

// Append writes rec as one JSON line. The file is append-only.
func Append(path string, rec Record) error {
	if rec.EndedAt.IsZero() {
		rec.EndedAt = time.Now()
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("ledger: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ledger: dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("ledger: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("ledger: write: %w", err)
	}
	return nil
}

// Read returns every parsable record. Malformed lines are skipped: a
// half-written line must not make the whole history unreadable.
func Read(path string) ([]Record, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: open: %w", err)
	}
	defer f.Close()

	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		recs = append(recs, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ledger: read: %w", err)
	}
	return recs, nil
}

// Stats is the aggregate `lead stats` prints.
type Stats struct {
	Runs        int           `json:"runs"`
	Completed   int           `json:"completed"`
	Failed      int           `json:"failed"`
	Awaiting    int           `json:"awaiting_human"`
	SuccessRate float64       `json:"success_rate"`
	MedianTime  time.Duration `json:"-"`
	MedianSecs  float64       `json:"median_duration_seconds"`
	Reverts     int           `json:"reverts"`
	Scenarios   int           `json:"scenarios_passed"`
	ScenarioAll int           `json:"scenarios_total"`
}

// Aggregate folds records into Stats. Empty input yields zeros (never NaN).
func Aggregate(recs []Record) Stats {
	var s Stats
	var durations []time.Duration
	for _, r := range recs {
		if r.Phase != "" && r.Phase != PhaseRun {
			// A merge event: contributes scenarios/reverts, not attempts.
			if pass, total, ok := parseScenarios(r.Scenarios); ok {
				s.Scenarios += pass
				s.ScenarioAll += total
			}
			if r.Outcome == OutcomePostMergeFailed {
				s.Reverts++
			}
			continue
		}
		s.Runs++
		switch r.Outcome {
		case OutcomeCompleted:
			s.Completed++
		case OutcomeAwaitingHuman:
			s.Awaiting++
		case OutcomePostMergeFailed:
			s.Failed++
			s.Reverts++
		default:
			s.Failed++
		}
		if !r.StartedAt.IsZero() && !r.EndedAt.IsZero() && r.EndedAt.After(r.StartedAt) {
			durations = append(durations, r.EndedAt.Sub(r.StartedAt))
		}
		if pass, total, ok := parseScenarios(r.Scenarios); ok {
			s.Scenarios += pass
			s.ScenarioAll += total
		}
	}
	if s.Runs > 0 {
		s.SuccessRate = float64(s.Completed) / float64(s.Runs)
	}
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		s.MedianTime = durations[len(durations)/2]
		s.MedianSecs = s.MedianTime.Seconds()
	}
	return s
}

func parseScenarios(v string) (pass, total int, ok bool) {
	before, after, found := strings.Cut(v, "/")
	if !found {
		return 0, 0, false
	}
	p, err1 := strconv.Atoi(strings.TrimSpace(before))
	t, err2 := strconv.Atoi(strings.TrimSpace(after))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return p, t, true
}

// String renders the human-readable form of Stats.
func (s Stats) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "runs: %d\n", s.Runs)
	fmt.Fprintf(&b, "completed: %d\n", s.Completed)
	fmt.Fprintf(&b, "failed: %d\n", s.Failed)
	if s.Awaiting > 0 {
		fmt.Fprintf(&b, "awaiting human: %d\n", s.Awaiting)
	}
	fmt.Fprintf(&b, "success rate: %.1f%%\n", s.SuccessRate*100)
	if s.MedianTime > 0 {
		fmt.Fprintf(&b, "median duration: %s\n", s.MedianTime.Round(time.Second))
	}
	if s.Reverts > 0 {
		fmt.Fprintf(&b, "reverts: %d\n", s.Reverts)
	}
	if s.ScenarioAll > 0 {
		fmt.Fprintf(&b, "scenarios: %d/%d\n", s.Scenarios, s.ScenarioAll)
	}
	return strings.TrimRight(b.String(), "\n")
}
