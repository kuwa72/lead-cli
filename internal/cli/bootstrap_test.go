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
	if !strings.Contains(out.String(), "lead bootstrap") {
		t.Errorf("output = %q, want hint pointing at lead bootstrap", out.String())
	}
}
