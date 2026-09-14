// Package projinit implements `lead enable`: project-oriented agent
// configuration (lead-flow skill) and an AGENTS.md managed block
// (issue #68, renamed from `lead install` to `lead init` in #79,
// separated from `lead setup` as `lead enable` in #169).
//
// Principles: apply by default after approval, idempotent re-runs,
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

	"github.com/kuwa72/lead-cli/internal/ports"
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
// (dispatch queue, review flow). Colors follow GitHub's conventional
// palette. `lead enable` reports each as `label/<name>: ok|missing`, and
// `lead enable` creates the missing ones (issues #172, #174).
var RequiredIssueLabels = []ports.LabelDefinition{
	{Name: "needs-review", Color: "FBCA04", Description: "Waiting for review"},
	{Name: "ready", Color: "0E8A16", Description: "Ready to be worked on"},
	{Name: "blocked", Color: "D93F0B", Description: "Blocked by another issue"},
}

// LabelClient lists and creates a repository's labels
// (satisfied by ports.GhClient).
type LabelClient interface {
	RepoLabels(ctx context.Context, repo string) ([]string, error)
	RepoCreateLabel(ctx context.Context, repo string, label ports.LabelDefinition) error
}

// ProtectionClient reads branch-protection state for the enable report
// (issue #197: dispatch refuses to start without it, so `lead enable`
// surfaces the gap with fix guidance). Satisfied by ports.GhClient;
// nil skips the report (offline / gh unavailable).
type ProtectionClient interface {
	RepoDefaultBranch(ctx context.Context, repo string) (string, error)
	BranchProtection(ctx context.Context, repo, branch string) (ports.BranchProtection, error)
	RepoAllowsAutoMerge(ctx context.Context, repo string) (bool, error)
}

var targets = []Target{
	{Name: "claude", Dir: claudeDir},
	{Name: "devin", Dir: devinDir},
}

// Options controls one install run.
type Options struct {
	// Root is the project root directory. Required.
	Root string
	// DryRun only previews what applying would change; nothing is
	// written locally or remotely.
	DryRun bool
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
	// Gh lists and creates repository labels for the required-labels check
	// (issues #172, #174). Nil skips the label handling
	// (offline / gh unavailable).
	Gh LabelClient
	// Protection reads branch-protection state for the enable report
	// (issue #197). Nil skips the protection handling.
	Protection ProtectionClient
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
	case opts.DryRun:
		return runPreview(opts.Root, opts, skill, agents)
	default:
		return runApply(opts.Root, opts, skill, agents)
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

func wantBlock(wantAgents []byte) string {
	return BlockStart + "\n" + string(wantAgents) + "\n" + BlockEnd + "\n"
}

// previewLines appends the skill and AGENTS.md status lines and reports
// whether applying would change anything.
func previewLines(rep *Report, root string, wantSkill []byte, block string) bool {
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
	return needChange
}

// runPreview reports what `lead enable` would change without touching
// anything locally or remotely (`--dry-run`).
func runPreview(root string, opts Options, wantSkill, wantAgents []byte) (Report, error) {
	var rep Report
	needChange := previewLines(&rep, root, wantSkill, wantBlock(wantAgents))
	labelLines, _ := labelReport(opts.Gh, opts.Repo)
	rep.Lines = append(rep.Lines, labelLines...)
	protLines, _ := protectionReport(opts.Protection, opts.Repo)
	rep.Lines = append(rep.Lines, protLines...)
	if !needChange {
		rep.Lines = append(rep.Lines, "lead-flow is already installed: no changes")
		return rep, nil
	}
	rep.Lines = append(rep.Lines, "dry run: no changes (run `lead enable` to apply)")
	return rep, nil
}

// runApply installs the skill files, the AGENTS.md block, and the missing
// required labels after approval (`lead enable`).
func runApply(root string, opts Options, wantSkill, wantAgents []byte) (Report, error) {
	var rep Report
	block := wantBlock(wantAgents)
	needChange := previewLines(&rep, root, wantSkill, block)

	if !opts.Yes {
		// The approval gates both local writes and remote label
		// creation: probe (read-only) whether labels are pending so an
		// installed tree with missing labels still asks first.
		if needChange || len(missingLabels(opts.Gh, opts.Repo)) > 0 {
			fmt.Fprint(os.Stderr, "Install lead-flow project configuration? [y/N] ")
			if !ask(opts.Stdin) {
				rep.Lines = append(rep.Lines, "aborted: no changes")
				return rep, nil
			}
		}
	}

	if needChange {
		for _, t := range targets {
			path := filepath.Join(root, t.Dir, "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return rep, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
			}
			if err := os.WriteFile(path, wantSkill, 0o644); err != nil {
				return rep, fmt.Errorf("write %s: %w", path, err)
			}
		}

		agentsPath := filepath.Join(root, agentsFile)
		curAgents, _ := os.ReadFile(agentsPath)
		next := UpsertBlock(normalizeLegacy(string(curAgents), block), block)
		if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
			return rep, err
		}
		if err := os.WriteFile(agentsPath, []byte(next), 0o644); err != nil {
			return rep, err
		}
	}

	// Remote labels last, after the approval above: create what is missing.
	labelLines, labelsCreated := ensureLabels(opts.Gh, opts.Repo)
	rep.Lines = append(rep.Lines, labelLines...)

	// Branch protection is report-only: creating protection rules needs
	// repository admin rights, so enable surfaces the gap with fix
	// guidance instead of mutating security settings (issue #197).
	protLines, _ := protectionReport(opts.Protection, opts.Repo)
	rep.Lines = append(rep.Lines, protLines...)

	rep.Changed = needChange || labelsCreated
	if !rep.Changed {
		rep.Lines = append(rep.Lines, "lead-flow is already installed: no changes")
		return rep, nil
	}
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

	// Protection never affects completeness: since issue #200 missing
	// protection only warns, so --check reports the gap with fix guidance
	// but still passes.
	protLines, _ := protectionReport(opts.Protection, opts.Repo)
	rep.Lines = append(rep.Lines, protLines...)

	if !rep.Complete {
		rep.Lines = append(rep.Lines, "Next: run `lead enable`")
	}
	return rep, nil
}

