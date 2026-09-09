package inbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SeenState is the inbox read-state persisted to disk (issue #107).
// It records when the human last opened the inbox and which merged issues
// have been manually confirmed, so they are not shown again.
type SeenState struct {
	LastSeenAt time.Time `json:"last_seen_at"`
	Confirmed  []int     `json:"confirmed"`
}

// IsConfirmed reports whether the given issue number has been confirmed.
func (s *SeenState) IsConfirmed(number int) bool {
	for _, n := range s.Confirmed {
		if n == number {
			return true
		}
	}
	return false
}

// Confirm marks an issue as seen and confirmed, deduplicating and keeping
// the list sorted.
func (s *SeenState) Confirm(number int) {
	if s.IsConfirmed(number) {
		return
	}
	s.Confirmed = append(s.Confirmed, number)
	sort.Ints(s.Confirmed)
}

// ConfirmedSet returns a map for O(1) lookups.
func (s *SeenState) ConfirmedSet() map[int]bool {
	m := make(map[int]bool, len(s.Confirmed))
	for _, n := range s.Confirmed {
		m[n] = true
	}
	return m
}

// ResolveSeenPath picks the inbox-seen file location next to workflows.json:
// same directory as LEAD_STATE_FILE, then $XDG_STATE_HOME/lead/inbox-seen.json,
// then ~/.local/state/lead/inbox-seen.json.
func ResolveSeenPath() string {
	if p := os.Getenv("LEAD_STATE_FILE"); p != "" {
		return filepath.Join(filepath.Dir(p), "inbox-seen.json")
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "lead", "inbox-seen.json")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "lead", "inbox-seen.json")
}

// SeenStore reads and writes inbox-seen.json atomically.
// A corrupt file is an error: callers must not overwrite it silently.
type SeenStore struct {
	Path string
}

// Load reads the read-state. A missing file yields a fresh, empty state.
func (s *SeenStore) Load() (*SeenState, error) {
	raw, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return &SeenState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inbox seen %s: read: %w", s.Path, err)
	}
	var st SeenState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("inbox seen %s: corrupt JSON (%v); refusing to overwrite — inspect or move it aside", s.Path, err)
	}
	return &st, nil
}

// save writes atomically (temp file + rename) so crashes never leave a
// half-written inbox-seen.json.
func (s *SeenStore) save(st *SeenState) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return fmt.Errorf("inbox seen %s: mkdir: %w", s.Path, err)
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("inbox seen %s: encode: %w", s.Path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), "inbox-seen-*.tmp")
	if err != nil {
		return fmt.Errorf("inbox seen %s: temp: %w", s.Path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("inbox seen %s: write: %w", s.Path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("inbox seen %s: close: %w", s.Path, err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("inbox seen %s: rename: %w", s.Path, err)
	}
	return nil
}

// Save persists the read-state atomically.
func (s *SeenStore) Save(st *SeenState) error {
	if st == nil {
		st = &SeenState{}
	}
	if st.LastSeenAt.IsZero() {
		st.LastSeenAt = time.Now().UTC()
	}
	return s.save(st)
}

// Confirm loads the state, marks the issue as confirmed, updates
// LastSeenAt, and persists. Failures are returned, not swallowed.
func (s *SeenStore) Confirm(number int) error {
	st, err := s.Load()
	if err != nil {
		return err
	}
	st.Confirm(number)
	return s.Save(st)
}
