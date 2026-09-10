package inbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// IssueCache is the persisted cache of GitHub issues for one repository (issue #140).
type IssueCache struct {
	Repo        string               `json:"repo"`
	CachedAt    time.Time            `json:"cached_at"`
	Review      []ports.IssueSummary `json:"review"`
	Blocked     []ports.IssueSummary `json:"blocked"`
	Merged      []ports.MergedIssue  `json:"merged"`
	OpenNumbers []int                `json:"open_numbers"`
}

// ResolveCachePath picks the cache file location next to workflows.json.
func ResolveCachePath(repo string) string {
	baseDir := ""
	if p := os.Getenv("LEAD_STATE_FILE"); p != "" {
		baseDir = filepath.Dir(p)
	} else if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		baseDir = filepath.Join(xdg, "lead")
	} else {
		baseDir = filepath.Join(os.Getenv("HOME"), ".local", "state", "lead")
	}
	slug := "default"
	if repo != "" {
		slug = strings.ReplaceAll(strings.ReplaceAll(repo, "/", "_"), ":", "_")
	}
	return filepath.Join(baseDir, "cache", slug+".json")
}

// CacheStore reads and writes the IssueCache atomically.
type CacheStore struct {
	Path string
}

// Load reads the cache from disk. A missing file returns nil, nil.
func (s *CacheStore) Load() (*IssueCache, error) {
	if s == nil || s.Path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inbox cache %s: read: %w", s.Path, err)
	}
	var c IssueCache
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("inbox cache %s: corrupt JSON (%v)", s.Path, err)
	}
	return &c, nil
}

// saveRaw writes raw bytes atomically.
func (s *CacheStore) saveRaw(raw []byte) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("inbox cache %s: mkdir: %w", s.Path, err)
	}
	tmp, err := os.CreateTemp(dir, "cache-*.tmp")
	if err != nil {
		return fmt.Errorf("inbox cache %s: temp: %w", s.Path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("inbox cache %s: write: %w", s.Path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("inbox cache %s: close: %w", s.Path, err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("inbox cache %s: rename: %w", s.Path, err)
	}
	return nil
}

// Save persists the cache atomically.
func (s *CacheStore) Save(c *IssueCache) error {
	if s == nil || s.Path == "" || c == nil {
		return nil
	}
	if c.CachedAt.IsZero() {
		c.CachedAt = time.Now().UTC()
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("inbox cache %s: encode: %w", s.Path, err)
	}
	return s.saveRaw(raw)
}
