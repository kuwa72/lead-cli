package projinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	testSkill  = []byte("name: " + skillName + "\ntest skill content\n")
	testAgents = []byte("test agents block\n")
)

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

func TestRunWriteCreatesManagedFiles(t *testing.T) {
	root := t.TempDir()
	rep, err := Run(Options{
		Root:         root,
		Write:        true,
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

func TestRunWriteNeedsApproval(t *testing.T) {
	root := t.TempDir()
	// "n" declines: nothing written.
	if _, err := Run(Options{
		Root:         root,
		Write:        true,
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
		Write:        true,
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
		Write:        true,
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
		Write:        true,
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
		Write:        true,
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
		Write:        true,
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
