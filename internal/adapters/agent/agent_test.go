package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// mkDummyAgent installs a dummy agent exiting with exitCode.
func mkDummyAgent(t *testing.T, name string, exitCode int) string {
	t.Helper()
	return testutil.InstallDummy(t, name, "exit "+strconv.Itoa(exitCode))
}

func TestLaunch_AgyUsesInteractiveFlag(t *testing.T) {
	logPath := mkDummyAgent(t, "agy", 0)

	if err := New().Launch(context.Background(), "agy", "do stuff"); err != nil {
		t.Fatalf("Launch agy: %v", err)
	}
	log := testutil.LogText(t, logPath)
	if !strings.Contains(log, "<-i>") {
		t.Errorf("agy argv missing -i/--prompt-interactive flag, got:\n%s", log)
	}
	if !strings.Contains(log, "<do stuff>") {
		t.Errorf("agy argv missing prompt, got:\n%s", log)
	}
}

func TestLaunch_BatchMode(t *testing.T) {
	logPath := mkDummyAgent(t, "agy", 0)

	if err := New().LaunchForMode(context.Background(), "agy", "do batch", "batch"); err != nil {
		t.Fatalf("LaunchForMode agy batch: %v", err)
	}
	log := testutil.LogText(t, logPath)
	if !strings.Contains(log, "<--dangerously-skip-permissions>") {
		t.Errorf("agy batch argv missing --dangerously-skip-permissions, got:\n%s", log)
	}
	if !strings.Contains(log, "<-p>") {
		t.Errorf("agy batch argv missing -p, got:\n%s", log)
	}
}

func TestLaunch_OtherAgentUsesPositionalPrompt(t *testing.T) {
	for _, name := range []string{"devin", "opencode", "claude"} {
		logPath := mkDummyAgent(t, name, 0)

		if err := New().Launch(context.Background(), name, "do stuff"); err != nil {
			t.Fatalf("Launch %s: %v", name, err)
		}
		log := testutil.LogText(t, logPath)
		if !strings.Contains(log, "<do stuff>") {
			t.Errorf("Launch %s argv missing positional prompt, got:\n%s", name, log)
		}
		if strings.Contains(log, "<-i>") {
			t.Errorf("Launch %s must not use agy-only -i flag, got:\n%s", name, log)
		}
	}
}

func TestLaunch_EmptyAgentDefaultsToAgy(t *testing.T) {
	logPath := mkDummyAgent(t, "agy", 0)

	if err := New().Launch(context.Background(), "", "do stuff"); err != nil {
		t.Fatalf("Launch default: %v", err)
	}
	log := testutil.LogText(t, logPath)
	if !strings.Contains(log, "<-i>") || !strings.Contains(log, "<do stuff>") {
		t.Errorf("default agent argv = \n%s, want agy -i <prompt>", log)
	}
}

func TestLaunch_MissingAgentReturnsTypedError(t *testing.T) {
	testutil.EmptyBin(t) // no agent binaries on PATH

	err := New().Launch(context.Background(), "agy", "do stuff")
	if err == nil {
		t.Fatal("Launch with no agent binary = nil error, want typed not-found error")
	}
	if !ports.IsBinaryNotFound(err) {
		t.Fatalf("Launch error = %v (%T), want BinaryNotFoundError for graceful fallback", err, err)
	}
}

func TestLaunch_NonZeroExitPropagates(t *testing.T) {
	mkDummyAgent(t, "agy", 2)

	err := New().Launch(context.Background(), "agy", "do stuff")
	if err == nil {
		t.Fatal("Launch with failing agent = nil error, want exit status propagation")
	}
	if ports.IsBinaryNotFound(err) {
		t.Fatalf("Launch error = %v, must not be classified as missing binary", err)
	}
	if !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("Launch error = %q, want exit status surfaced", err)
	}
}

func TestResolve(t *testing.T) {
	if got := Resolve(""); got != DefaultAgent {
		t.Errorf("Resolve(\"\") = %q, want default %q", got, DefaultAgent)
	}
	if got := Resolve("devin"); got != "devin" {
		t.Errorf("Resolve(devin) = %q, want devin", got)
	}
	if DefaultAgent != "agy" {
		t.Errorf("DefaultAgent = %q, want agy (legacy bin/hgf default)", DefaultAgent)
	}
}

func TestCommandString_BuildsReviewableCommand(t *testing.T) {
	// Legacy bin/hgf prepared these exact command strings in the new pane
	// for human review (not auto-sent).
	cases := map[string]string{
		"agy":      `agy -i "hello"`,
		"devin":    `devin "hello"`,
		"opencode": `opencode "hello"`,
		"claude":   `claude "hello"`,
	}
	for agentName, want := range cases {
		if got := CommandString(agentName, "hello"); got != want {
			t.Errorf("CommandString(%s) = %q, want %q", agentName, got, want)
		}
	}
	if got := CommandString("", "hello"); got != `agy -i "hello"` {
		t.Errorf("CommandString default = %q, want agy form", got)
	}
}

