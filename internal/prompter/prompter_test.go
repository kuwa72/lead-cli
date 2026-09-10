package prompter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/testutil"
)

func sampleInput() DecomposeInput {
	return DecomposeInput{
		Repository:  "o/r",
		IssueNumber: 25,
		IssueTitle:  "flexible workflow",
		IssueBody:   "support modes",
		RepositoryRules: "TDD required; run ./test/run-tests.sh",
	}
}

func TestRender_MatchesGolden(t *testing.T) {
	out, err := Render(sampleInput())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	testutil.AssertGoldenString(t, "testdata/decompose.golden", out)
}

func TestRender_InjectsAllFields(t *testing.T) {
	out, err := Render(sampleInput())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"o/r", "#25", "flexible workflow", "support modes", "TDD required",
		"受入条件は実行結果", "TDD first step", "Open questions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered prompt missing %q", want)
		}
	}
}

func TestRender_RejectsMissingFields(t *testing.T) {
	for _, mutate := range []func(*DecomposeInput){
		func(d *DecomposeInput) { d.Repository = "" },
		func(d *DecomposeInput) { d.IssueNumber = 0 },
		func(d *DecomposeInput) { d.IssueTitle = "" },
	} {
		in := sampleInput()
		mutate(&in)
		if _, err := Render(in); err == nil {
			t.Errorf("Render(%+v) = nil, want missing-field error", in)
		}
	}
}

func TestLintTitle(t *testing.T) {
	if problems := LintTitle("impl(go): ports adapter"); len(problems) != 0 {
		t.Errorf("LintTitle(good) = %v, want none", problems)
	}
	long := strings.Repeat("あ", 257)
	if problems := LintTitle(long); len(problems) == 0 {
		t.Error("LintTitle(257 runes) = none, want length violation (256以内)")
	} else if !strings.Contains(strings.Join(problems, " "), "256") {
		t.Errorf("length violation = %v, want 256-char guidance", problems)
	}
	if problems := LintTitle(""); len(problems) == 0 {
		t.Error("LintTitle(empty) = none, want violation")
	}
	if problems := LintTitle("trailing period."); len(problems) == 0 {
		t.Error("LintTitle(trailing period) = none, want style violation")
	}
	if problems := LintTitle("two\nlines"); len(problems) == 0 {
		t.Error("LintTitle(multiline) = none, want single-line violation")
	}
}

func TestRenderWorkflowPrompt_Implement(t *testing.T) {
	out, err := RenderWorkflowPrompt("implement", 89, "review workflow", "body text", "")
	if err != nil {
		t.Fatalf("RenderWorkflowPrompt: %v", err)
	}
	for _, want := range []string{"#89", "review workflow", "body text", "TDD"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered implement prompt missing %q", want)
		}
	}
}

func TestRenderWorkflowPrompt_Review(t *testing.T) {
	out, err := RenderWorkflowPrompt("review", 89, "review workflow", "body text", "")
	if err != nil {
		t.Fatalf("RenderWorkflowPrompt: %v", err)
	}
	for _, want := range []string{"#89", "review workflow", "body text", "lead lgtm 89"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered review prompt missing %q", want)
		}
	}
}

