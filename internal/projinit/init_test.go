package projinit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
)

var (
	testSkill  = []byte("name: " + skillName + "\ntest skill content\n")
	testAgents = []byte("test agents block\n")
)

// fakeProtection is a narrow ProtectionClient for report tests.
type fakeProtection struct {
	branch    string
	branchErr error
	prot      ports.BranchProtection
	protErr   error
	auto      bool
	autoErr   error
}

func (f fakeProtection) RepoDefaultBranch(_ context.Context, _ string) (string, error) {
	return f.branch, f.branchErr
}

func (f fakeProtection) BranchProtection(_ context.Context, _, _ string) (ports.BranchProtection, error) {
	return f.prot, f.protErr
}

func (f fakeProtection) RepoAllowsAutoMerge(_ context.Context, _ string) (bool, error) {
	return f.auto, f.autoErr
}

func TestProtectionReport_OK(t *testing.T) {
	gh := fakeProtection{branch: "main",
		prot: ports.BranchProtection{Protected: true, RequiresPR: true, RequiredChecks: []string{"test"}},
		auto: true}
	lines, ok := protectionReport(gh, "o/r")
	if !ok {
		t.Fatalf("protected repo ok = false: %v", lines)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"protection/branch-protection: ok",
		"protection/required-checks: ok",
		"protection/auto-merge: ok",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("report missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Next:") {
		t.Errorf("ok report must not carry fix guidance:\n%s", joined)
	}
}

func TestProtectionReport_Missing(t *testing.T) {
	lines, ok := protectionReport(fakeProtection{branch: "main"}, "o/r")
	if ok {
		t.Fatalf("unprotected repo ok = true: %v", lines)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"protection/branch-protection: missing",
		"protection/required-checks: missing",
		"recommended for safe unattended dispatch",
		"Next:",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("report missing %q:\n%s", want, joined)
		}
	}
}

func TestProtectionReport_Skip(t *testing.T) {
	for _, tc := range []struct {
		name string
		gh   ProtectionClient
		repo string
	}{
		{"nil client", nil, "o/r"},
		{"empty repo", fakeProtection{branch: "main"}, ""},
		{"api error", fakeProtection{branchErr: errors.New("HTTP 403")}, "o/r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines, ok := protectionReport(tc.gh, tc.repo)
			if !ok {
				t.Errorf("skip case ok = false: %v", lines)
			}
			if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "protection: skip") {
				t.Errorf("skip case missing skip line: %v", lines)
			}
		})
	}
}

func TestRunRequiresRoot(t *testing.T) {
	if _, err := Run(Options{}); err == nil {
		t.Fatal("Run with empty Root = nil, want error")
	}
	if _, err := Run(Options{Root: t.TempDir() + "/missing"}); err == nil {
		t.Fatal("Run with missing Root = nil, want error")
	}
}

func TestRunDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	rep, err := Run(Options{
		Root:         root,
		DryRun:       true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	})
	if err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}
	if rep.Changed {
		t.Error("dry-run Changed = true, want no writes")
	}
	if len(rep.Lines) == 0 {
		t.Error("dry-run produced no preview lines")
	}
	if !strings.Contains(strings.Join(rep.Lines, "\n"), "dry run") {
		t.Errorf("dry-run missing dry-run notice: %v", rep.Lines)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Errorf("dry-run created files: %v", entries)
	}
}

func TestRunApplyCreatesManagedFiles(t *testing.T) {
	root := t.TempDir()
	rep, err := Run(Options{
		Root:         root,
		Yes:          true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	})
	if err != nil {
		t.Fatalf("Run --write: %v", err)
	}
	if !rep.Changed {
		t.Error("Changed = false, want true after writes")
	}

	for _, d := range []string{claudeDir, devinDir} {
		path := filepath.Join(root, d, "SKILL.md")
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("skill file missing: %s", path)
		}
		if string(got) != string(testSkill) {
			t.Errorf("%s = %q, want %q", path, got, testSkill)
		}
	}

	agentsPath := filepath.Join(root, agentsFile)
	got, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("AGENTS.md missing: %v", err)
	}
	if !HasBlock(string(got)) {
		t.Errorf("AGENTS.md missing managed block:\n%s", got)
	}
	if !strings.Contains(string(got), string(testAgents)) {
		t.Errorf("AGENTS.md missing block content: %s", got)
	}
}

