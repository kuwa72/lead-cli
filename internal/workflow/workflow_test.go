package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

type mockGitRunner struct {
	root       string
	origin     string
	branches   map[string]bool
	worktrees  []git.WorktreeInfo
	checkedOut string
}

func (m *mockGitRunner) RepoRoot(dir string) (string, error) { return m.root, nil }
func (m *mockGitRunner) CreateBranch(repoDir, branch string) error {
	if m.branches == nil {
		m.branches = make(map[string]bool)
	}
	m.branches[branch] = true
	m.checkedOut = branch
	return nil
}
func (m *mockGitRunner) WorktreeAdd(repoDir, path, branch string) error {
	m.worktrees = append(m.worktrees, git.WorktreeInfo{Path: path, Branch: branch})
	return os.MkdirAll(path, 0o755)
}
func (m *mockGitRunner) WorktreeRemove(repoDir, path string, force bool) error {
	var kept []git.WorktreeInfo
	for _, w := range m.worktrees {
		if w.Path != path {
			kept = append(kept, w)
		}
	}
	m.worktrees = kept
	return os.RemoveAll(path)
}
func (m *mockGitRunner) OriginURL(repoDir string) string { return m.origin }
func (m *mockGitRunner) BranchExists(repoDir, branch string) (bool, error) {
	return m.branches[branch], nil
}
func (m *mockGitRunner) WorktreeList(repoDir string) ([]git.WorktreeInfo, error) {
	return m.worktrees, nil
}
func (m *mockGitRunner) ListBranches(repoDir string) ([]string, error) {
	var list []string
	for b := range m.branches {
		list = append(list, b)
	}
	return list, nil
}
func (m *mockGitRunner) CheckoutBranch(repoDir, branch string) error {
	if !m.branches[branch] {
		return os.ErrNotExist
	}
	m.checkedOut = branch
	return nil
}

func newMockGit(t *testing.T) *mockGitRunner {
	t.Helper()
	return &mockGitRunner{
		root:     t.TempDir(),
		origin:   "git@github.com:kuwa72/lead-cli.git",
		branches: make(map[string]bool),
	}
}

func TestResolveTarget_ByIssueNumberFromState(t *testing.T) {
	g := newMockGit(t)
	g.branches["issue/10-feature"] = true
	store := &state.Store{Path: filepath.Join(t.TempDir(), "workflows.json")}
	if err := store.Upsert(state.Workflow{Issue: 10, Branch: "issue/10-feature", Status: state.StatusInProgress}); err != nil {
		t.Fatal(err)
	}

	cand, err := ResolveTarget(context.Background(), g, store, nil, g.root, "10", false)
	if err != nil {
		t.Fatalf("ResolveTarget(10): %v", err)
	}
	if cand.Issue != 10 || cand.Branch != "issue/10-feature" {
		t.Errorf("cand = %+v, want Issue 10, Branch issue/10-feature", cand)
	}
}

func TestResolveTarget_ByBranchNameFromGit(t *testing.T) {
	g := newMockGit(t)
	g.branches["issue/20-bugfix"] = true
	store := &state.Store{Path: filepath.Join(t.TempDir(), "workflows.json")}

	cand, err := ResolveTarget(context.Background(), g, store, nil, g.root, "issue/20-bugfix", false)
	if err != nil {
		t.Fatalf("ResolveTarget(branch): %v", err)
	}
	if cand.Issue != 20 || cand.Branch != "issue/20-bugfix" {
		t.Errorf("cand = %+v, want Issue 20, Branch issue/20-bugfix", cand)
	}
}

func TestResolveTarget_ByPRNumberFromGh(t *testing.T) {
	g := newMockGit(t)
	g.branches["issue/30-pr-target"] = true
	store := &state.Store{Path: filepath.Join(t.TempDir(), "workflows.json")}
	gh := &testutil.FakeGhClient{
		PR: ports.PRInfo{Number: 42, State: "open", HeadRefName: "issue/30-pr-target"},
	}

	cand, err := ResolveTarget(context.Background(), g, store, gh, g.root, "42", false)
	if err != nil {
		t.Fatalf("ResolveTarget(42): %v", err)
	}
	if cand.Issue != 30 || cand.Branch != "issue/30-pr-target" {
		t.Errorf("cand = %+v, want Issue 30, Branch issue/30-pr-target", cand)
	}
}

func TestResolveTarget_CorruptStateRefusesUnlessRepair(t *testing.T) {
	g := newMockGit(t)
	g.branches["issue/50-fix"] = true
	statePath := filepath.Join(t.TempDir(), "workflows.json")
	if err := os.WriteFile(statePath, []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &state.Store{Path: statePath}

	// Without repair: must return corruption error
	_, err := ResolveTarget(context.Background(), g, store, nil, g.root, "50", false)
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("ResolveTarget on corrupt without repair = %v, want corrupt error", err)
	}

	// With repair: should succeed via git discovery
	cand, err := ResolveTarget(context.Background(), g, store, nil, g.root, "50", true)
	if err != nil {
		t.Fatalf("ResolveTarget with repair: %v", err)
	}
	if cand.Issue != 50 || cand.Branch != "issue/50-fix" {
		t.Errorf("cand = %+v, want Issue 50", cand)
	}
}

func TestResume_NeverCreatesNewBranch(t *testing.T) {
	g := newMockGit(t)
	// Branch does NOT exist in git
	store := &state.Store{Path: filepath.Join(t.TempDir(), "workflows.json")}

	_, err := Resume(context.Background(), g, store, nil, ResumeOptions{
		Target:  "issue/99-nonexistent",
		WorkDir: g.root,
	})
	if err == nil {
		t.Fatal("Resume on non-existent branch succeeded, want failure")
	}
	if len(g.branches) != 0 {
		t.Fatalf("Resume created branches: %v", g.branches)
	}
}

func TestResume_ReusesWorktreeAndTransitionsStatus(t *testing.T) {
	g := newMockGit(t)
	g.branches["issue/60-wt"] = true
	wtPath := filepath.Join(g.root, ".worktrees", "issue-60")
	if err := g.WorktreeAdd(g.root, wtPath, "issue/60-wt"); err != nil {
		t.Fatal(err)
	}
	store := &state.Store{Path: filepath.Join(t.TempDir(), "workflows.json")}
	if err := store.Upsert(state.Workflow{
		Issue: 60, Branch: "issue/60-wt", Worktree: wtPath, Status: state.StatusBlocked, Attempts: 3,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := Resume(context.Background(), g, store, nil, ResumeOptions{
		Target:  "60",
		WorkDir: g.root,
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res.Status != state.StatusInProgress {
		t.Errorf("Status = %s, want in_progress", res.Status)
	}
	if res.Worktree != wtPath {
		t.Errorf("Worktree = %s, want %s", res.Worktree, wtPath)
	}

	saved, ok, _ := store.Get(60, "")
	if !ok || saved.Status != state.StatusInProgress || saved.Attempts != 0 {
		t.Errorf("store after resume = %+v, want in_progress and 0 attempts", saved)
	}
}
