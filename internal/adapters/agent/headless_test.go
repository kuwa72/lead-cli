package agent

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// Flags below were confirmed against each CLI's `--help` on 2026-09-08
// (AGENTS.md test rule 2: never guess external CLI flags).
func TestHeadlessArgv_MapsVerifiedFlags(t *testing.T) {
	const p = "Issue #7: do it"
	cases := []struct {
		agent string
		want  []string
	}{
		{"claude", []string{"claude", "-p", "--dangerously-skip-permissions", p}},
		{"codex", []string{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", p}},
		{"agy", []string{"agy", "--dangerously-skip-permissions", "-p", "--print-timeout", "24h0m0s", p}},
		{"", []string{"agy", "--dangerously-skip-permissions", "-p", "--print-timeout", "24h0m0s", p}},
		{"gemini", []string{"gemini", "-y", p}},
		{"opencode", []string{"opencode", "run", "--auto", p}},
		{"devin", []string{"devin", "--permission-mode", "dangerous", "-p", p}},
	}
	for _, c := range cases {
		got, err := HeadlessArgv(c.agent, p)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", c.agent, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: argv = %q, want %q", c.agent, got, c.want)
		}
	}
}

// issue #204: agy's own --print-timeout default is 5m, too short for
// headless spec drafting. Since issue #215 it is only a backstop — lead's
// stall watchdog ends runs — so it must sit far above the watchdog limits.
func TestHeadlessArgv_AgyPrintTimeout(t *testing.T) {
	argv, err := HeadlessArgv("agy", "p")
	if err != nil {
		t.Fatal(err)
	}
	timeout := argvValue(argv, "--print-timeout")
	if timeout == "" {
		t.Fatalf("agy argv missing --print-timeout: %q", argv)
	}
	d, err := time.ParseDuration(timeout)
	if err != nil || d < 12*time.Hour {
		t.Errorf("--print-timeout = %q, want a backstop well over the 3h max-runtime default", timeout)
	}
}

func argvValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func TestHeadlessArgv_UnsupportedAgents(t *testing.T) {
	for _, name := range []string{"nope", "cursor"} {
		_, err := HeadlessArgv(name, "x")
		if !errors.Is(err, ErrHeadlessUnsupported) {
			t.Errorf("%q: err = %v, want ErrHeadlessUnsupported", name, err)
		}
	}
}
