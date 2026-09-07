package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// DefaultTTL bounds preview staleness (rate-limit friendly).
const DefaultTTL = 10 * time.Minute

// DefaultDir resolves the preview cache location:
// ${XDG_CACHE_HOME:-$HOME/.cache}/lead/issues (issue scope #37, replaces #14).
func DefaultDir() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "lead", "issues")
	}
	return filepath.Join(os.Getenv("HOME"), ".cache", "lead", "issues")
}

type cacheEntry struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Cache stores issue previews on disk with TTL expiry.
type Cache struct {
	Dir string
	TTL time.Duration
}

func (c *Cache) ttl() time.Duration {
	if c.TTL != 0 {
		return c.TTL
	}
	return DefaultTTL
}

// disabled reports whether caching is off (negative TTL).
func (c *Cache) disabled() bool { return c.TTL < 0 }

func (c *Cache) path(number int) string {
	return filepath.Join(c.Dir, fmt.Sprintf("%d.json", number))
}

// Get returns the cached preview text (hit=false on miss/expiry/corruption).
func (c *Cache) Get(number int) (text string, ok bool) {
	if c.disabled() {
		return "", false
	}
	raw, err := os.ReadFile(c.path(number))
	if err != nil {
		return "", false
	}
	var e cacheEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", false
	}
	if e.Number != number || time.Since(e.FetchedAt) > c.ttl() {
		return "", false
	}
	return fmt.Sprintf("#%d %s\n\n%s", e.Number, e.Title, e.Body), true
}

// Set stores a preview.
func (c *Cache) Set(number int, title, body, state string) error {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(cacheEntry{
		Number: number, Title: title, Body: body, State: state, FetchedAt: time.Now(),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(c.path(number), raw, 0o644)
}

// Previewer builds a PreviewFunc with read-through caching: cache hits
// never touch the network (dummy-gh call counts prove it in tests).
func Previewer(gh ports.GhClient, cache *Cache) PreviewFunc {
	return func(ctx context.Context, number int) (string, error) {
		if cache != nil {
			if text, ok := cache.Get(number); ok {
				return text, nil
			}
		}
		iss, err := gh.View(ctx, number)
		if err != nil {
			return "", err
		}
		if cache != nil && !cache.disabled() {
			_ = cache.Set(iss.Number, iss.Title, iss.Body, iss.State)
		}
		return fmt.Sprintf("#%d %s\n\n%s", iss.Number, iss.Title, iss.Body), nil
	}
}
