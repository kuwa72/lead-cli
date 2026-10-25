package inbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// newPreviewCacheFixture builds a headless model wired to a temp preview
// cache. seed runs before New so entries look like a previous session.
func newPreviewCacheFixture(t *testing.T, now time.Time, seed func(*PreviewCacheStore)) (*testutil.FakeGhClient, Model, *PreviewCacheStore) {
	t.Helper()
	gh := &testutil.FakeGhClient{
		Labeled: map[string][]ports.IssueSummary{
			LabelNeedsReview: {{Number: 7, Title: "spec: add warning"}, {Number: 8, Title: "spec: second"}},
		},
		Issues: map[int]ports.Issue{
			7: {Number: 7, Title: "spec: add warning", Body: "live body", State: "OPEN"},
			8: {Number: 8, Title: "spec: second", Body: "second body", State: "OPEN"},
		},
	}
	stateFile := filepath.Join(t.TempDir(), "workflows.json")
	store := &state.Store{Path: stateFile}
	pc := &PreviewCacheStore{
		Dir:  filepath.Join(t.TempDir(), "cache", "kuwa72_lead-cli", "preview"),
		Repo: "kuwa72/lead-cli",
		TTL:  DefaultPreviewTTL,
		Now:  func() time.Time { return now },
	}
	if seed != nil {
		seed(pc)
	}
	m := New(Options{
		Gh:           gh,
		Store:        store,
		PreviewCache: pc,
		Repo:         "kuwa72/lead-cli",
		PreviewDelay: 0,
		Headless:     true,
		Now:          func() time.Time { return now },
	})
	m = drive(t, m, nil, m.Init())
	return gh, m, pc
}

// selectIssue7 moves the cursor off #7 and back so the preview load runs.
func selectIssue7(t *testing.T, m Model) Model {
	t.Helper()
	return press(t, m, "k", "j")
}

func TestPreviewDiskCache_HitSkipsGhView(t *testing.T) {
	now := time.Now().UTC()
	gh, m, _ := newPreviewCacheFixture(t, now, func(pc *PreviewCacheStore) {
		if err := pc.Save(PreviewCacheEntry{
			Repo:      "kuwa72/lead-cli",
			Number:    7,
			FetchedAt: now.Add(-time.Minute),
			Issue:     ports.Issue{Number: 7, Title: "spec: add warning", Body: "cached body", State: "OPEN"},
			Comments:  []ports.Comment{{Author: "alice", Body: "cached comment"}},
		}); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
	})
	m.width = 120
	m.height = 30

	m = selectIssue7(t, m)

	if v := m.View(); !strings.Contains(v, "cached body") {
		t.Errorf("preview should show cached body, got:\n%s", v)
	}
	if len(gh.ViewCalls) != 0 {
		t.Errorf("cache hit must not call gh issue view, got ViewCalls=%v", gh.ViewCalls)
	}
	if len(gh.CommentsCalls) != 0 {
		t.Errorf("cache hit must not call gh issue view --json comments, got CommentsCalls=%v", gh.CommentsCalls)
	}
	if len(m.detailComments) != 1 || m.detailComments[0].Body != "cached comment" {
		t.Errorf("cached comments should be applied, got %+v", m.detailComments)
	}
}

func TestPreviewDiskCache_ExpiredRefetches(t *testing.T) {
	now := time.Now().UTC()
	gh, m, _ := newPreviewCacheFixture(t, now, func(pc *PreviewCacheStore) {
		if err := pc.Save(PreviewCacheEntry{
			Repo:      "kuwa72/lead-cli",
			Number:    7,
			FetchedAt: now.Add(-2 * DefaultPreviewTTL),
			Issue:     ports.Issue{Number: 7, Title: "spec: add warning", Body: "cached body", State: "OPEN"},
		}); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
	})
	m.width = 120
	m.height = 30

	m = selectIssue7(t, m)

	if !containsInt(gh.ViewCalls, 7) {
		t.Errorf("expired entry should refetch #7, got ViewCalls=%v", gh.ViewCalls)
	}
	if !containsInt(gh.CommentsCalls, 7) {
		t.Errorf("expired entry should refetch comments for #7, got CommentsCalls=%v", gh.CommentsCalls)
	}
	v := m.View()
	if !strings.Contains(v, "live body") {
		t.Errorf("preview should show refreshed body, got:\n%s", v)
	}
	if strings.Contains(v, "cached body") {
		t.Errorf("preview should not show expired body, got:\n%s", v)
	}
}

