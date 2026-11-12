package bootstrap

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
)

type fakeLauncher struct {
	calls int
	agent string
	prompt string
	mode  string
	err   error
}

func (f *fakeLauncher) LaunchForMode(_ context.Context, agentName, prompt, mode string) error {
	f.calls++
	f.agent = agentName
	f.prompt = prompt
	f.mode = mode
	return f.err
}

func initTempRepo(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q", "-b", "main")
	if remote != "" {
		runGit("remote", "add", "origin", remote)
	}
	return dir
}

func TestInspect_States(t *testing.T) {
	g := &git.Runner{}

	t.Run("empty dir is uninitialized", func(t *testing.T) {
		st := Inspect(t.TempDir(), g)
		if st.InGitRepo || st.RepoSlug != "" || !st.IsUninitialized() {
			t.Fatalf("Inspect = %+v, want uninitialized non-git", st)
		}
	})

	t.Run("git repo without origin is uninitialized", func(t *testing.T) {
		dir := initTempRepo(t, "")
		st := Inspect(dir, g)
		if !st.InGitRepo || st.RepoSlug != "" || !st.IsUninitialized() {
			t.Fatalf("Inspect = %+v, want InGitRepo=true RepoSlug=\"\" IsUninitialized=true", st)
		}
	})

	t.Run("git repo with non-github origin is uninitialized", func(t *testing.T) {
		dir := initTempRepo(t, "https://gitlab.com/org/app.git")
		st := Inspect(dir, g)
		if !st.InGitRepo || st.RepoSlug != "" || !st.IsUninitialized() {
			t.Fatalf("Inspect = %+v, want non-github origin treated as uninitialized", st)
		}
	})

	t.Run("git repo with github origin is initialized", func(t *testing.T) {
		dir := initTempRepo(t, "https://github.com/kuwa72/sample-app.git")
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Agents\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".github", "workflows", "ci.yml"), []byte("on: push\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st := Inspect(dir, g)
		if !st.InGitRepo || st.RepoSlug != "kuwa72/sample-app" || st.IsUninitialized() {
			t.Fatalf("Inspect = %+v, want initialized GitHub repo", st)
		}
		if !st.HasAgentsMD || !st.HasWorkflows {
			t.Fatalf("Inspect = %+v, want HasAgentsMD=true HasWorkflows=true", st)
		}
	})
}

func TestPrompt_IncludesStateAndGoal(t *testing.T) {
	p := Prompt(Status{Dir: "/tmp/newapp"}, "build a snake game")
	for _, want := range []string{"/tmp/newapp", "build a snake game", "lead enable", "AGENTS.md", ".github/workflows"} {
		if !strings.Contains(p, want) {
			t.Errorf("Prompt missing %q:\n%s", want, p)
		}
	}
}

func TestRun_DryRunPrintsPreviewWithoutLaunching(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	l := &fakeLauncher{}
	err := Run(context.Background(), Options{
		Dir:      dir,
		Goal:     "build cli",
		Agent:    "agy",
		DryRun:   true,
		Git:      &git.Runner{},
		Launcher: l,
		Out:      &out,
	})
	if err != nil {
		t.Fatalf("Run(DryRun) = %v", err)
	}
	if l.calls != 0 {
		t.Errorf("DryRun launched agent %d times, want 0", l.calls)
	}
	s := out.String()
	if !strings.Contains(s, "agy -i") || !strings.Contains(s, "build cli") {
		t.Errorf("DryRun output = %q, want agent command and goal", s)
	}
}

func TestRun_LaunchesInteractiveAgent(t *testing.T) {
	dir := t.TempDir()
	l := &fakeLauncher{}
	var out bytes.Buffer
	err := Run(context.Background(), Options{
		Dir:      dir,
		Goal:     "create web api",
		Git:      &git.Runner{},
		Launcher: l,
		Out:      &out,
	})
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if l.calls != 1 {
		t.Fatalf("calls = %d, want 1", l.calls)
	}
	if l.agent != "agy" || l.mode != "interactive" || !strings.Contains(l.prompt, "create web api") {
		t.Errorf("launched = (%q, %q, %q), want agy / interactive / goal", l.agent, l.mode, l.prompt)
	}
}
