package inbox

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

func TestCacheStore_SaveAndLoad(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "cache", "test_repo.json")
	store := &CacheStore{Path: path}

	now := time.Now().UTC().Truncate(time.Second)
	cache := &IssueCache{
		Repo:     "kuwa72/lead-cli",
		CachedAt: now,
		Review:   []ports.IssueSummary{{Number: 1, Title: "review issue"}},
		Blocked:  []ports.IssueSummary{{Number: 2, Title: "blocked issue"}},
		Merged: []ports.MergedIssue{{
			Number:   3,
			Title:    "merged issue",
			MergedAt: now,
		}},
		OpenNumbers: []int{1, 2},
	}

	if err := store.Save(cache); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Repo != cache.Repo {
		t.Errorf("Repo = %q, want %q", loaded.Repo, cache.Repo)
	}
	if len(loaded.Review) != 1 || loaded.Review[0].Number != 1 {
		t.Errorf("Review = %+v, want 1 issue (#1)", loaded.Review)
	}
	if len(loaded.Blocked) != 1 || loaded.Blocked[0].Number != 2 {
		t.Errorf("Blocked = %+v, want 1 issue (#2)", loaded.Blocked)
	}
	if len(loaded.Merged) != 1 || loaded.Merged[0].Number != 3 {
		t.Errorf("Merged = %+v, want 1 issue (#3)", loaded.Merged)
	}
	if len(loaded.OpenNumbers) != 2 || loaded.OpenNumbers[0] != 1 {
		t.Errorf("OpenNumbers = %+v, want [1, 2]", loaded.OpenNumbers)
	}
}

func TestCacheStore_CorruptJSON(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "cache", "corrupt.json")
	store := &CacheStore{Path: path}

	// Corrupt content
	if err := store.saveRaw([]byte("{invalid json")); err != nil {
		t.Fatalf("saveRaw: %v", err)
	}

	_, err := store.Load()
	if err == nil {
		t.Fatal("Load corrupt JSON: want error, got nil")
	}
}

func TestInbox_InitialRenderFromCache(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "cache", "test_repo.json")
	cacheStore := &CacheStore{Path: path}

	now := time.Now().UTC()
	_ = cacheStore.Save(&IssueCache{
		Repo:        "test/repo",
		CachedAt:    now,
		Review:      []ports.IssueSummary{{Number: 10, Title: "Cached review"}},
		Blocked:     []ports.IssueSummary{},
		Merged:      []ports.MergedIssue{},
		OpenNumbers: []int{10},
	})

	opts := Options{
		Repo:     "test/repo",
		Cache:    cacheStore,
		Headless: true,
		Now:      func() time.Time { return now },
	}

	m := New(opts)
	// Even before Init/loadCmd executes, m.sections should be populated from cache!
	rows := m.visible()
	found := false
	for _, r := range rows {
		if r.Item != nil && r.Item.Number == 10 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("New() did not render cached issue #10 immediately")
	}
}

func TestInbox_OfflineFallbackKeepsCachedSections(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "cache", "test_repo.json")
	cacheStore := &CacheStore{Path: path}

	now := time.Now().UTC()
	_ = cacheStore.Save(&IssueCache{
		Repo:        "test/repo",
		CachedAt:    now,
		Review:      []ports.IssueSummary{{Number: 10, Title: "Cached review"}},
		Blocked:     []ports.IssueSummary{},
		Merged:      []ports.MergedIssue{},
		OpenNumbers: []int{10},
	})

	// Gh client that always errors (simulating network failure)
	failingGh := &testutil.FakeGhClient{
		ListErr:      errors.New("network unreachable"),
		LabelListErr: errors.New("network unreachable"),
	}

	opts := Options{
		Repo:     "test/repo",
		Gh:       failingGh,
		Cache:    cacheStore,
		Headless: true,
		Now:      func() time.Time { return now },
	}

	m := New(opts)
	// Execute loadCmd
	cmd := m.loadCmd()
	msg := cmd()

	// Update model with loadedMsg (which has an error)
	newM, _ := m.Update(msg)
	model := newM.(Model)

	// Cached sections must be preserved
	rows := model.visible()
	found := false
	for _, r := range rows {
		if r.Item != nil && r.Item.Number == 10 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("offline update discarded cached sections")
	}
	// Status should indicate cache fallback / error
	if model.status == "" {
		t.Errorf("offline update should surface cache fallback status")
	}
}
