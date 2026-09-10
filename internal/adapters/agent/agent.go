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
	"strings"

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

// ArgvForMode returns the argv (without the binary name) for launching the agent
// in the requested mode (interactive vs batch/dangerous).
func ArgvForMode(agentName, prompt, mode string) ([]string, error) {
	name := Resolve(agentName)
	switch mode {
	case "", "interactive", "safe":
		if !IsKnown(name) {
			return nil, fmt.Errorf("%s: unknown agent", name)
		}
		return Argv(name, prompt), nil
	case "batch", "dangerous", "auto":
		argv, err := HeadlessArgv(name, prompt)
		if err != nil {
			return nil, err
		}
		if len(argv) <= 1 {
			return nil, fmt.Errorf("%s: headless argv too short", name)
		}
		return argv[1:], nil
	default:
		return nil, fmt.Errorf("unknown agent mode %q (supported: interactive, batch, dangerous)", mode)
	}
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

// CommandStringForMode builds the shell command string for launching the agent
// in the requested mode (interactive vs batch/dangerous).
func CommandStringForMode(agentName, prompt, mode string) (string, error) {
	name := Resolve(agentName)
	switch mode {
	case "", "interactive", "safe":
		if !IsKnown(name) {
			return "", fmt.Errorf("%s: unknown agent", name)
		}
		return CommandString(name, prompt), nil
	case "batch", "dangerous", "auto":
		argv, err := HeadlessArgv(name, prompt)
		if err != nil {
			return "", err
		}
		var parts []string
		for i, arg := range argv {
			if i == len(argv)-1 {
				parts = append(parts, strconv.Quote(arg))
			} else {
				parts = append(parts, arg)
			}
		}
		return strings.Join(parts, " "), nil
	default:
		return "", fmt.Errorf("unknown agent mode %q (supported: interactive, batch, dangerous)", mode)
	}
}

// Launcher runs the agent binary directly in the current terminal
// (inline fallback). Zero value is usable.
type Launcher struct {
	// LookPath resolves the agent binary; defaults to exec.LookPath.
	LookPath func(name string) (string, error)
	// Stdout/Stderr receive the agent's output; nil inherits os.Stdout/os.Stderr.
	Stdout io.Writer
	Stderr io.Writer
	// Dir sets the working directory of the agent command.
	Dir string
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
	return l.LaunchForMode(ctx, agentName, prompt, "interactive")
}

// LaunchInDir resolves the agent and execs it in dir with mode-specific flags.
func (l *Launcher) LaunchInDir(ctx context.Context, dir, agentName, prompt, mode string) error {
	cp := *l
	if dir != "" {
		cp.Dir = dir
	}
	return cp.LaunchForMode(ctx, agentName, prompt, mode)
}

// LaunchForMode resolves the agent and execs it with mode-specific flags.
func (l *Launcher) LaunchForMode(ctx context.Context, agentName, prompt, mode string) error {
	name := Resolve(agentName)
	if _, err := l.lookPath()(name); err != nil {
		return &ports.BinaryNotFoundError{Binary: name}
	}
	argv, err := ArgvForMode(name, prompt, mode)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, name, argv...)
	if l.Dir != "" {
		cmd.Dir = l.Dir
	}
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
