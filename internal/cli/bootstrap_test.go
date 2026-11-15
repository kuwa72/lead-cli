package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/testutil"
)

func installDummyAgyForBootstrap(t *testing.T, dir string) string {
	t.Helper()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(binDir, "agy")
	body := `#!/bin/sh
echo "DUMMY_AGY_PWD:$(pwd)"
echo "DUMMY_AGY_ARGS:$*"
input=$(cat)
echo "DUMMY_AGY_STDIN:$input"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestBootstrapCommand_DryRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAD_STATE_FILE", filepath.Join(t.TempDir(), "wf.json"))
	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", Deps{
		Gh:      &testutil.FakeGhClient{},
		Git:     &fakeGitRunner{},
		WorkDir: dir,
		LookPath: func(string) (string, error) {
			t.Fatal("DryRun must not look up agent binary")
			return "", nil
		},
	})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"bootstrap", "--dry-run", "sample todo cli"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bootstrap --dry-run = %v\nout=%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "agy -i") || !strings.Contains(got, "sample todo cli") {
		t.Errorf("dry-run output = %q, want command preview and goal", got)
	}
}

func TestBootstrapCommand_LaunchesAgentWithStdin(t *testing.T) {
	dir := t.TempDir()
	agyPath := installDummyAgyForBootstrap(t, t.TempDir())
	t.Setenv("LEAD_STATE_FILE", filepath.Join(t.TempDir(), "wf.json"))

	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", Deps{
		Gh:      &testutil.FakeGhClient{},
		Git:     &fakeGitRunner{},
		WorkDir: dir,
		Stdin:   strings.NewReader("go backend\n"),
		LookPath: func(name string) (string, error) {
			if name == "agy" {
				return agyPath, nil
			}
			return "", os.ErrNotExist
		},
	})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"bootstrap", "api server"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bootstrap = %v\nout=%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "DUMMY_AGY_ARGS:-i") || !strings.Contains(got, "api server") {
		t.Errorf("output = %q, want dummy agy invoked with -i and goal", got)
	}
	if !strings.Contains(got, "DUMMY_AGY_STDIN:go backend") {
		t.Errorf("output = %q, want stdin forwarded", got)
	}
}

func TestBareLeadInteractiveTTY_PromptsBootstrapAndLaunches(t *testing.T) {
	dir := t.TempDir()
	agyPath := installDummyAgyForBootstrap(t, t.TempDir())
	t.Setenv("LEAD_STATE_FILE", filepath.Join(t.TempDir(), "wf.json"))
	t.Setenv("LEAD_TEST_INBOX_KEYS", "")
	t.Setenv("LEAD_TEST_TTY", "1")

	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", Deps{
		Gh:      &testutil.FakeGhClient{},
		Git:     &fakeGitRunner{},
		WorkDir: dir,
		Stdin:   strings.NewReader("y\nagent-turn\n"),
		LookPath: func(name string) (string, error) {
			if name == "agy" {
				return agyPath, nil
			}
			return "", os.ErrNotExist
		},
	})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{})
	if err := root.Execute(); err != nil {
		t.Fatalf("bare lead interactive bootstrap = %v\nout=%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "[Y/n]") {
		t.Errorf("output = %q, want confirmation prompt [Y/n]", got)
	}
	if !strings.Contains(got, "DUMMY_AGY_ARGS:-i") || !strings.Contains(got, "DUMMY_AGY_STDIN:agent-turn") {
		t.Errorf("output = %q, want agent launched and remaining stdin forwarded", got)
	}
}

func TestBareLeadInteractiveTTY_DeclinedDoesNotLaunchBootstrap(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAD_STATE_FILE", filepath.Join(t.TempDir(), "wf.json"))
	t.Setenv("LEAD_TEST_INBOX_KEYS", "")
	t.Setenv("LEAD_TEST_TTY", "1")

	launched := false
	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", Deps{
		Gh:      &testutil.FakeGhClient{},
		Git:     &fakeGitRunner{},
		WorkDir: dir,
		Stdin:   strings.NewReader("n\n"),
		LookPath: func(string) (string, error) {
			launched = true
			return "", os.ErrNotExist
		},
	})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{})
	if err := root.Execute(); err != nil {
		t.Fatalf("bare lead declined = %v\nout=%s", err, out.String())
	}
	if launched {
		t.Fatal("agent binary lookup ran despite user declining with 'n'")
	}
	if !strings.Contains(out.String(), "lead bootstrap") || !strings.Contains(out.String(), "Cancelled.") {
		t.Errorf("output = %q, want Cancelled hint pointing at lead bootstrap", out.String())
	}
}

func TestBootstrapCommand_FromSubdirLaunchesAtRepoRoot(t *testing.T) {
	repoRoot := t.TempDir()
	subDir := filepath.Join(repoRoot, "nested", "pkg")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agyPath := installDummyAgyForBootstrap(t, t.TempDir())
	t.Setenv("LEAD_STATE_FILE", filepath.Join(t.TempDir(), "wf.json"))

	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", Deps{
		Gh:      &testutil.FakeGhClient{},
		Git:     &fakeGitRunner{root: repoRoot},
		WorkDir: subDir,
		Stdin:   strings.NewReader("go\n"),
		LookPath: func(name string) (string, error) {
			if name == "agy" {
				return agyPath, nil
			}
			return "", os.ErrNotExist
		},
	})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"bootstrap", "subdir test"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bootstrap from subdir = %v\nout=%s", err, out.String())
	}
	got := out.String()
	wantPwd := "DUMMY_AGY_PWD:" + repoRoot
	if !strings.Contains(got, wantPwd) {
		t.Errorf("output = %q, want agent cwd %q (not subdir %q)", got, wantPwd, subDir)
	}
}

func TestBareLeadInteractiveTTY_FromSubdirUsesRepoRoot(t *testing.T) {
	repoRoot := t.TempDir()
	subDir := filepath.Join(repoRoot, "nested", "pkg")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agyPath := installDummyAgyForBootstrap(t, t.TempDir())
	t.Setenv("LEAD_STATE_FILE", filepath.Join(t.TempDir(), "wf.json"))
	t.Setenv("LEAD_TEST_INBOX_KEYS", "")
	t.Setenv("LEAD_TEST_TTY", "1")

	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", Deps{
		Gh:      &testutil.FakeGhClient{},
		Git:     &fakeGitRunner{root: repoRoot},
		WorkDir: subDir,
		Stdin:   strings.NewReader("y\nnext\n"),
		LookPath: func(name string) (string, error) {
			if name == "agy" {
				return agyPath, nil
			}
			return "", os.ErrNotExist
		},
	})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{})
	if err := root.Execute(); err != nil {
		t.Fatalf("bare lead from subdir = %v\nout=%s", err, out.String())
	}
	got := out.String()
	wantPwd := "DUMMY_AGY_PWD:" + repoRoot
	if !strings.Contains(got, wantPwd) {
		t.Errorf("output = %q, want agent cwd %q (not subdir %q)", got, wantPwd, subDir)
	}
}

func installDummyAgentArgRecorder(t *testing.T, dir, name string) string {
	t.Helper()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(binDir, name)
	body := `#!/bin/sh
printf 'ARG1:%s\n' "$1"
printf 'ARG2:%s\n' "$2"
input=$(cat)
printf 'STDIN:%s\n' "$input"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestBootstrapCommand_DevinAndOpencodeFlags(t *testing.T) {
	for _, tc := range []struct {
		agent    string
		wantArg1 string
	}{
		{"devin", "--"},
		{"opencode", "--prompt"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			dir := t.TempDir()
			binPath := installDummyAgentArgRecorder(t, t.TempDir(), tc.agent)
			t.Setenv("LEAD_STATE_FILE", filepath.Join(t.TempDir(), "wf.json"))

			root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-07", Deps{
				Gh:      &testutil.FakeGhClient{},
				Git:     &fakeGitRunner{},
				WorkDir: dir,
				Stdin:   strings.NewReader("interactive reply\n"),
				LookPath: func(name string) (string, error) {
					if name == tc.agent {
						return binPath, nil
					}
					return "", os.ErrNotExist
				},
			})
			var out strings.Builder
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"bootstrap", "--agent", tc.agent, "read requirements.txt"})
			if err := root.Execute(); err != nil {
				t.Fatalf("bootstrap --agent %s = %v\nout=%s", tc.agent, err, out.String())
			}
			got := out.String()
			if !strings.Contains(got, "ARG1:"+tc.wantArg1+"\n") {
				t.Errorf("%s ARG1 = %q, want %q", tc.agent, got, "ARG1:"+tc.wantArg1)
			}
			if !strings.Contains(got, "read requirements.txt") || !strings.Contains(got, "STDIN:interactive reply") {
				t.Errorf("%s output missing goal or stdin: %q", tc.agent, got)
			}
		})
	}
}