func TestRunApplyNeedsApproval(t *testing.T) {
	root := t.TempDir()
	// "n" declines: nothing written.
	if _, err := Run(Options{
		Root:         root,
		Stdin:        strings.NewReader("n\n"),
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	}); err != nil {
		t.Fatalf("declined Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, agentsFile)); !os.IsNotExist(err) {
		t.Error("declined install wrote AGENTS.md")
	}

	// EOF (non-interactive) defaults to no.
	if _, err := Run(Options{
		Root:         root,
		Stdin:        strings.NewReader(""),
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	}); err != nil {
		t.Fatalf("EOF Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, agentsFile)); !os.IsNotExist(err) {
		t.Error("non-interactive install wrote AGENTS.md")
	}

	// "y" approves.
	if _, err := Run(Options{
		Root:         root,
		Stdin:        strings.NewReader("y\n"),
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	}); err != nil {
		t.Fatalf("approved Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, agentsFile)); err != nil {
		t.Errorf("approved install did not write AGENTS.md: %v", err)
	}
}

func TestRunIsIdempotent(t *testing.T) {
	root := t.TempDir()
	opts := Options{
		Root:         root,
		Yes:          true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	}
	if _, err := Run(opts); err != nil {
		t.Fatalf("first install: %v", err)
	}
	rep, err := Run(opts)
	if err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if rep.Changed {
		t.Errorf("reinstall Changed = true, want idempotent (lines: %v)", rep.Lines)
	}
}

func TestRunCheckReportsCompleteness(t *testing.T) {
	root := t.TempDir()

	rep, err := Run(Options{
		Root:         root,
		Check:        true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	})
	if err != nil {
		t.Fatalf("check on empty: %v", err)
	}
	if rep.Complete {
		t.Error("check on empty = complete, want incomplete")
	}

	if _, err := Run(Options{
		Root:         root,
		Yes:          true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	rep, err = Run(Options{
		Root:         root,
		Check:        true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	})
	if err != nil {
		t.Fatalf("check after install: %v", err)
	}
	if !rep.Complete {
		t.Errorf("check after install = incomplete (lines: %v)", rep.Lines)
	}
}

func TestRunUninstallRemovesManagedFiles(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(Options{
		Root:         root,
		Yes:          true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	rep, err := Run(Options{
		Root:         root,
		Uninstall:    true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	})
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !rep.Changed {
		t.Error("uninstall Changed = false, want removals reported")
	}

	for _, d := range []string{claudeDir, devinDir} {
		path := filepath.Join(root, d, "SKILL.md")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("skill file remains: %s", path)
		}
	}

	agents, _ := os.ReadFile(filepath.Join(root, agentsFile))
	if HasBlock(string(agents)) {
		t.Errorf("AGENTS.md block remains:\n%s", agents)
	}

	// Idempotent: second uninstall is a no-op.
	rep2, err := Run(Options{
		Root:         root,
		Uninstall:    true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	})
	if err != nil {
		t.Fatalf("re-uninstall: %v", err)
	}
	if rep2.Changed {
		t.Error("re-uninstall Changed = true, want no-op")
	}
}

func TestRunUninstallKeepsForeignSkill(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, claudeDir, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("name: other-skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(Options{
		Root:         root,
		Uninstall:    true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	})
	if err != nil {
		t.Fatalf("uninstall with foreign skill: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("foreign skill file was removed")
	}
	if !strings.Contains(strings.Join(rep.Lines, "\n"), "foreign") {
		t.Errorf("uninstall did not report foreign skill kept: %v", rep.Lines)
	}
}

func TestRunUninstallKeepsForeignAgents(t *testing.T) {
	root := t.TempDir()
	agentsPath := filepath.Join(root, agentsFile)
	if err := os.WriteFile(agentsPath, []byte("# foreign AGENTS.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(Options{
		Root:         root,
		Uninstall:    true,
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	})
	if err != nil {
		t.Fatalf("uninstall with foreign AGENTS: %v", err)
	}
	got, _ := os.ReadFile(agentsPath)
	if !strings.Contains(string(got), "foreign") {
		t.Error("foreign AGENTS.md content was removed")
	}
	if rep.Changed {
		t.Error("uninstall Changed = true, want no-op on foreign AGENTS.md")
	}
}

func TestBlockUpsertRemoveRoundTrip(t *testing.T) {
	plain := "# rules\n"
	block := BlockStart + "\ncontent\n" + BlockEnd + "\n"

	with := UpsertBlock(plain, block)
	if !HasBlock(with) {
		t.Fatalf("UpsertBlock missing markers:\n%s", with)
	}
	if got := strings.Count(with, BlockStart); got != 1 {
		t.Errorf("block markers = %d, want exactly 1", got)
	}
	// Idempotent: second upsert replaces instead of duplicating.
	block2 := BlockStart + "\ncontent2\n" + BlockEnd + "\n"
	again := UpsertBlock(with, block2)
	if got := strings.Count(again, BlockStart); got != 1 {
		t.Errorf("re-upsert markers = %d, want 1", got)
	}
	if strings.Contains(again, "content\n") && !strings.Contains(again, "content2\n") {
		t.Errorf("re-upsert kept old snippet:\n%s", again)
	}
	removed, ok := RemoveBlock(again)
	if !ok || HasBlock(removed) {
		t.Errorf("RemoveBlock = %q, %v; want block gone", removed, ok)
	}
	if !strings.Contains(removed, "# rules") {
		t.Errorf("RemoveBlock dropped user content: %q", removed)
	}
}

func TestLegacyInitBlockMigratesToEnable(t *testing.T) {
	root := t.TempDir()
	legacy := "<!-- lead-flow begin (managed by `lead init`; do not edit) -->\nold\n<!-- lead-flow end -->\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# rules\n"+legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(Options{Root: root, Yes: true, SkillContent: testSkill, AgentsBlock: testAgents})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Changed {
		t.Error("Changed = false migrating a legacy block, want true")
	}
	cur, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if strings.Contains(string(cur), "managed by `lead init`") {
		t.Errorf("legacy block marker remains:\n%s", cur)
	}
	if got := strings.Count(string(cur), "lead-flow begin"); got != 1 {
		t.Errorf("lead-flow blocks = %d, want exactly 1:\n%s", got, cur)
	}
	if !HasBlock(string(cur)) {
		t.Errorf("migrated file missing the managed block:\n%s", cur)
	}
	// Legacy content alone also counts as installed for --check (no forced rewrite).
	root2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(root2, "AGENTS.md"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tgt := range []string{".claude/skills/lead-flow/SKILL.md", ".devin/skills/lead-flow/SKILL.md"} {
		p := filepath.Join(root2, tgt)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, testSkill, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rep2, err := Run(Options{Root: root2, Check: true, SkillContent: testSkill, AgentsBlock: testAgents})
	if err != nil {
		t.Fatal(err)
	}
	if !rep2.Complete {
		t.Errorf("--check on legacy block = incomplete, want complete (lines: %v)", rep2.Lines)
	}
}

// stubLabelClient is a minimal projinit.LabelClient for label-check tests:
// it records the requested repo and returns canned labels or an error,
// and records created labels (or fails creation).
type stubLabelClient struct {
	labels    []string
	err       error
	repos     []string
	created   []string
	createErr error
}

func (s *stubLabelClient) RepoLabels(_ context.Context, repo string) ([]string, error) {
	s.repos = append(s.repos, repo)
	return s.labels, s.err
}

func (s *stubLabelClient) RepoCreateLabel(_ context.Context, repo string, label ports.LabelDefinition) error {
	s.repos = append(s.repos, repo)
	if s.createErr != nil {
		return s.createErr
	}
	s.created = append(s.created, label.Name)
	return nil
}

// installLocal writes a fully-configured local tree so that only the
// label check can make --check incomplete.
func installLocal(t *testing.T, root string) {
	t.Helper()
	if _, err := Run(Options{Root: root, Yes: true, SkillContent: testSkill, AgentsBlock: testAgents}); err != nil {
		t.Fatalf("install local: %v", err)
	}
}

func joinLines(rep Report) string { return strings.Join(rep.Lines, "\n") }

func TestRunCheckFailsWhenLabelsMissing(t *testing.T) {
	root := t.TempDir()
	installLocal(t, root)
	gh := &stubLabelClient{labels: []string{"needs-review", "ready"}}
	rep, err := Run(Options{Root: root, Check: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: "o/r"})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if rep.Complete {
		t.Errorf("check with a missing label = complete, want incomplete (lines: %v)", rep.Lines)
	}
	if got := joinLines(rep); !strings.Contains(got, "label/blocked: missing") {
		t.Errorf("check missing label/blocked report line:\n%s", got)
	}
	if got := joinLines(rep); !strings.Contains(got, "label/needs-review: ok") {
		t.Errorf("check missing label/needs-review ok line:\n%s", got)
	}
}

func TestRunCheckPassesWhenLabelsPresent(t *testing.T) {
	root := t.TempDir()
	installLocal(t, root)
	gh := &stubLabelClient{labels: []string{"needs-review", "ready", "blocked", "extra"}}
	rep, err := Run(Options{Root: root, Check: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: "o/r"})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !rep.Complete {
		t.Errorf("check with all labels = incomplete, want complete (lines: %v)", rep.Lines)
	}
	for _, want := range RequiredIssueLabels {
		if got := joinLines(rep); !strings.Contains(got, "label/"+want.Name+": ok") {
			t.Errorf("check missing label/%s ok line:\n%s", want.Name, got)
		}
	}
	if len(gh.repos) != 1 || gh.repos[0] != "o/r" {
		t.Errorf("RepoLabels repos = %v, want [o/r]", gh.repos)
	}
}

func TestRunCheckSkipsLabelsWithoutRepo(t *testing.T) {
	root := t.TempDir()
	installLocal(t, root)
	gh := &stubLabelClient{labels: []string{}}
	rep, err := Run(Options{Root: root, Check: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: ""})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !rep.Complete {
		t.Errorf("check without repo = incomplete, want complete (local checks must not be blocked): %v", rep.Lines)
	}
	if got := joinLines(rep); !strings.Contains(got, "skip") {
		t.Errorf("check without repo missing skip notice:\n%s", got)
	}
	if len(gh.repos) != 0 {
		t.Errorf("RepoLabels called without repo: %v", gh.repos)
	}
}

func TestRunCheckSkipsLabelsOnGhError(t *testing.T) {
	root := t.TempDir()
	installLocal(t, root)
	gh := &stubLabelClient{err: errors.New("401 Unauthorized")}
	rep, err := Run(Options{Root: root, Check: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: "o/r"})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !rep.Complete {
		t.Errorf("check with gh failure = incomplete, want complete (warning only): %v", rep.Lines)
	}
	if got := joinLines(rep); !strings.Contains(got, "skip") {
		t.Errorf("check with gh failure missing skip warning:\n%s", got)
	}
}

func TestRunDryRunReportsLabels(t *testing.T) {
	root := t.TempDir()
	gh := &stubLabelClient{labels: []string{"ready"}}
	rep, err := Run(Options{Root: root, DryRun: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: "o/r"})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if rep.Changed {
		t.Error("dry-run Changed = true, want no writes")
	}
	got := joinLines(rep)
	for _, want := range []string{"label/needs-review: missing", "label/ready: ok", "label/blocked: missing"} {
		if !strings.Contains(got, want) {
			t.Errorf("dry-run missing %q:\n%s", want, got)
		}
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("dry-run created files: %v", entries)
	}
}

func TestRunDefaultAppliesChanges(t *testing.T) {
	root := t.TempDir()
	gh := &stubLabelClient{labels: []string{}}
	rep, err := Run(Options{Root: root, Yes: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: "o/r"})
	if err != nil {
		t.Fatalf("default Run: %v", err)
	}
	if !rep.Changed {
		t.Error("default Run Changed = false, want apply-by-default")
	}
	if _, err := os.Stat(filepath.Join(root, agentsFile)); err != nil {
		t.Errorf("default Run did not write AGENTS.md: %v", err)
	}
	if len(gh.created) != len(RequiredIssueLabels) {
		t.Errorf("default Run created = %v, want all required labels", gh.created)
	}
}

func TestRunDefaultNeedsApproval(t *testing.T) {
	root := t.TempDir()
	rep, err := Run(Options{
		Root:         root,
		Stdin:        strings.NewReader(""),
		SkillContent: testSkill,
		AgentsBlock:  testAgents,
	})
	if err != nil {
		t.Fatalf("EOF Run: %v", err)
	}
	if rep.Changed {
		t.Error("non-interactive default Run Changed = true, want abort without approval")
	}
	if got := joinLines(rep); !strings.Contains(got, "aborted") {
		t.Errorf("non-interactive default Run missing abort notice:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(root, agentsFile)); !os.IsNotExist(err) {
		t.Error("non-interactive default Run wrote AGENTS.md")
	}
}

func TestRunDryRunDoesNotCreateLabels(t *testing.T) {
	root := t.TempDir()
	gh := &stubLabelClient{labels: []string{}}
	if _, err := Run(Options{Root: root, DryRun: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: "o/r"}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(gh.created) != 0 {
		t.Errorf("dry-run created labels %v, want no creation without --write", gh.created)
	}
}

func TestRunApplyCreatesMissingLabels(t *testing.T) {
	root := t.TempDir()
	gh := &stubLabelClient{labels: []string{"ready"}}
	rep, err := Run(Options{Root: root, Yes: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: "o/r"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !rep.Changed {
		t.Error("Changed = false, want true after writes")
	}
	wantCreated := []string{"needs-review", "blocked"}
	if !reflect.DeepEqual(gh.created, wantCreated) {
		t.Errorf("created = %v, want %v (present labels must not be recreated)", gh.created, wantCreated)
	}
	got := joinLines(rep)
	for _, want := range []string{"label/needs-review: created", "label/ready: ok", "label/blocked: created"} {
		if !strings.Contains(got, want) {
			t.Errorf("write missing %q:\n%s", want, got)
		}
	}
}

func TestRunApplySkipsCreationWhenLabelsExist(t *testing.T) {
	root := t.TempDir()
	gh := &stubLabelClient{labels: []string{"needs-review", "ready", "blocked"}}
	rep, err := Run(Options{Root: root, Yes: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: "o/r"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(gh.created) != 0 {
		t.Errorf("created = %v, want no creation when all labels exist", gh.created)
	}
	if got := joinLines(rep); !strings.Contains(got, "label/ready: ok") {
		t.Errorf("write missing label/ready ok line:\n%s", got)
	}
}

func TestRunApplyReportsLabelCreateFailure(t *testing.T) {
	root := t.TempDir()
	gh := &stubLabelClient{labels: []string{}, createErr: errors.New("403 Forbidden")}
	rep, err := Run(Options{Root: root, Yes: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: "o/r"})
	if err != nil {
		t.Fatalf("write with label failure = %v, want local install to succeed with a warning", err)
	}
	if got := joinLines(rep); !strings.Contains(got, "label/needs-review: create failed") {
		t.Errorf("write missing create-failed warning:\n%s", got)
	}
	if _, statErr := os.Stat(filepath.Join(root, agentsFile)); statErr != nil {
		t.Errorf("local AGENTS.md was not installed despite label failure: %v", statErr)
	}
}

func TestRunApplySkipsCreationWithoutRepo(t *testing.T) {
	root := t.TempDir()
	gh := &stubLabelClient{labels: []string{}}
	if _, err := Run(Options{Root: root, Yes: true, SkillContent: testSkill, AgentsBlock: testAgents, Gh: gh, Repo: ""}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(gh.created) != 0 {
		t.Errorf("created = %v, want no creation without a remote", gh.created)
	}
}