func TestPreviewDiskCache_CorruptFileRefetches(t *testing.T) {
	now := time.Now().UTC()
	gh, m, pc := newPreviewCacheFixture(t, now, func(pc *PreviewCacheStore) {
		if err := os.MkdirAll(pc.Dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(pc.path(7), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("seed corrupt file: %v", err)
		}
	})
	m.width = 120
	m.height = 30

	m = selectIssue7(t, m)

	if !containsInt(gh.ViewCalls, 7) {
		t.Errorf("corrupt cache should refetch #7, got ViewCalls=%v", gh.ViewCalls)
	}
	if v := m.View(); !strings.Contains(v, "live body") {
		t.Errorf("preview should recover and show live body, got:\n%s", v)
	}
	// A successful refetch repairs the entry for the next session.
	if _, ok := pc.Load(7); !ok {
		t.Errorf("successful refetch should rewrite the cache entry")
	}
}

func TestPreviewDiskCache_MismatchedNumberRefetches(t *testing.T) {
	now := time.Now().UTC()
	gh, m, _ := newPreviewCacheFixture(t, now, func(pc *PreviewCacheStore) {
		if err := pc.Save(PreviewCacheEntry{
			Repo:      "kuwa72/lead-cli",
			Number:    8,
			FetchedAt: now,
			Issue:     ports.Issue{Number: 8, Title: "spec: second", Body: "wrong body", State: "OPEN"},
		}); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
		// Rename the #8 entry to the #7 slot to model a mismatched file.
		if err := os.Rename(pc.path(8), pc.path(7)); err != nil {
			t.Fatalf("rename: %v", err)
		}
	})
	m.width = 120
	m.height = 30

	m = selectIssue7(t, m)

	if !containsInt(gh.ViewCalls, 7) {
		t.Errorf("mismatched entry should refetch #7, got ViewCalls=%v", gh.ViewCalls)
	}
	if v := m.View(); !strings.Contains(v, "live body") {
		t.Errorf("preview should show live body, got:\n%s", v)
	}
}

func TestPreviewDiskCache_SavesFetchedEntryForNextSession(t *testing.T) {
	now := time.Now().UTC()
	gh, m, pc := newPreviewCacheFixture(t, now, nil)
	m.width = 120
	m.height = 30

	m = press(t, m, "k", "j", "j") // move to #8, first fetch
	if !containsInt(gh.ViewCalls, 8) {
		t.Fatalf("first visit should fetch #8, got ViewCalls=%v", gh.ViewCalls)
	}
	entry, ok := pc.Load(8)
	if !ok {
		t.Fatalf("first fetch should persist #8 preview")
	}
	if entry.Issue.Body != "second body" {
		t.Errorf("saved body = %q, want %q", entry.Issue.Body, "second body")
	}

	// Simulate a restart: a fresh model sharing the same cache store.
	restarted := New(Options{
		Gh:           gh,
		Store:        m.opts.Store,
		PreviewCache: pc,
		Repo:         "kuwa72/lead-cli",
		PreviewDelay: 0,
		Headless:     true,
		Now:          func() time.Time { return now },
	})
	restarted = drive(t, restarted, nil, restarted.Init())
	restarted.width, restarted.height = 120, 30
	viewCalls := len(gh.ViewCalls)
	restarted = press(t, restarted, "k", "j", "j") // select #8 again

	if len(gh.ViewCalls) != viewCalls {
		t.Errorf("restart should reuse #8 cache, got new ViewCalls=%v", gh.ViewCalls[viewCalls:])
	}
	if v := restarted.View(); !strings.Contains(v, "second body") {
		t.Errorf("restart should show cached #8 body, got:\n%s", v)
	}
}

func TestPreviewDiskCache_RepoIsolation(t *testing.T) {
	now := time.Now().UTC()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)

	dirA := ResolvePreviewCacheDir("kuwa72/lead-cli")
	dirB := ResolvePreviewCacheDir("other/repo")
	if dirA == dirB {
		t.Fatalf("preview cache dirs must differ per repository: %q", dirA)
	}

	storeA := &PreviewCacheStore{Dir: dirA, Repo: "kuwa72/lead-cli", TTL: DefaultPreviewTTL, Now: func() time.Time { return now }}
	if err := storeA.Save(PreviewCacheEntry{
		Repo:      "kuwa72/lead-cli",
		Number:    7,
		FetchedAt: now,
		Issue:     ports.Issue{Number: 7, Title: "spec: add warning", Body: "repo A body", State: "OPEN"},
	}); err != nil {
		t.Fatalf("seed repo A cache: %v", err)
	}

	gh := &testutil.FakeGhClient{
		Labeled: map[string][]ports.IssueSummary{
			LabelNeedsReview: {{Number: 7, Title: "spec: add warning"}},
		},
		Issues: map[int]ports.Issue{
			7: {Number: 7, Title: "spec: add warning", Body: "repo B live body", State: "OPEN"},
		},
	}
	storeB := &PreviewCacheStore{Dir: dirB, Repo: "other/repo", TTL: DefaultPreviewTTL, Now: func() time.Time { return now }}
	m := New(Options{
		Gh:           gh,
		Store:        &state.Store{Path: filepath.Join(t.TempDir(), "workflows.json")},
		PreviewCache: storeB,
		Repo:         "other/repo",
		PreviewDelay: 0,
		Headless:     true,
		Now:          func() time.Time { return now },
	})
	m = drive(t, m, nil, m.Init())
	m.width, m.height = 120, 30
	m = press(t, m, "k", "j")

	if !containsInt(gh.ViewCalls, 7) {
		t.Errorf("other repo must not read repo A cache, got ViewCalls=%v", gh.ViewCalls)
	}
	v := m.View()
	if !strings.Contains(v, "repo B live body") {
		t.Errorf("preview should show this repo's body, got:\n%s", v)
	}
	if strings.Contains(v, "repo A body") {
		t.Errorf("preview must not show another repo's cached body, got:\n%s", v)
	}
}