func TestRenderWorkflowPrompt_Override(t *testing.T) {
	tmpDir := t.TempDir()
	promptDir := filepath.Join(tmpDir, "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customTpl := "CUSTOM PROMPT #{{.Number}} {{.Title}}"
	if err := os.WriteFile(filepath.Join(promptDir, "review.md"), []byte(customTpl), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := RenderWorkflowPrompt("review", 89, "review workflow", "body text", tmpDir)
	if err != nil {
		t.Fatalf("RenderWorkflowPrompt with override: %v", err)
	}
	if want := "CUSTOM PROMPT #89 review workflow"; out != want {
		t.Errorf("rendered prompt = %q, want %q", out, want)
	}
}

func TestRenderWorkflowPromptWithOptions_TemplateSelection(t *testing.T) {
	// Built-in template selection by name
	out, err := RenderWorkflowPromptWithOptions(PromptOptions{
		Template: "review",
		Number:   84,
		Title:    "template selection",
		Body:     "body text",
	})
	if err != nil {
		t.Fatalf("RenderWorkflowPromptWithOptions: %v", err)
	}
	if !strings.Contains(out, "lead lgtm 84") {
		t.Errorf("expected review template, got:\n%s", out)
	}

	// Unknown template falls back to implement template
	outFallback, err := RenderWorkflowPromptWithOptions(PromptOptions{
		Template: "nonexistent",
		Number:   84,
		Title:    "fallback title",
		Body:     "body text",
	})
	if err != nil {
		t.Fatalf("RenderWorkflowPromptWithOptions fallback: %v", err)
	}
	if !strings.Contains(outFallback, "TDD") {
		t.Errorf("expected fallback to implement template, got:\n%s", outFallback)
	}

	// Repository custom template selection by name
	tmpDir := t.TempDir()
	promptsDir := filepath.Join(tmpDir, "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptsDir, "custom.md"), []byte("CUSTOM TPL #{{.Number}} {{.Title}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	outCustom, err := RenderWorkflowPromptWithOptions(PromptOptions{
		Template: "custom",
		Number:   84,
		Title:    "custom title",
		RepoDir:  tmpDir,
	})
	if err != nil {
		t.Fatalf("RenderWorkflowPromptWithOptions custom: %v", err)
	}
	if want := "CUSTOM TPL #84 custom title"; outCustom != want {
		t.Errorf("got %q, want %q", outCustom, want)
	}
}

func TestRenderWorkflowPromptWithOptions_Variables(t *testing.T) {
	tmpDir := t.TempDir()
	promptsDir := filepath.Join(tmpDir, "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	varTpl := "N:{{.Number}}|T:{{.Title}}|B:{{.Body}}|BR:{{.Branch}}|M:{{.Mode}}|AM:{{.AgentMode}}|R:{{.Rules}}"
	if err := os.WriteFile(filepath.Join(promptsDir, "vars.md"), []byte(varTpl), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := RenderWorkflowPromptWithOptions(PromptOptions{
		Template:  "vars",
		Number:    84,
		Title:     "test title",
		Body:      "test body",
		Branch:    "issue/84-test",
		Mode:      "implement",
		AgentMode: "batch",
		Rules:     "Custom Rules",
		RepoDir:   tmpDir,
	})
	if err != nil {
		t.Fatalf("RenderWorkflowPromptWithOptions: %v", err)
	}
	want := "N:84|T:test title|B:test body|BR:issue/84-test|M:implement|AM:batch|R:Custom Rules"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestRenderWorkflowPromptWithOptions_AutomaticAgentsMD(t *testing.T) {
	tmpDir := t.TempDir()
	agentsContent := "- TDD required\n- Always run tests"
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. With default implement template: should automatically include AGENTS.md in ## プロジェクト規約
	out, err := RenderWorkflowPromptWithOptions(PromptOptions{
		Number:  84,
		Title:   "agents test",
		Body:    "body",
		RepoDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("RenderWorkflowPromptWithOptions: %v", err)
	}
	if !strings.Contains(out, "## プロジェクト規約") {
		t.Errorf("expected '## プロジェクト規約' in prompt, got:\n%s", out)
	}
	if !strings.Contains(out, agentsContent) {
		t.Errorf("expected AGENTS.md content in prompt, got:\n%s", out)
	}

	// 2. Without AGENTS.md: ## プロジェクト規約 should not be present
	emptyDir := t.TempDir()
	outEmpty, err := RenderWorkflowPromptWithOptions(PromptOptions{
		Number:  84,
		Title:   "empty test",
		Body:    "body",
		RepoDir: emptyDir,
	})
	if err != nil {
		t.Fatalf("RenderWorkflowPromptWithOptions: %v", err)
	}
	if strings.Contains(outEmpty, "## プロジェクト規約") {
		t.Errorf("did not expect '## プロジェクト規約' in prompt when AGENTS.md missing, got:\n%s", outEmpty)
	}
}

