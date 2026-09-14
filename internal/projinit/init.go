// Package projinit implements `lead enable`: project-oriented agent
// configuration (lead-flow skill) and an AGENTS.md managed block
// (issue #68, renamed from `lead install` to `lead init` in #79,
// separated from `lead setup` as `lead enable` in #169).
//
// Principles: dry-run by default, approval before writes, idempotent re-runs,
// safe no-ops on non-interactive stdin, and conservative uninstall that
// preserves foreign files.
package projinit

import (
	"bufio"
	"context"
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
	BlockStart = "<!-- lead-flow begin (managed by `lead enable`; do not edit) -->"
	BlockEnd   = "<!-- lead-flow end -->"
	// LegacyBlockStart fences blocks written by `lead init` before #169.
	// HasBlock/RemoveBlock recognize it; --write migrates it to BlockStart.
	LegacyBlockStart = "<!-- lead-flow begin (managed by `lead init`; do not edit) -->"
	LegacyBlockEnd   = "<!-- lead-flow end -->"
)

// Target is one skill installation location.
type Target struct {
	Name string
	Dir  string
}

// RequiredIssueLabels are the GitHub issue labels the lead workflow needs
// (dispatch queue, review flow). `lead enable` reports each as
// `label/<name>: ok` or `label/<name>: missing` (issue #172).
var RequiredIssueLabels = []string{"needs-review", "ready", "blocked"}

// LabelLister lists a repository's label names (satisfied by ports.GhClient).
type LabelLister interface {
	RepoLabels(ctx context.Context, repo string) ([]string, error)
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
	// Gh lists repository labels for the required-labels check (issue #172).
	// Nil skips the label check (offline / gh unavailable).
	Gh LabelLister
	// Repo is "owner/repo" for the label check. Empty skips it
	// (no remote: local checks only).
	Repo string
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
		return Report{}, fmt.Errorf("enable: project root is required")
	}
	fi, err := os.Stat(opts.Root)
	if err != nil {
		return Report{}, fmt.Errorf("enable: %w", err)
	}
	if !fi.IsDir() {
		return Report{}, fmt.Errorf("enable: %s is not a directory", opts.Root)
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
		return runCheck(opts.Root, opts, skill, agents)
	default:
		return runInstall(opts.Root, opts, skill, agents)
	}
}

// HasBlock reports whether content carries the managed block
// (current or legacy `lead init` markers).
func HasBlock(content string) bool {
	return hasMarker(content, BlockStart, BlockEnd) ||
		hasMarker(content, LegacyBlockStart, LegacyBlockEnd)
}

func hasMarker(content, start, end string) bool {
	return strings.Contains(content, start) && strings.Contains(content, end)
}

// normalizeLegacy replaces a legacy `lead init` block with the want block.
// A file carrying only legacy markers is functionally installed, so --check
// treats it as complete while --write normalizes the markers.
func normalizeLegacy(content, block string) string {
	if hasMarker(content, BlockStart, BlockEnd) {
		if next, ok := removeMarker(content, LegacyBlockStart, LegacyBlockEnd); ok {
			return next
		}
		return content
	}
	start := strings.Index(content, LegacyBlockStart)
	end := strings.Index(content, LegacyBlockEnd)
	if start < 0 || end <= start {
		return content
	}
	end += len(LegacyBlockEnd)
	rest := strings.TrimLeft(content[end:], "\n")
	return content[:start] + block + rest
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
// ok=false means no block was present. Legacy `lead init` blocks count
// as managed and are removed the same way.
func RemoveBlock(content string) (string, bool) {
	if next, ok := removeMarker(content, BlockStart, BlockEnd); ok {
		return next, true
	}
	return removeMarker(content, LegacyBlockStart, LegacyBlockEnd)
}

func removeMarker(content, startMarker, endMarker string) (string, bool) {
	start := strings.Index(content, startMarker)
	end := strings.Index(content, endMarker)
	if start < 0 || end <= start {
		return content, false
	}
	end += len(endMarker)
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

	// Required-label status is informational only: missing labels never
	// force a rewrite (auto-creation is out of scope for #172).
	labelLines, _ := labelReport(opts.Gh, opts.Repo)
	rep.Lines = append(rep.Lines, labelLines...)

	if !needChange {
		rep.Lines = append(rep.Lines, "lead-flow is already installed: no changes")
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

	next := UpsertBlock(normalizeLegacy(string(curAgents), block), block)
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

func runCheck(root string, opts Options, wantSkill, wantAgents []byte) (Report, error) {
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
	cur := normalizeLegacy(string(curAgents), block)
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

	labelLines, labelsOK := labelReport(opts.Gh, opts.Repo)
	rep.Lines = append(rep.Lines, labelLines...)
	if !labelsOK {
		rep.Complete = false
	}

	if !rep.Complete {
		rep.Lines = append(rep.Lines, "Next: run `lead enable --write`")
	}
	return rep, nil
}

// labelReport checks RequiredIssueLabels against the repository's labels.
// ok=false means at least one required label is missing. A nil Gh, an
// empty repo (no remote), or a listing failure yields a skip warning with
// ok=true so offline / unauthenticated environments never block the local
// skill and AGENTS.md checks.
func labelReport(gh LabelLister, repo string) (lines []string, ok bool) {
	if gh == nil || repo == "" {
		return []string{"labels: skip (no repository remote; local checks only)"}, true
	}
	have, err := gh.RepoLabels(context.Background(), repo)
	if err != nil {
		return []string{fmt.Sprintf("labels: skip (could not list labels: %v)", err)}, true
	}
	present := make(map[string]bool, len(have))
	for _, l := range have {
		present[l] = true
	}
	ok = true
	for _, want := range RequiredIssueLabels {
		if present[want] {
			lines = append(lines, "label/"+want+": ok")
		} else {
			lines = append(lines, "label/"+want+": missing")
			ok = false
		}
	}
	return lines, ok
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
		next := UpsertBlock(normalizeLegacy(cur, block), block)
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