// labelReport checks RequiredIssueLabels against the repository's labels.
// ok=false means at least one required label is missing. A nil Gh, an
// empty repo (no remote), or a listing failure yields a skip warning with
// ok=true so offline / unauthenticated environments never block the local
// skill and AGENTS.md checks.
func labelReport(gh LabelClient, repo string) (lines []string, ok bool) {
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
		if present[want.Name] {
			lines = append(lines, "label/"+want.Name+": ok")
		} else {
			lines = append(lines, "label/"+want.Name+": missing")
			ok = false
		}
	}
	return lines, ok
}

// protectionReport checks the default-branch protection dispatch needs
// (issue #197: unattended agents refuse to start without it).
// ok=false means a required item is missing. A nil client, an empty repo
// (no remote), or an inspection failure yields a skip warning with ok=true
// so offline / unauthenticated environments never block the local checks.
func protectionReport(gh ProtectionClient, repo string) (lines []string, ok bool) {
	if gh == nil || repo == "" {
		return []string{"protection: skip (no repository remote; local checks only)"}, true
	}
	branch, err := gh.RepoDefaultBranch(context.Background(), repo)
	if err != nil {
		return []string{fmt.Sprintf("protection: skip (could not inspect: %v)", err)}, true
	}
	bp, err := gh.BranchProtection(context.Background(), repo, branch)
	if err != nil {
		return []string{fmt.Sprintf("protection: skip (could not inspect: %v)", err)}, true
	}
	auto, err := gh.RepoAllowsAutoMerge(context.Background(), repo)
	if err != nil {
		return []string{fmt.Sprintf("protection: skip (could not inspect: %v)", err)}, true
	}
	ok = true
	if bp.Protected {
		how := "protected"
		if bp.RequiresPR {
			how += ", PR required"
		}
		lines = append(lines, fmt.Sprintf("protection/branch-protection: ok (%s is %s)", branch, how))
	} else {
		lines = append(lines, fmt.Sprintf("protection/branch-protection: missing (%s is not protected; recommended for safe unattended dispatch)", branch))
		ok = false
	}
	if len(bp.RequiredChecks) > 0 {
		lines = append(lines, fmt.Sprintf("protection/required-checks: ok (%s requires: %s)", branch, strings.Join(bp.RequiredChecks, ", ")))
	} else {
		lines = append(lines, fmt.Sprintf("protection/required-checks: missing (%s has no required status checks; recommended for safe unattended dispatch)", branch))
		ok = false
	}
	if auto {
		lines = append(lines, "protection/auto-merge: ok (allow_auto_merge enabled)")
	} else {
		lines = append(lines, "protection/auto-merge: off (allow_auto_merge disabled; `lead finish` will pause for a manual --merge)")
	}
	if !ok {
		lines = append(lines, "Next: protect the default branch (Settings > Branches / Rules) so dispatch can start, then re-run `lead enable --check`")
	}
	return lines, ok
}

// missingLabels returns the required labels absent from the repository.
// It is a read-only probe for the write-path approval gate; nil means
// none missing — or that the check was skipped or failed (those cases
// surface as warnings in the report, never as pending work).
func missingLabels(gh LabelClient, repo string) []ports.LabelDefinition {
	if gh == nil || repo == "" {
		return nil
	}
	have, err := gh.RepoLabels(context.Background(), repo)
	if err != nil {
		return nil
	}
	present := make(map[string]bool, len(have))
	for _, l := range have {
		present[l] = true
	}
	var missing []ports.LabelDefinition
	for _, want := range RequiredIssueLabels {
		if !present[want.Name] {
			missing = append(missing, want)
		}
	}
	return missing
}

// ensureLabels creates the required labels missing from the repository and
// returns the final per-label status lines with created=true when at least
// one label was created. It is the write-path counterpart of labelReport:
// `created` replaces `missing`, while listing/creation failures degrade to
// skip warnings so remote trouble never fails the local install.
func ensureLabels(gh LabelClient, repo string) (lines []string, created bool) {
	if gh == nil || repo == "" {
		return []string{"labels: skip (no repository remote; local checks only)"}, false
	}
	have, err := gh.RepoLabels(context.Background(), repo)
	if err != nil {
		return []string{fmt.Sprintf("labels: skip (could not list labels: %v)", err)}, false
	}
	present := make(map[string]bool, len(have))
	for _, l := range have {
		present[l] = true
	}
	for _, want := range RequiredIssueLabels {
		if present[want.Name] {
			lines = append(lines, "label/"+want.Name+": ok")
			continue
		}
		if err := gh.RepoCreateLabel(context.Background(), repo, want); err != nil {
			lines = append(lines, fmt.Sprintf("label/%s: create failed: %v", want.Name, err))
			continue
		}
		lines = append(lines, "label/"+want.Name+": created")
		created = true
	}
	return lines, created
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