func TestPreviewDiskCache_MemorySnapshotExpiresAfterTTL(t *testing.T) {
	now := time.Now().UTC()
	clock := now
	gh := &testutil.FakeGhClient{
		Labeled: map[string][]ports.IssueSummary{
			LabelNeedsReview: {{Number: 7, Title: "spec: add warning"}},
		},
		Issues: map[int]ports.Issue{
			7: {Number: 7, Title: "spec: add warning", Body: "live body", State: "OPEN"},
		},
	}
	pc := &PreviewCacheStore{
		Dir:  filepath.Join(t.TempDir(), "preview"),
		Repo: "kuwa72/lead-cli",
		TTL:  DefaultPreviewTTL,
		Now:  func() time.Time { return clock },
	}
	if err := pc.Save(PreviewCacheEntry{
		Repo:      "kuwa72/lead-cli",
		Number:    7,
		FetchedAt: now,
		Issue:     ports.Issue{Number: 7, Title: "spec: add warning", Body: "cached body", State: "OPEN"},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	m := New(Options{
		Gh:           gh,
		Store:        &state.Store{Path: filepath.Join(t.TempDir(), "workflows.json")},
		PreviewCache: pc,
		Repo:         "kuwa72/lead-cli",
		PreviewDelay: 0,
		Headless:     true,
		Now:          func() time.Time { return clock },
	})
	m = drive(t, m, nil, m.Init())
	m.width, m.height = 120, 30

	m = selectIssue7(t, m) // cache hit, snapshot held in memory
	if len(gh.ViewCalls) != 0 {
		t.Fatalf("first selection should hit cache, got ViewCalls=%v", gh.ViewCalls)
	}

	clock = now.Add(DefaultPreviewTTL + time.Minute)
	m = selectIssue7(t, m) // reselect after TTL

	if !containsInt(gh.ViewCalls, 7) {
		t.Errorf("reselect after TTL should refetch #7, got ViewCalls=%v", gh.ViewCalls)
	}
	if v := m.View(); !strings.Contains(v, "live body") {
		t.Errorf("preview should show refreshed body after TTL, got:\n%s", v)
	}
}

func TestPreviewDiskCache_GhErrorKeepsCachedBody(t *testing.T) {
	now := time.Now().UTC()
	gh, m, _ := newPreviewCacheFixture(t, now, func(pc *PreviewCacheStore) {
		if err := pc.Save(PreviewCacheEntry{
			Repo:      "kuwa72/lead-cli",
			Number:    7,
			FetchedAt: now.Add(-2 * DefaultPreviewTTL), // expired, so gh is attempted
			Issue:     ports.Issue{Number: 7, Title: "spec: add warning", Body: "cached body", State: "OPEN"},
		}); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
	})
	gh.ViewErr = errors.New("gh view failed")
	m.width = 120
	m.height = 30

	m = selectIssue7(t, m)

	v := m.View()
	if !strings.Contains(v, "cached body") {
		t.Errorf("gh failure should keep showing cached body, got:\n%s", v)
	}
	if !strings.Contains(v, "Unable to refresh #7") {
		t.Errorf("gh failure should surface a status error, got:\n%s", v)
	}
}
