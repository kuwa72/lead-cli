// Package projinit implements `lead init`: project-oriented agent
// configuration (lead-flow skill) and an AGENTS.md managed block
// (issue #68, renamed from `lead install` in #79).
//
// Principles: dry-run by default, approval before writes, idempotent re-runs,
// safe no-ops on non-interactive stdin, and conservative uninstall that
// preserves foreign files.
package projinit

import (
	"bufio"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed skill.md
var defaultSkill []byte

//go:embed agents_snippet.md
var defaultAgentsBlock []byte

const (
	skillName  = "lead-flow"
	claudeDir  = ".claude/skills/" + skillName
	devinDir   = ".devin/skills/" + skillName
	agentsFile = "AGENTS.md"
)

const (
	// BlockStart/BlockEnd fence the managed AGENTS.md section (idempotency markers).
	BlockStart = "<!-- lead-flow begin (managed by `lead init`; do not edit) -->"
	BlockEnd   = "<!-- lead-flow end -->"
)

// Target is one skill installation location.
type Target struct {
	Name string
	Dir  string
}

var targets = []Target{
	{Name: "claude", Dir: claudeDir},
	{Name: "devin", Dir: devinDir},
}

// Options controls one install run.
type Options struct {
	// Root is the project root directory. Required.
	Root string
	// Write applies changes. Without it, Run only previews.
	Write bool
	// Check reports whether the project is configured; no changes.
	Check bool
	// Uninstall removes managed files and the AGENTS.md block.
	Uninstall bool
	// Yes skips the approval prompt.
	Yes bool
	// Stdin is used for the approval prompt. Non-interactive (nil/EOF) is a no.
	Stdin io.Reader
	// SkillContent overrides the embedded skill markdown. Tests use it.
	SkillContent []byte
	// AgentsBlock overrides the embedded AGENTS.md snippet. Tests use it.
	AgentsBlock []byte
}

// Report describes what happened (Lines are user-facing).
type Report struct {
	Lines    []string
	Changed  bool
	Complete bool // for --check: everything is in place
}

// Run executes install/uninstall/check per Options.
func Run(opts Options) (Report, error) {
	if opts.Root == "" {
		return Report{}, fmt.Errorf("init: project root is required")
	}
	fi, err := os.Stat(opts.Root)
	if err != nil {
		return Report{}, fmt.Errorf("init: %w", err)
	}
	if !fi.IsDir() {
		return Report{}, fmt.Errorf("init: %s is not a directory", opts.Root)
	}

	skill := opts.SkillContent
	if skill == nil {
		skill = defaultSkill
	}
	agents := opts.AgentsBlock
	if agents == nil {
		agents = defaultAgentsBlock
	}

	switch {
	case opts.Uninstall:
		return runUninstall(opts.Root)
	case opts.Check:
		return runCheck(opts.Root, skill, agents)
	default:
		return runInstall(opts.Root, opts, skill, agents)
	}
}

// HasBlock reports whether content carries the managed block.
func HasBlock(content string) bool {
	return strings.Contains(content, BlockStart) && strings.Contains(content, BlockEnd)
}

// UpsertBlock replaces the managed block or appends it (newline-terminated).
func UpsertBlock(content, block string) string {
	start := strings.Index(content, BlockStart)
	end := strings.Index(content, BlockEnd)
	if start >= 0 && end > start {
		end += len(BlockEnd)
		rest := content[end:]
		rest = strings.TrimLeft(rest, "\n")
		return content[:start] + block + rest
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + block
}

// RemoveBlock drops the managed block, preserving user content.
// ok=false means no block was present.
func RemoveBlock(content string) (string, bool) {
	start := strings.Index(content, BlockStart)
	end := strings.Index(content, BlockEnd)
	if start < 0 || end <= start {
		return content, false
	}
	end += len(BlockEnd)
	out := content[:start] + strings.TrimLeft(content[end:], "\n")
	return out, true
}

func runInstall(root string, opts Options, wantSkill, wantAgents []byte) (Report, error) {
	var rep Report
	block := BlockStart + "\n" + string(wantAgents) + "\n" + BlockEnd + "\n"
	needChange := false

	for _, t := range targets {
		path := filepath.Join(root, t.Dir, "SKILL.md")
		cur, _ := os.ReadFile(path)
		action := fileAction(cur, wantSkill)
		rep.Lines = append(rep.Lines, fmt.Sprintf("skill/%s: %s %s", t.Name, action, path))
		if action != "keep" {
			needChange = true
		}
	}

	agentsPath := filepath.Join(root, agentsFile)
	curAgents, _ := os.ReadFile(agentsPath)
	action := agentsAction(string(curAgents), block)
	rep.Lines = append(rep.Lines, fmt.Sprintf("AGENTS.md: %s %s", action, agentsPath))
	if action != "keep" {
		needChange = true
	}

	if !needChange {
		rep.Lines = []string{"lead-flow is already installed: no changes"}
		return rep, nil
	}

	if !opts.Write {
		rep.Lines = append(rep.Lines, "dry run: no changes (pass --write to apply)")
		return rep, nil
	}

	if !opts.Yes {
		fmt.Fprint(os.Stderr, "Install lead-flow project configuration? [y/N] ")
		if !ask(opts.Stdin) {
			rep.Lines = append(rep.Lines, "aborted: no changes")
			return rep, nil
		}
	}

	for _, t := range targets {
		path := filepath.Join(root, t.Dir, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return rep, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, wantSkill, 0o644); err != nil {
			return rep, fmt.Errorf("write %s: %w", path, err)
		}
	}

	next := UpsertBlock(string(curAgents), block)
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		return rep, err
	}
	if err := os.WriteFile(agentsPath, []byte(next), 0o644); err != nil {
		return rep, err
	}

	rep.Changed = true
	rep.Lines = append(rep.Lines, "done: lead-flow is installed")
	return rep, nil
}

func runCheck(root string, wantSkill, wantAgents []byte) (Report, error) {
	var rep Report
	rep.Complete = true
	block := BlockStart + "\n" + string(wantAgents) + "\n" + BlockEnd + "\n"

	for _, t := range targets {
		path := filepath.Join(root, t.Dir, "SKILL.md")
		cur, _ := os.ReadFile(path)
		if string(cur) != string(wantSkill) {
			rep.Complete = false
			if len(cur) == 0 {
				rep.Lines = append(rep.Lines, fmt.Sprintf("skill/%s: missing (%s)", t.Name, path))
			} else {
				rep.Lines = append(rep.Lines, fmt.Sprintf("skill/%s: differs (%s)", t.Name, path))
			}
		} else {
			rep.Lines = append(rep.Lines, fmt.Sprintf("skill/%s: ok (%s)", t.Name, path))
		}
	}

	agentsPath := filepath.Join(root, agentsFile)
	curAgents, _ := os.ReadFile(agentsPath)
	cur := string(curAgents)
	if !HasBlock(cur) || UpsertBlock(cur, block) != cur {
		rep.Complete = false
		if len(curAgents) == 0 {
			rep.Lines = append(rep.Lines, fmt.Sprintf("AGENTS.md: missing (%s)", agentsPath))
		} else if !HasBlock(cur) {
			rep.Lines = append(rep.Lines, fmt.Sprintf("AGENTS.md: block missing (%s)", agentsPath))
		} else {
			rep.Lines = append(rep.Lines, fmt.Sprintf("AGENTS.md: differs (%s)", agentsPath))
		}
	} else {
		rep.Lines = append(rep.Lines, fmt.Sprintf("AGENTS.md: ok (%s)", agentsPath))
	}

	if !rep.Complete {
		rep.Lines = append(rep.Lines, "Next: run `lead init --write`")
	}
	return rep, nil
}

func runUninstall(root string) (Report, error) {
	var rep Report

	for _, t := range targets {
		path := filepath.Join(root, t.Dir, "SKILL.md")
		cur, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if !isManagedSkill(cur) {
			rep.Lines = append(rep.Lines, fmt.Sprintf("skill/%s: foreign file kept (%s)", t.Name, path))
			continue
		}
		if err := os.Remove(path); err != nil {
			return rep, fmt.Errorf("remove %s: %w", path, err)
		}
		removeEmptyParents(filepath.Dir(path), root)
		rep.Changed = true
		rep.Lines = append(rep.Lines, fmt.Sprintf("skill/%s: removed (%s)", t.Name, path))
	}

	agentsPath := filepath.Join(root, agentsFile)
	cur, _ := os.ReadFile(agentsPath)
	next, ok := RemoveBlock(string(cur))
	if !ok {
		rep.Lines = append(rep.Lines, "AGENTS.md: no managed block (nothing to do)")
		return rep, nil
	}
	if err := os.WriteFile(agentsPath, []byte(next), 0o644); err != nil {
		return rep, fmt.Errorf("write %s: %w", agentsPath, err)
	}
	rep.Changed = true
	rep.Lines = append(rep.Lines, fmt.Sprintf("AGENTS.md: removed managed block (%s)", agentsPath))
	return rep, nil
}

func fileAction(cur, want []byte) string {
	if len(cur) == 0 {
		return "new"
	}
	if string(cur) == string(want) {
		return "keep"
	}
	return "replace"
}

func agentsAction(cur, block string) string {
	if cur == "" {
		return "new"
	}
	if HasBlock(cur) {
		next := UpsertBlock(cur, block)
		if next == cur {
			return "keep"
		}
		return "update"
	}
	return "append"
}

func isManagedSkill(content []byte) bool {
	return strings.Contains(string(content), "name: "+skillName)
}

func removeEmptyParents(dir, stop string) {
	for {
		if dir == stop || dir == "" || dir == "." || dir == "/" {
			return
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

func ask(stdin io.Reader) bool {
	if stdin == nil {
		return false
	}
	sc := bufio.NewScanner(stdin)
	if !sc.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(sc.Text()))
	return ans == "y" || ans == "yes"
}
