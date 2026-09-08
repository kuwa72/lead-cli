// Package agent implements ports.AgentLauncher: direct (inline) execution
// of coding agents, plus agent resolution and reviewable command strings.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// DefaultAgent is the standard agent (legacy bin/hgf DEFAULT_AGENT).
const DefaultAgent = "agy"

// KnownAgents lists the agents lead can dispatch to
// (docs/design.md §4 plus legacy bin/hgf options).
var KnownAgents = []string{"agy", "claude", "codex", "devin", "opencode", "gemini"}

// Resolve maps a requested agent name to the effective one.
// Empty requests fall back to DefaultAgent.
func Resolve(requested string) string {
	if requested == "" {
		return DefaultAgent
	}
	return requested
}

// IsKnown reports whether name is a recognized agent.
func IsKnown(name string) bool {
	for _, known := range KnownAgents {
		if name == known {
			return true
		}
	}
	return false
}

// Argv builds the exec argv for launching agent with prompt.
// agy takes interactive prompts via -i/--prompt-interactive (closed #19);
// other agents take the prompt positionally.
func Argv(agentName, prompt string) []string {
	name := Resolve(agentName)
	if name == "agy" {
		return []string{"-i", prompt}
	}
	return []string{prompt}
}

// ErrHeadlessUnsupported reports an agent with no verified headless
// (non-interactive, auto-approve) invocation.
var ErrHeadlessUnsupported = errors.New("agent has no verified headless mode")

// HeadlessArgv builds the full argv (binary first) that runs the agent
// non-interactively with permissions auto-approved, for `lead dispatch`
// (docs/rfc-inbox-ux.md §7). Every mapping below was confirmed against the
// CLI's own `--help` on 2026-09-08; do not add entries from memory.
//
//	claude   -p --dangerously-skip-permissions <prompt>
//	codex    exec --dangerously-bypass-approvals-and-sandbox <prompt>
//	agy      --dangerously-skip-permissions -p <prompt>
//	gemini   -y <prompt>            (positional prompt; -p is deprecated)
//	opencode run --auto <prompt>
//	devin    --permission-mode dangerous -p <prompt>
//	         (help: `--permission-mode` "dangerous" auto-approves all tools;
//	         `-p/--print [<PROMPT>]` non-interactive, accepts an inline prompt)
func HeadlessArgv(agentName, prompt string) ([]string, error) {
	name := Resolve(agentName)
	switch name {
	case "claude":
		return []string{"claude", "-p", "--dangerously-skip-permissions", prompt}, nil
	case "codex":
		return []string{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", prompt}, nil
	case "agy":
		return []string{"agy", "--dangerously-skip-permissions", "-p", prompt}, nil
	case "gemini":
		return []string{"gemini", "-y", prompt}, nil
	case "opencode":
		return []string{"opencode", "run", "--auto", prompt}, nil
	case "devin":
		return []string{"devin", "--permission-mode", "dangerous", "-p", prompt}, nil
	}
	return nil, fmt.Errorf("%s: %w", name, ErrHeadlessUnsupported)
}

// CommandString builds the shell command string prepared in a new Herdr
// pane for human review (legacy bin/hgf behavior; not auto-sent).
func CommandString(agentName, prompt string) string {
	name := Resolve(agentName)
	if name == "agy" {
		return "agy -i " + strconv.Quote(prompt)
	}
	return name + " " + strconv.Quote(prompt)
}

// Launcher runs the agent binary directly in the current terminal
// (inline fallback). Zero value is usable.
type Launcher struct {
	// LookPath resolves the agent binary; defaults to exec.LookPath.
	LookPath func(name string) (string, error)
	// Stdout/Stderr receive the agent's output; nil inherits os.Stdout/os.Stderr.
	Stdout io.Writer
	Stderr io.Writer
}

// New returns a Launcher with defaults.
func New() *Launcher { return &Launcher{} }

var _ ports.AgentLauncher = (*Launcher)(nil)

func (l *Launcher) lookPath() func(string) (string, error) {
	if l.LookPath != nil {
		return l.LookPath
	}
	return exec.LookPath
}

// Launch resolves the agent, verifies its binary is on PATH, and execs it.
// A missing binary yields *ports.BinaryNotFoundError; a failing agent
// propagates its exit status.
func (l *Launcher) Launch(ctx context.Context, agentName string, prompt string) error {
	name := Resolve(agentName)
	if _, err := l.lookPath()(name); err != nil {
		return &ports.BinaryNotFoundError{Binary: name}
	}
	cmd := exec.CommandContext(ctx, name, Argv(name, prompt)...)
	if l.Stdout != nil {
		cmd.Stdout = l.Stdout
	} else {
		cmd.Stdout = os.Stdout
	}
	if l.Stderr != nil {
		cmd.Stderr = l.Stderr
	} else {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("agent %s: %w", name, err)
	}
	return nil
}
