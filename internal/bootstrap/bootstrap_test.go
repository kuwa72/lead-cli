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
		if !st.InGitRepo || !st.HasOrigin || st.RepoSlug != "" || !st.IsUninitialized() {
			t.Fatalf("Inspect = %+v, want InGitRepo=true HasOrigin=true RepoSlug=\"\" IsUninitialized=true", st)
		}
	})

	t.Run("lookalike host containing github.com is not treated as GitHub origin", func(t *testing.T) {
		for _, remote := range []string{
			"https://notgithub.com/org/app.git",
			"https://github.com.evil.example/org/app.git",
			"https://evil.example/github.com/org/app.git",
			"git@github.com.evil.example:org/app.git",
		} {
			dir := initTempRepo(t, remote)
			st := Inspect(dir, g)
			if st.RepoSlug != "" || !st.IsUninitialized() {
				t.Fatalf("Inspect(%q) = %+v, want lookalike host treated as uninitialized", remote, st)
			}
		}
	})

	t.Run("subdir inspects RepoRoot for AGENTS.md and WorkDir", func(t *testing.T) {
		dir := initTempRepo(t, "")
		sub := filepath.Join(dir, "nested", "pkg")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Agents\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		st := Inspect(sub, g)
		if !st.InGitRepo || !st.HasAgentsMD || !st.IsUninitialized() {
			t.Fatalf("Inspect(subdir) = %+v, want InGitRepo=true HasAgentsMD=true IsUninitialized=true", st)
		}
		if st.RepoRoot == "" || st.WorkDir() == sub {
			t.Fatalf("WorkDir() = %q (RepoRoot=%q), want resolved repo root != subdir %q", st.WorkDir(), st.RepoRoot, sub)
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

func TestPrompt_RedactsOriginCredentialsAndHandlesNonGitHubOrigin(t *testing.T) {
	g := &git.Runner{}
	credDir := initTempRepo(t, "https://x-access-token:ghp_secret999@github.com/kuwa72/clean-app.git")
	stCred := Inspect(credDir, g)
	if stCred.RepoSlug != "kuwa72/clean-app" || stCred.IsUninitialized() {
		t.Fatalf("Inspect(cred) = %+v, want RepoSlug=kuwa72/clean-app", stCred)
	}
	pCred := Prompt(stCred, "")
	if !strings.Contains(pCred, "kuwa72/clean-app") {
		t.Errorf("Prompt missing clean slug:\n%s", pCred)
	}
	for _, secret := range []string{"ghp_secret999", "x-access-token"} {
		if strings.Contains(pCred, secret) {
			t.Errorf("Prompt leaked credential %q:\n%s", secret, pCred)
		}
	}

	nonGhDir := initTempRepo(t, "https://gitlab.com/org/app.git")
	pNonGh := Prompt(Inspect(nonGhDir, g), "")
	if !strings.Contains(pNonGh, "non-GitHub origin remote") || !strings.Contains(pNonGh, "set-url origin") {
		t.Errorf("Prompt for non-GitHub origin should explain existing origin and set-url:\n%s", pNonGh)
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
