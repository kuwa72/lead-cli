package inbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// DefaultPreviewTTL bounds how long a saved issue detail is served without
// calling gh. It mirrors tui.DefaultTTL (rate-limit friendly).
const DefaultPreviewTTL = 10 * time.Minute

// PreviewCacheEntry is one issue's detail saved for a repository: body,
// state, labels and comments (issue #263).
type PreviewCacheEntry struct {
	Repo      string          `json:"repo"`
	Number    int             `json:"number"`
	FetchedAt time.Time       `json:"fetched_at"`
	Issue     ports.Issue     `json:"issue"`
	Comments  []ports.Comment `json:"comments,omitempty"`
}

// PreviewCacheStore reads and writes per-issue preview entries under Dir.
// Missing, corrupt, mismatched or expired files are cache misses.
type PreviewCacheStore struct {
	Dir string
	// Repo is the owning repository slug; entries from another repo are
	// ignored even if they land in Dir.
	Repo string
	TTL  time.Duration
	Now  func() time.Time
}

func (s *PreviewCacheStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *PreviewCacheStore) path(number int) string {
	return filepath.Join(s.Dir, fmt.Sprintf("%d.json", number))
}

// Load returns the entry when it is fresh, owned by this repo, and matches
// number. Any other outcome is a cache miss.
func (s *PreviewCacheStore) Load(number int) (PreviewCacheEntry, bool) {
	e, ok := s.loadAny(number)
	if !ok {
		return PreviewCacheEntry{}, false
	}
	ttl := DefaultPreviewTTL
	if s.TTL > 0 {
		ttl = s.TTL
	}
	if s.now().Sub(e.FetchedAt) > ttl {
		return PreviewCacheEntry{}, false
	}
	return e, true
}

// LoadStale returns the last good entry regardless of TTL, for showing
// content while gh is failing.
func (s *PreviewCacheStore) LoadStale(number int) (PreviewCacheEntry, bool) {
	return s.loadAny(number)
}

func (s *PreviewCacheStore) loadAny(number int) (PreviewCacheEntry, bool) {
	if s == nil || s.Dir == "" || number <= 0 {
		return PreviewCacheEntry{}, false
	}
	raw, err := os.ReadFile(s.path(number))
	if err != nil {
		return PreviewCacheEntry{}, false
	}
	var e PreviewCacheEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return PreviewCacheEntry{}, false
	}
	if e.Number != number || e.Issue.Number != number {
		return PreviewCacheEntry{}, false
	}
	if s.Repo != "" && e.Repo != "" && e.Repo != s.Repo {
		return PreviewCacheEntry{}, false
	}
	return e, true
}

// Save persists one entry atomically.
func (s *PreviewCacheStore) Save(e PreviewCacheEntry) error {
	if s == nil || s.Dir == "" || e.Number <= 0 {
		return nil
	}
	if e.Issue.Number == 0 {
		e.Issue.Number = e.Number
	} else if e.Issue.Number != e.Number {
		return fmt.Errorf("preview cache %s: issue number %d does not match entry %d", s.path(e.Number), e.Issue.Number, e.Number)
	}
	if e.FetchedAt.IsZero() {
		e.FetchedAt = s.now().UTC()
	}
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("preview cache %s: encode: %w", s.path(e.Number), err)
	}
	return writeFileAtomic(s.path(e.Number), raw)
}
