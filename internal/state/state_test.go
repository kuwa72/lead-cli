package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "workflows.json")}
}

func TestResolvePathPriority(t *testing.T) {
	t.Setenv("LEAD_STATE_FILE", "/tmp/custom/wf.json")
	if got := ResolvePath(); got != "/tmp/custom/wf.json" {
		t.Fatalf("ResolvePath = %q, want LEAD_STATE_FILE override", got)
	}
}

func TestResolvePathUsesXDGStateHome(t *testing.T) {
	t.Setenv("LEAD_STATE_FILE", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg")
	if got := ResolvePath(); got != "/tmp/xdg/lead/workflows.json" {
		t.Fatalf("ResolvePath = %q, want XDG_STATE_HOME path", got)
	}
}

func TestResolvePathFallsBackToHome(t *testing.T) {
	t.Setenv("LEAD_STATE_FILE", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/tmp/fakehome")
	if got := ResolvePath(); got != "/tmp/fakehome/.local/state/lead/workflows.json" {
		t.Fatalf("ResolvePath = %q, want ~/.local/state fallback", got)
	}
}

func TestUpsertGetRoundTrip(t *testing.T) {
	s := tempStore(t)
	w := Workflow{Repository: "o/r", Issue: 25, Mode: ModeImplement, Branch: "issue/25-x", Status: StatusOpen}

	if err := s.Upsert(w); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, ok, err := s.Get(25, "")
	if err != nil || !ok {
		t.Fatalf("Get = %+v, %v, %v; want record", got, ok, err)
	}
	if got.Branch != "issue/25-x" || got.Mode != ModeImplement {
		t.Errorf("Get = %+v, want stored fields", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt not stamped on Upsert")
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	s := tempStore(t)
	w := Workflow{Issue: 25, Mode: ModeImplement, Branch: "issue/25-x", Status: StatusOpen}
	if err := s.Upsert(w); err != nil {
		t.Fatal(err)
	}
	w.Status = StatusInProgress
	if err := s.Upsert(w); err != nil {
		t.Fatal(err)
	}
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Status != StatusInProgress {
		t.Errorf("List after re-Upsert = %+v, want single updated record", all)
	}
}

func TestPartDistinguishesRecords(t *testing.T) {
	s := tempStore(t)
	for _, part := range []string{"", "api"} {
		if err := s.Upsert(Workflow{Issue: 25, Part: part, Status: StatusOpen}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.List()
	if err != nil || len(all) != 2 {
		t.Fatalf("List = %d, %v; want 2 part-distinguished records", len(all), err)
	}
}

func TestDeleteRemovesRecordAndIsIdempotent(t *testing.T) {
	s := tempStore(t)
	if err := s.Upsert(Workflow{Issue: 25, Status: StatusOpen}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Delete(25, "")
	if err != nil || !removed {
		t.Fatalf("Delete = %v, %v; want true, nil", removed, err)
	}
	removed, err = s.Delete(25, "")
	if err != nil || removed {
		t.Errorf("re-Delete = %v, %v; want false, nil (idempotent)", removed, err)
	}
	if _, ok, _ := s.Get(25, ""); ok {
		t.Error("Get after Delete found record, want absent")
	}
}

func TestMissingFileLoadsEmpty(t *testing.T) {
	all, err := tempStore(t).List()
	if err != nil {
		t.Fatalf("List on missing file: %v, want empty store", err)
	}
	if len(all) != 0 {
		t.Errorf("List on missing file = %+v, want empty", all)
	}
}

func TestCorruptFileRefusesToLoad(t *testing.T) {
	s := tempStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err == nil {
		t.Fatal("List on corrupt file = nil, want error refusing to proceed")
	} else if !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("corrupt error = %q, want 'corrupt' guidance", err)
	}
	// A failed load must never clobber the file with a fresh store.
	if err := s.Upsert(Workflow{Issue: 1}); err == nil {
		t.Error("Upsert over corrupt file = nil, want refusal (no silent overwrite)")
	}
	raw, _ := os.ReadFile(s.Path)
	if string(raw) != "{broken" {
		t.Errorf("corrupt file overwritten: %q", raw)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	s := tempStore(t)
	if err := s.Upsert(Workflow{Issue: 7, Branch: "issue/7-x", Status: StatusOpen}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(s.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %q left behind; save must rename atomically", e.Name())
		}
	}
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "issue/7-x") {
		t.Errorf("saved file missing record: %s", raw)
	}
}

func TestCheckTransition(t *testing.T) {
	valid := [][2]Status{
		{StatusOpen, StatusPlanned},
		{StatusOpen, StatusInProgress},
		{StatusPlanned, StatusInProgress},
		{StatusInProgress, StatusBlocked},
		{StatusBlocked, StatusInProgress},
		{StatusInProgress, StatusAwaitingReview},
		{StatusAwaitingReview, StatusInProgress},
		{StatusAwaitingReview, StatusCompleted},
		{StatusCompleted, StatusClosed},
		{StatusInProgress, StatusClosed},
		{StatusClosed, StatusInProgress}, // resume
	}
	for _, v := range valid {
		if err := CheckTransition(v[0], v[1]); err != nil {
			t.Errorf("CheckTransition(%q→%q) = %v, want nil", v[0], v[1], err)
		}
	}
	invalid := [][2]Status{
		{StatusOpen, StatusCompleted},
		{StatusOpen, StatusAwaitingReview},
		{StatusCompleted, StatusInProgress},
		{StatusClosed, StatusClosed},
		{StatusPlanned, StatusCompleted},
	}
	for _, v := range invalid {
		if err := CheckTransition(v[0], v[1]); err == nil {
			t.Errorf("CheckTransition(%q→%q) = nil, want rejection", v[0], v[1])
		}
	}
}

func TestRepairUpsert(t *testing.T) {
	s := tempStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, []byte("{corrupt-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := Workflow{Issue: 42, Branch: "issue/42-fix", Status: StatusInProgress}
	if err := s.RepairUpsert(w); err != nil {
		t.Fatalf("RepairUpsert on corrupt file: %v", err)
	}
	got, ok, err := s.Get(42, "")
	if err != nil || !ok {
		t.Fatalf("Get after RepairUpsert = %+v, %v, %v", got, ok, err)
	}
	if got.Branch != "issue/42-fix" {
		t.Errorf("got branch %q, want issue/42-fix", got.Branch)
	}
}

func TestFindByTarget(t *testing.T) {
	s := tempStore(t)
	w1 := Workflow{Issue: 10, Branch: "issue/10-feature", PullRequests: []PRRef{{Number: 100}}}
	w2 := Workflow{Issue: 20, Branch: "issue/20-bug", Worktree: "/path/to/wt-20"}
	if err := s.Upsert(w1); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(w2); err != nil {
		t.Fatal(err)
	}

	// numeric issue
	m, err := s.FindByTarget("10")
	if err != nil || len(m) != 1 || m[0].Issue != 10 {
		t.Errorf("FindByTarget(10) = %+v, %v; want w1", m, err)
	}
	// numeric with #
	m, err = s.FindByTarget("#10")
	if err != nil || len(m) != 1 || m[0].Issue != 10 {
		t.Errorf("FindByTarget(#10) = %+v, %v; want w1", m, err)
	}
	// PR number
	m, err = s.FindByTarget("100")
	if err != nil || len(m) != 1 || m[0].Issue != 10 {
		t.Errorf("FindByTarget(100) = %+v, %v; want w1", m, err)
	}
	// branch name
	m, err = s.FindByTarget("issue/20-bug")
	if err != nil || len(m) != 1 || m[0].Issue != 20 {
		t.Errorf("FindByTarget(branch) = %+v, %v; want w2", m, err)
	}
	// worktree path
	m, err = s.FindByTarget("/path/to/wt-20")
	if err != nil || len(m) != 1 || m[0].Issue != 20 {
		t.Errorf("FindByTarget(worktree) = %+v, %v; want w2", m, err)
	}
}
