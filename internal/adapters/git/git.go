// Package git implements worktree/branch operations via the `git` CLI
// (issue #40). Destructive calls are guarded: WorktreeRemove only touches
// paths registered as worktrees of the target repo, never the main
// worktree or unrelated directories.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// Runner calls the `git` CLI. Zero value is usable.
type Runner struct {
	// Bin is the git binary name or path. Empty means "git".
	Bin string
	// LookPath resolves the binary; defaults to exec.LookPath.
	LookPath func(name string) (string, error)
}

// New returns a Runner with defaults.
func New() *Runner { return &Runner{} }

func (r *Runner) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	return "git"
}

func (r *Runner) lookPath() func(string) (string, error) {
	if r.LookPath != nil {
		return r.LookPath
	}
	return exec.LookPath
}

func (r *Runner) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	bin := r.bin()
	if _, err := r.lookPath()(bin); err != nil {
		return nil, &ports.BinaryNotFoundError{Binary: bin}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// RepoRoot returns the repository top level containing dir.
func (r *Runner) RepoRoot(dir string) (string, error) {
	out, err := r.run(context.Background(), dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// BranchExists reports whether branch exists in the repo.
func (r *Runner) BranchExists(repoDir, branch string) (bool, error) {
	_, err := r.run(context.Background(), repoDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	// rev-parse --verify --quiet exits 1 for missing refs; distinguish that
	// from real failures by checking the ref directly.
	if strings.Contains(err.Error(), "exit status 1") {
		return false, nil
	}
	return false, err
}

// CreateBranch checks out branch (creating or resetting it, idempotent).
func (r *Runner) CreateBranch(repoDir, branch string) error {
	_, err := r.run(context.Background(), repoDir, "checkout", "-B", branch)
	return err
}

// CheckoutBranch checks out existing branch without resetting or creating.
func (r *Runner) CheckoutBranch(repoDir, branch string) error {
	_, err := r.run(context.Background(), repoDir, "checkout", branch)
	return err
}

// ListBranches returns all local branch names in repoDir.
func (r *Runner) ListBranches(repoDir string) ([]string, error) {
	out, err := r.run(context.Background(), repoDir, "branch", "--format=%(refname:short)")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

// WorktreeInfo is one entry of `git worktree list --porcelain`.
type WorktreeInfo struct {
	Path   string
	Branch string
	Bare   bool
}

// WorktreeList lists worktrees registered with the repo.
func (r *Runner) WorktreeList(repoDir string) ([]WorktreeInfo, error) {
	out, err := r.run(context.Background(), repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []WorktreeInfo
	var cur WorktreeInfo
	flush := func() {
		if cur.Path != "" {
			list = append(list, cur)
		}
		cur = WorktreeInfo{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(line, "branch ")
		case line == "bare":
			cur.Bare = true
		case line == "":
			flush()
		}
	}
	flush()
	return list, nil
}

// WorktreeAdd creates a worktree at path on branch (creating the branch
// when needed). Re-adding an existing registered path is idempotent.
func (r *Runner) WorktreeAdd(repoDir, path, branch string) error {
	for _, w := range mustList(r, repoDir) {
		if samePath(w.Path, path) {
			return nil // already registered: idempotent success
		}
	}
	exists, err := r.BranchExists(repoDir, branch)
	if err != nil {
		return err
	}
	args := []string{"worktree", "add", path}
	if !exists {
		args = append(args, "-b", branch)
	} else {
		args = append(args, branch)
	}
	_, err = r.run(context.Background(), repoDir, args...)
	return err
}

func mustList(r *Runner, repoDir string) []WorktreeInfo {
	list, _ := r.WorktreeList(repoDir)
	return list
}

// WorktreeRemove removes the worktree at path. It refuses paths that are
// not registered worktrees of repoDir, and the main worktree, even with
// force=true. Removing an already-gone path is idempotent success.
func (r *Runner) WorktreeRemove(repoDir, path string, force bool) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("git worktree remove: %w", err)
	}
	list, err := r.WorktreeList(repoDir)
	if err != nil {
		return err
	}
	registered := false
	for _, w := range list[1:] { // list[0] is the main worktree: never removable
		if samePath(w.Path, abs) {
			registered = true
			break
		}
	}
	if !registered {
		if _, statErr := os.Stat(abs); os.IsNotExist(statErr) {
			return nil // already gone: idempotent success
		}
		return fmt.Errorf("git worktree remove: refusing %q (not a registered worktree of %s)", abs, repoDir)
	}
	args := []string{"worktree", "remove", abs}
	if force {
		args = append(args, "--force")
	}
	_, err = r.run(context.Background(), repoDir, args...)
	return err
}

// OriginURL returns remote.origin.url ("" when unset).
func (r *Runner) OriginURL(repoDir string) string {
	out, err := r.run(context.Background(), repoDir, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RepoSlug reduces a remote URL to "owner/repo" for `gh api repos/<slug>`.
// Accepts https://host/o/r(.git), git@host:o/r(.git), ssh://git@host/o/r,
// host/o/r and a bare o/r. Anything else (including "local" and "") is "".
func RepoSlug(remote string) string {
	u := strings.TrimSpace(remote)
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.Replace(u, ":", "/", 1)
	parts := strings.Split(u, "/")
	for _, p := range parts {
		if p == "" {
			return ""
		}
	}
	switch {
	case len(parts) >= 3:
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	case len(parts) == 2 && !strings.Contains(parts[0], "."):
		return u
	}
	return ""
}

func samePath(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		return a == b
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return a == b
	}
	return aa == bb
}

// BranchName builds "issue/<n>-<slug>" like legacy bin/hgf: lowercase,
// non-alphanumeric runs become one '-', trimmed, capped at 30 chars.
func BranchName(number int, title string) string {
	slug := strings.ToLower(title)
	var sb strings.Builder
	dash := false
	for _, c := range slug {
		isAlnum := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if isAlnum {
			sb.WriteRune(c)
			dash = false
			continue
		}
		if !dash && sb.Len() > 0 {
			sb.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(sb.String(), "-")
	if len(s) > 30 {
		s = strings.Trim(s[:30], "-")
	}
	if s == "" {
		s = "untitled"
	}
	return fmt.Sprintf("issue/%d-%s", number, s)
}
