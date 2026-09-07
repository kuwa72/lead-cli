// Package doctor implements `lead doctor`: environment diagnosis with
// required/optional separation (issue #44). See docs/rfc-26-distribution.md
// §6.6. Exit status reflects required items only; optional gaps warn.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/kuwa72/lead-cli/internal/adapters/agent"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/setup"
)

// Check is one diagnosis row.
type Check struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
}

// Mark renders the check status: ok, FAIL (required), or -- (optional gap).
func (c Check) Mark() string {
	if c.OK {
		return "ok"
	}
	if c.Required {
		return "FAIL"
	}
	return "--"
}

// Report is the full diagnosis.
type Report struct {
	Checks []Check `json:"checks"`
}

// OK reports whether all required checks pass.
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if c.Required && !c.OK {
			return false
		}
	}
	return true
}

// JSON renders the machine-readable report.
func (r Report) JSON() string {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(raw) + "\n"
}

// Deps injects the diagnosis environment.
type Deps struct {
	Gh       ports.GhClient
	LookPath func(string) (string, error)
	Getenv   func(string) string
	Home     string
	Shell    string // override; "" = detect from $SHELL
	Version  string
	Offline  bool
	// GenCompletion renders the expected completion script (for drift check).
	GenCompletion func(shell string) (string, error)
}

func (d Deps) lookPath() func(string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath
	}
	return exec.LookPath
}

func (d Deps) getenv(k string) string {
	if d.Getenv != nil {
		return d.Getenv(k)
	}
	return os.Getenv(k)
}

func (d Deps) home() string {
	if d.Home != "" {
		return d.Home
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// Run executes all checks.
func Run(d Deps) Report {
	var rep Report
	ctx := context.Background()

	rep.Checks = append(rep.Checks, Check{
		Name: "lead version", Required: true, OK: d.Version != "",
		Detail: fmt.Sprintf("lead version %s", d.Version),
	})

	if d.Gh == nil {
		rep.Checks = append(rep.Checks, Check{Name: "gh auth", Required: true, Detail: "gh client unavailable"})
	} else if err := d.Gh.AuthStatus(ctx); err != nil {
		rep.Checks = append(rep.Checks, Check{Name: "gh auth", Required: true,
			Detail: fmt.Sprintf("not authenticated (%v); run `gh auth login`", err)})
	} else {
		rep.Checks = append(rep.Checks, Check{Name: "gh auth", Required: true, OK: true, Detail: "authenticated"})
	}

	if d.Offline {
		rep.Checks = append(rep.Checks, Check{Name: "github api", Detail: "skipped (--offline)"})
	} else if d.Gh == nil {
		rep.Checks = append(rep.Checks, Check{Name: "github api", Detail: "gh client unavailable"})
	} else if login, err := d.Gh.ApiUser(ctx); err != nil {
		rep.Checks = append(rep.Checks, Check{Name: "github api", Detail: fmt.Sprintf("unreachable: %v", err)})
	} else {
		rep.Checks = append(rep.Checks, Check{Name: "github api", OK: true, Detail: fmt.Sprintf("reachable as %s", login)})
	}

	if _, err := d.lookPath()("herdr"); err != nil {
		rep.Checks = append(rep.Checks, Check{Name: "herdr", Detail: "absent: inline fallback (single terminal)"})
	} else if d.getenv("HERDR_ENV") == "1" {
		rep.Checks = append(rep.Checks, Check{Name: "herdr", OK: true, Detail: "multipane available (HERDR_ENV=1)"})
	} else {
		rep.Checks = append(rep.Checks, Check{Name: "herdr", OK: true, Detail: "installed but HERDR_ENV unset: inline fallback"})
	}

	var found []string
	for _, name := range agent.KnownAgents {
		if _, err := d.lookPath()(name); err == nil {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		rep.Checks = append(rep.Checks, Check{Name: "agents", Detail: "none of agy/claude/codex/devin/opencode/gemini on PATH"})
	} else {
		rep.Checks = append(rep.Checks, Check{Name: "agents", OK: true, Detail: fmt.Sprintf("found: %s", strings.Join(found, ", "))})
	}

	rep.Checks = append(rep.Checks, completionCheck(d))
	rep.Checks = append(rep.Checks, keybindingCheck(d))
	return rep
}

func shellOf(d Deps) (setup.Shell, error) {
	return setup.DetectShell(d.Shell)
}

func completionCheck(d Deps) Check {
	sh, err := shellOf(d)
	if err != nil {
		return Check{Name: "completion", Detail: fmt.Sprintf("shell undetected: %v", err)}
	}
	paths := setup.PathsFor(sh, d.home())
	got, err := os.ReadFile(paths.CompletionFile)
	if err != nil {
		return Check{Name: "completion", Detail: fmt.Sprintf("missing (%s); run `lead setup --write`", paths.CompletionFile)}
	}
	if d.GenCompletion != nil {
		if want, genErr := d.GenCompletion(string(sh)); genErr == nil && string(got) != want {
			return Check{Name: "completion", Detail: fmt.Sprintf("differs (%s); run `lead setup --write`", paths.CompletionFile)}
		}
	}
	return Check{Name: "completion", OK: true, Detail: fmt.Sprintf("placed (%s)", paths.CompletionFile)}
}

func keybindingCheck(d Deps) Check {
	sh, err := shellOf(d)
	if err != nil {
		return Check{Name: "keybinding", Detail: "shell undetected"}
	}
	paths := setup.PathsFor(sh, d.home())
	b, err := os.ReadFile(paths.RCFile)
	if err != nil || !setup.HasBlock(string(b)) {
		return Check{Name: "keybinding", Detail: "no lead block (opt-in via `lead setup --write`)"}
	}
	if strings.Contains(string(b), "C-g") || strings.Contains(string(b), "^G") || strings.Contains(string(b), "\\cg") {
		return Check{Name: "keybinding", OK: true, Detail: "Ctrl-G enabled"}
	}
	return Check{Name: "keybinding", Detail: "block present, keybinding off"}
}
