package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// initRepo creates a git repo with one commit for worktree tests.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir(),
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func TestRepoRootFindsTopLevel(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := New().RepoRoot(sub)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	if got != repo {
		t.Errorf("RepoRoot = %q, want %q", got, repo)
	}
}

func TestRepoSlug(t *testing.T) {
	cases := map[string]string{
		"https://github.com/o/r.git":    "o/r",
		"https://github.com/o/r":        "o/r",
		"git@github.com:o/r.git":        "o/r",
		"ssh://git@github.com/o/r.git":  "o/r",
		"ssh://git@ghe.corp:2222/o/r":   "o/r",
		"github.com/o/r":                "o/r",
		"o/r":                           "o/r",
		" https://github.com/o/r.git/ ": "o/r",
		"local":                         "",
		"":                              "",
		"https://github.com/":           "",
	}
	for in, want := range cases {
		if got := RepoSlug(in); got != want {
			t.Errorf("RepoSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepoRootOutsideRepoFails(t *testing.T) {
	if _, err := New().RepoRoot(t.TempDir()); err == nil {
		t.Error("RepoRoot outside repo = nil, want failure")
	}
}

func TestCreateBranchAndBranchExists(t *testing.T) {
	repo := initRepo(t)
	r := New()

	if ok, err := r.BranchExists(repo, "issue/1-x"); err != nil || ok {
		t.Fatalf("BranchExists before = %v, %v; want false, nil", ok, err)
	}
	if err := r.CreateBranch(repo, "issue/1-x"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if ok, err := r.BranchExists(repo, "issue/1-x"); err != nil || !ok {
		t.Errorf("BranchExists after = %v, %v; want true, nil", ok, err)
	}
	// Idempotent: re-creating the same branch succeeds.
	if err := r.CreateBranch(repo, "issue/1-x"); err != nil {
		t.Errorf("re-CreateBranch: %v, want idempotent success", err)
	}
}

func TestWorktreeAddListRemoveRoundTrip(t *testing.T) {
	repo := initRepo(t)
	r := New()
	wt := filepath.Join(t.TempDir(), "wt-1")

	if err := r.WorktreeAdd(repo, wt, "issue/1-x"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if st, err := os.Stat(filepath.Join(wt, "f.txt")); err != nil || st.IsDir() {
		t.Fatalf("worktree checkout missing f.txt: %v", err)
	}
	list, err := r.WorktreeList(repo)
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	found := false
	for _, w := range list {
		if w.Path == wt {
			found = true
		}
	}
	if !found {
		t.Errorf("WorktreeList = %+v, want %q listed", list, wt)
	}
	if err := r.WorktreeRemove(repo, wt, false); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present after remove: %v", err)
	}
	// Idempotent: removing again succeeds.
	if err := r.WorktreeRemove(repo, wt, false); err != nil {
		t.Errorf("re-WorktreeRemove: %v, want idempotent success", err)
	}
}

func TestWorktreeRemoveRefusesNonWorktree(t *testing.T) {
	repo := initRepo(t)
	r := New()

	// The repo root itself must never be removed.
	if err := r.WorktreeRemove(repo, repo, true); err == nil {
		t.Error("WorktreeRemove(repo root, force) = nil, want refusal")
	}
	// An unrelated directory must never be removed.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.WorktreeRemove(repo, other, true); err == nil {
		t.Error("WorktreeRemove(unrelated dir, force) = nil, want refusal")
	}
	if _, err := os.Stat(filepath.Join(other, "keep.txt")); err != nil {
		t.Error("unrelated dir was touched by WorktreeRemove")
	}
}

func TestMissingGitReturnsTypedError(t *testing.T) {
	testutil.EmptyBin(t) // no git on PATH

	if _, err := New().RepoRoot("."); err == nil {
		t.Error("RepoRoot with no git = nil, want typed not-found error")
	} else if !ports.IsBinaryNotFound(err) {
		t.Errorf("RepoRoot error = %v (%T), want BinaryNotFoundError", err, err)
	}
}

func TestBranchName(t *testing.T) {
	cases := map[string]string{
		"ports＋Adapter（gh・herdr・エージェント抽象化）": "issue/36-ports-adapter-gh-herdr",
		"Simple Title Here!": "issue/1-simple-title-here",
		"UPPER and  spaces":  "issue/2-upper-and-spaces",
	}
	_ = cases
	got := BranchName(36, "ports＋Adapter（gh・herdr・エージェント抽象化）")
	if !strings.HasPrefix(got, "issue/36-") || len(got) > len("issue/36-")+30 {
		t.Errorf("BranchName = %q, want issue/36-<slug≤30>", got)
	}
	if got != "issue/36-ports-adapter-gh-herdr" {
		t.Errorf("BranchName = %q, want issue/36-ports-adapter-gh-herdr", got)
	}
	if got := BranchName(1, "Simple Title Here!"); got != "issue/1-simple-title-here" {
		t.Errorf("BranchName = %q", got)
	}
	if got := BranchName(9, ""); got != "issue/9-untitled" {
		t.Errorf("BranchName empty title = %q, want issue/9-untitled", got)
	}
}
