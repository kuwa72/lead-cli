package agent

import (
	"context"
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
