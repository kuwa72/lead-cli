package agent

import (
	"errors"
	"reflect"
	"testing"
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
		{"agy", []string{"agy", "--dangerously-skip-permissions", "-p", p}},
		{"", []string{"agy", "--dangerously-skip-permissions", "-p", p}},
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

func TestHeadlessArgv_UnsupportedAgents(t *testing.T) {
	for _, name := range []string{"nope", "cursor"} {
		_, err := HeadlessArgv(name, "x")
		if !errors.Is(err, ErrHeadlessUnsupported) {
			t.Errorf("%q: err = %v, want ErrHeadlessUnsupported", name, err)
		}
	}
}