func TestCommandStringForMode(t *testing.T) {
	cases := []struct {
		agent   string
		mode    string
		want    string
		wantErr bool
	}{
		{"agy", "interactive", `agy -i "hello"`, false},
		{"agy", "batch", `agy --dangerously-skip-permissions -p "hello"`, false},
		{"agy", "dangerous", `agy --dangerously-skip-permissions -p "hello"`, false},
		{"claude", "batch", `claude -p --dangerously-skip-permissions "hello"`, false},
		{"codex", "batch", `codex exec --dangerously-bypass-approvals-and-sandbox "hello"`, false},
		{"gemini", "batch", `gemini -y "hello"`, false},
		{"opencode", "batch", `opencode run --auto "hello"`, false},
		{"devin", "batch", `devin --permission-mode dangerous -p "hello"`, false},
		{"unknown", "batch", "", true},
	}
	for _, tc := range cases {
		got, err := CommandStringForMode(tc.agent, "hello", tc.mode)
		if tc.wantErr {
			if err == nil {
				t.Errorf("CommandStringForMode(%s, %s) wanted error, got nil", tc.agent, tc.mode)
			}
			continue
		}
		if err != nil {
			t.Errorf("CommandStringForMode(%s, %s) unexpected error: %v", tc.agent, tc.mode, err)
			continue
		}
		if got != tc.want {
			t.Errorf("CommandStringForMode(%s, %s) = %q, want %q", tc.agent, tc.mode, got, tc.want)
		}
	}
}

func TestArgvForMode(t *testing.T) {
	cases := []struct {
		agent   string
		mode    string
		want    []string
		wantErr bool
	}{
		{"agy", "interactive", []string{"-i", "hello"}, false},
		{"agy", "batch", []string{"--dangerously-skip-permissions", "-p", "hello"}, false},
		{"claude", "batch", []string{"-p", "--dangerously-skip-permissions", "hello"}, false},
		{"codex", "batch", []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "hello"}, false},
		{"gemini", "batch", []string{"-y", "hello"}, false},
		{"unknown", "batch", nil, true},
	}
	for _, tc := range cases {
		got, err := ArgvForMode(tc.agent, "hello", tc.mode)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ArgvForMode(%s, %s) wanted error, got nil", tc.agent, tc.mode)
			}
			continue
		}
		if err != nil {
			t.Errorf("ArgvForMode(%s, %s) unexpected error: %v", tc.agent, tc.mode, err)
			continue
		}
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("ArgvForMode(%s, %s) = %v, want %v", tc.agent, tc.mode, got, tc.want)
		}
	}
}

func TestLaunch_RunsInSpecifiedDir(t *testing.T) {
	tmpDir := t.TempDir()
	pwdLog := filepath.Join(tmpDir, "pwd.log")
	testutil.InstallDummy(t, "agy", "pwd >> "+pwdLog+"\nexit 0")

	targetDir := filepath.Join(tmpDir, "worktree")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}

	l := &Launcher{Dir: targetDir}
	if err := l.Launch(context.Background(), "agy", "test"); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	content, err := os.ReadFile(pwdLog)
	if err != nil {
		t.Fatalf("ReadFile pwd.log: %v", err)
	}
	gotDir := strings.TrimSpace(string(content))
	realTarget, _ := filepath.EvalSymlinks(targetDir)
	realGot, _ := filepath.EvalSymlinks(gotDir)
	if realGot != realTarget {
		t.Errorf("Launcher ran in %q, want %q", gotDir, targetDir)
	}
}

func TestLaunchInDir_AllKnownAgents(t *testing.T) {
	tmpDir := t.TempDir()

	for _, name := range KnownAgents {
		pwdLog := filepath.Join(tmpDir, name+"_pwd.log")
		testutil.InstallDummy(t, name, "pwd >> "+pwdLog+"\nexit 0")

		targetDir := filepath.Join(tmpDir, "worktree-"+name)
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			t.Fatal(err)
		}

		l := New()
		if err := l.LaunchInDir(context.Background(), targetDir, name, "test", "interactive"); err != nil {
			t.Fatalf("LaunchInDir %s: %v", name, err)
		}

		content, err := os.ReadFile(pwdLog)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", pwdLog, err)
		}
		gotDir := strings.TrimSpace(string(content))
		realTarget, _ := filepath.EvalSymlinks(targetDir)
		realGot, _ := filepath.EvalSymlinks(gotDir)
		if realGot != realTarget {
			t.Errorf("LaunchInDir for %s ran in %q, want %q", name, gotDir, targetDir)
		}
	}
}

