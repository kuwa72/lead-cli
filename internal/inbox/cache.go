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
	OpenIssues  []ports.IssueSummary `json:"open_issues,omitempty"`
	// Ready is the dispatch queue in display order (already sorted by
	// dispatch.OrderReady, deferred issues last). Issue #212.
	Ready []ports.IssueSummary `json:"ready,omitempty"`
}

// stateDir is the lead state directory holding workflows.json and caches.
func stateDir() string {
	if p := os.Getenv("LEAD_STATE_FILE"); p != "" {
		return filepath.Dir(p)
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "lead")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "lead")
}

// cacheSlug turns "owner/name" into a filesystem-safe per-repository name.
func cacheSlug(repo string) string {
	if repo == "" {
		return "default"
	}
	return strings.ReplaceAll(strings.ReplaceAll(repo, "/", "_"), ":", "_")
}

// ResolveCachePath picks the cache file location next to workflows.json.
func ResolveCachePath(repo string) string {
	return filepath.Join(stateDir(), "cache", cacheSlug(repo)+".json")
}

// ResolvePreviewCacheDir picks the per-repository directory holding issue
// detail previews (issue #263).
func ResolvePreviewCacheDir(repo string) string {
	return filepath.Join(stateDir(), "cache", cacheSlug(repo), "preview")
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
	return writeFileAtomic(s.Path, raw)
}

// writeFileAtomic writes raw to path through a temp file + rename so readers
// never observe a partial file.
func writeFileAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cache %s: mkdir: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, "cache-*.tmp")
	if err != nil {
		return fmt.Errorf("cache %s: temp: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("cache %s: write: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cache %s: close: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cache %s: rename: %w", path, err)
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
