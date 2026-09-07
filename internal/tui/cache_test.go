package tui

import (
	"os"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

func TestPreviewer_CachesAcrossCalls(t *testing.T) {
	fake := &testutil.FakeGhClient{
		Issues: map[int]ports.Issue{36: {Number: 36, Title: "x", Body: "hello", State: "OPEN"}},
	}
	cache := &Cache{Dir: t.TempDir(), TTL: time.Hour}
	preview := Previewer(fake, cache)

	first, err := preview(t.Context(), 36)
	if err != nil || first == "" {
		t.Fatalf("preview = %q, %v", first, err)
	}
	second, err := preview(t.Context(), 36)
	if err != nil || second != first {
		t.Fatalf("cached preview = %q, %v; want identical", second, err)
	}
	if len(fake.ViewCalls) != 1 {
		t.Errorf("gh View calls = %d, want 1 (second served from cache)", len(fake.ViewCalls))
	}
}

func TestPreviewer_DisabledCacheHitsGhEveryTime(t *testing.T) {
	fake := &testutil.FakeGhClient{
		Issues: map[int]ports.Issue{36: {Number: 36, Title: "x", Body: "hello", State: "OPEN"}},
	}
	cache := &Cache{Dir: t.TempDir(), TTL: -1} // caching disabled
	preview := Previewer(fake, cache)

	if _, err := preview(t.Context(), 36); err != nil {
		t.Fatal(err)
	}
	if _, err := preview(t.Context(), 36); err != nil {
		t.Fatal(err)
	}
	if len(fake.ViewCalls) != 2 {
		t.Errorf("gh View calls = %d, want 2 (cache disabled)", len(fake.ViewCalls))
	}
}

func TestPreviewer_ExpiredEntriesRefetch(t *testing.T) {
	fake := &testutil.FakeGhClient{
		Issues: map[int]ports.Issue{36: {Number: 36, Title: "x", Body: "hello", State: "OPEN"}},
	}
	cache := &Cache{Dir: t.TempDir(), TTL: time.Nanosecond}
	preview := Previewer(fake, cache)

	if _, err := preview(t.Context(), 36); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Nanosecond)
	if _, err := preview(t.Context(), 36); err != nil {
		t.Fatal(err)
	}
	if len(fake.ViewCalls) != 2 {
		t.Errorf("gh View calls = %d, want 2 (entry expired)", len(fake.ViewCalls))
	}
}

func TestCache_CorruptFileIsMiss(t *testing.T) {
	dir := t.TempDir()
	cache := &Cache{Dir: dir, TTL: time.Hour}
	if err := os.WriteFile(cache.path(36), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Get(36); ok {
		t.Error("Get on corrupt entry = hit, want miss")
	}
}

func TestDefaultDir_RespectsXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	if got := DefaultDir(); got != "/tmp/xdg-cache/lead/issues" {
		t.Errorf("DefaultDir = %q, want XDG path", got)
	}
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/tmp/fakehome")
	if got := DefaultDir(); got != "/tmp/fakehome/.cache/lead/issues" {
		t.Errorf("DefaultDir = %q, want ~/.cache fallback", got)
	}
}
