// Package state persists Issue↔Branch↔Worktree↔PR links (issue #40).
// See docs/rfc-25-workflow-flexibility.md §5 for the design.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Statuses are lead's logical workflow states (RFC §4.1). They never replace
// GitHub's open/closed; they describe local work progress.
type Status string

const (
	StatusOpen           Status = "open"
	StatusPlanned        Status = "planned"
	StatusInProgress     Status = "in_progress"
	StatusBlocked        Status = "blocked"
	StatusAwaitingReview Status = "awaiting_review"
	StatusCompleted      Status = "completed"
	StatusClosed         Status = "closed"
)

// Modes of work (RFC §3).
const (
	ModeImplement = "implement"
	ModeReview    = "review"
	ModeSplit     = "split"
	ModeResearch  = "research"
	ModeDocs      = "docs"
)

// PRRef links one pull request to a workflow.
type PRRef struct {
	Number int    `json:"number"`
	Part   string `json:"part,omitempty"`
	Status string `json:"status"`
}

// Workflow is one row of workflows.json (RFC §5.2 minimal record).
type Workflow struct {
	Repository   string   `json:"repository"`
	Issue        int      `json:"issue"`
	ParentIssue  *int     `json:"parent_issue,omitempty"`
	Mode         string   `json:"mode"`
	Part         string   `json:"part,omitempty"`
	Branch       string   `json:"branch"`
	Worktree     string   `json:"worktree,omitempty"`
	Pane         string   `json:"pane,omitempty"`
	PullRequests []PRRef  `json:"pull_requests,omitempty"`
	Status       Status   `json:"status"`
	MergePolicy  string   `json:"merge_policy,omitempty"`
	// PolicyReason explains a MergePolicy that `lead finish` lowered itself
	// (guardrail: PR touched protected files). Empty when the policy came
	// from the human or the mode default.
	PolicyReason string   `json:"policy_reason,omitempty"`
	AgentMode    string   `json:"agent_mode,omitempty"`
	Artifacts    []string `json:"artifacts,omitempty"`
	// Dispatch bookkeeping (docs/rfc-inbox-ux.md §7): which headless agent
	// ran, how many times it failed to close the issue, the last process
	// and its log. GitHub stays the source of truth for the issue itself.
	Agent     string    `json:"agent,omitempty"`
	Attempts  int       `json:"attempts,omitempty"`
	PID       int       `json:"pid,omitempty"`
	LogPath   string    `json:"log_path,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Key identifies a workflow: issue number plus optional --part slug.
func Key(issue int, part string) string {
	if part == "" {
		return fmt.Sprintf("%d", issue)
	}
	return fmt.Sprintf("%d:%s", issue, part)
}

// CheckTransition rejects illegal status moves. Terminal Closed only
// reopens via explicit resume (Closed→InProgress).
func CheckTransition(from, to Status) error {
	allowed := map[Status][]Status{
		StatusOpen:           {StatusPlanned, StatusInProgress, StatusClosed},
		StatusPlanned:        {StatusInProgress, StatusClosed},
		StatusInProgress:     {StatusBlocked, StatusAwaitingReview, StatusClosed},
		StatusBlocked:        {StatusInProgress},
		StatusAwaitingReview: {StatusInProgress, StatusCompleted},
		StatusCompleted:      {StatusClosed},
		StatusClosed:         {StatusInProgress},
	}
	for _, next := range allowed[from] {
		if next == to {
			return nil
		}
	}
	return fmt.Errorf("illegal status transition %q → %q", from, to)
}

// ResolvePath picks the state file location (RFC §5.2):
// LEAD_STATE_FILE, then $XDG_STATE_HOME/lead/workflows.json,
// then ~/.local/state/lead/workflows.json. Never inside a repository.
func ResolvePath() string {
	if p := os.Getenv("LEAD_STATE_FILE"); p != "" {
		return p
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "lead", "workflows.json")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "lead", "workflows.json")
}

type fileShape struct {
	Version   int        `json:"version"`
	Workflows []Workflow `json:"workflows"`
}

// Store reads/writes workflows.json. Use ResolvePath for the default Path.
type Store struct {
	Path string
}

// Load reads all workflows. A missing file yields an empty store.
// A corrupt file is an error: callers must not overwrite it blindly.
func (s *Store) Load() ([]Workflow, error) {
	raw, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state %s: read: %w", s.Path, err)
	}
	var shape fileShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, fmt.Errorf("state %s: corrupt JSON (%v); refusing to overwrite — inspect or move it aside, then rebuild via `lead resume --repair`", s.Path, err)
	}
	return shape.Workflows, nil
}

// save writes atomically (temp file + rename) so crashes never leave
// a half-written workflows.json.
func (s *Store) save(all []Workflow) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("state %s: mkdir: %w", s.Path, err)
	}
	raw, err := json.MarshalIndent(fileShape{Version: 1, Workflows: all}, "", "  ")
	if err != nil {
		return fmt.Errorf("state %s: encode: %w", s.Path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), "workflows-*.tmp")
	if err != nil {
		return fmt.Errorf("state %s: temp: %w", s.Path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("state %s: write: %w", s.Path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("state %s: close: %w", s.Path, err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("state %s: rename: %w", s.Path, err)
	}
	return nil
}

// List returns all workflows (empty when the file is absent).
func (s *Store) List() ([]Workflow, error) {
	all, err := s.Load()
	if err != nil {
		return nil, err
	}
	if all == nil {
		return []Workflow{}, nil
	}
	return all, nil
}

// Get finds one workflow by issue and part.
func (s *Store) Get(issue int, part string) (Workflow, bool, error) {
	all, err := s.Load()
	if err != nil {
		return Workflow{}, false, err
	}
	want := Key(issue, part)
	for _, w := range all {
		if Key(w.Issue, w.Part) == want {
			return w, true, nil
		}
	}
	return Workflow{}, false, nil
}

// Upsert inserts or replaces a workflow (stamping UpdatedAt).
// It refuses to run when the existing file is corrupt.
func (s *Store) Upsert(w Workflow) error {
	all, err := s.Load()
	if err != nil {
		return err
	}
	w.UpdatedAt = time.Now().UTC()
	want := Key(w.Issue, w.Part)
	replaced := false
	for i, cur := range all {
		if Key(cur.Issue, cur.Part) == want {
			all[i] = w
			replaced = true
			break
		}
	}
	if !replaced {
		all = append(all, w)
	}
	return s.save(all)
}

// Delete removes one workflow. ok=false means absent (idempotent success).
// It refuses to run when the existing file is corrupt.
func (s *Store) Delete(issue int, part string) (ok bool, err error) {
	all, err := s.Load()
	if err != nil {
		return false, err
	}
	want := Key(issue, part)
	kept := all[:0]
	for _, w := range all {
		if Key(w.Issue, w.Part) != want {
			kept = append(kept, w)
		}
	}
	if len(kept) == len(all) {
		return false, nil
	}
	return true, s.save(kept)
}

// RepairUpsert inserts or replaces a workflow, repairing/overwriting the file
// even if the existing file contains corrupt JSON.
func (s *Store) RepairUpsert(w Workflow) error {
	all, err := s.Load()
	if err != nil {
		all = nil // ignore corrupt file
	}
	w.UpdatedAt = time.Now().UTC()
	want := Key(w.Issue, w.Part)
	replaced := false
	for i, cur := range all {
		if Key(cur.Issue, cur.Part) == want {
			all[i] = w
			replaced = true
			break
		}
	}
	if !replaced {
		all = append(all, w)
	}
	return s.save(all)
}

// FindByTarget returns all workflows matching target.
// If target is numeric (or #<num>), it matches Issue or PullRequests number.
// Otherwise it matches Branch or Worktree path.
func (s *Store) FindByTarget(target string) ([]Workflow, error) {
	all, err := s.Load()
	if err != nil {
		return nil, err
	}
	target = strings.TrimSpace(target)
	numStr := strings.TrimPrefix(target, "#")
	num, isNumErr := strconv.Atoi(numStr)
	isNumeric := isNumErr == nil && num > 0

	var matches []Workflow
	for _, w := range all {
		if isNumeric {
			if w.Issue == num {
				matches = append(matches, w)
				continue
			}
			for _, pr := range w.PullRequests {
				if pr.Number == num {
					matches = append(matches, w)
					break
				}
			}
		} else {
			if w.Branch == target || w.Worktree == target || filepath.Base(w.Worktree) == target {
				matches = append(matches, w)
			}
		}
	}
	return matches, nil
}
