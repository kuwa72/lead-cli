package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/dispatch"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// issue #214: inbox-config.json の agent を say/dispatch/run(work)/resume の
// 既定エージェントとして使う。優先順位は --agent > ($LEAD_SPEC_AGENT は say のみ)
// > inbox-config.json の agent > agy。resume だけは --agent の次に state 記録の
// 前回エージェントが来る。

// writeInboxConfig writes inbox-config.json beside the state file.
func writeInboxConfig(t *testing.T, stateFile, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(stateFile), "inbox-config.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSay_InboxConfigAgentIsDefault(t *testing.T) {
	fake := &testutil.FakeGhClient{}
	ag := &recordingSpecAgent{Output: `[{"title":"t","body":"b"}]`}
	stateFile := filepath.Join(t.TempDir(), "state", "workflows.json")
	writeInboxConfig(t, stateFile, `{"agent":"claude"}`)
	deps := Deps{Gh: fake, SpecAgent: ag, StateFile: stateFile, WorkDir: t.TempDir()}

	if _, err := executeSay(t, deps, "x", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if len(ag.Argv) != 1 || ag.Argv[0][0] != "claude" {
		t.Fatalf("argv = %q, want headless claude (inbox-config agent)", ag.Argv)
	}
}

func TestSay_InboxConfigAgentPriority(t *testing.T) {
	newDeps := func(t *testing.T) (*recordingSpecAgent, Deps) {
		ag := &recordingSpecAgent{Output: `[{"title":"t","body":"b"}]`}
		stateFile := filepath.Join(t.TempDir(), "state", "workflows.json")
		writeInboxConfig(t, stateFile, `{"agent":"claude"}`)
		return ag, Deps{Gh: &testutil.FakeGhClient{}, SpecAgent: ag, StateFile: stateFile, WorkDir: t.TempDir()}
	}

	// --agent beats the config pick.
	ag, deps := newDeps(t)
	if _, err := executeSay(t, deps, "x", "--dry-run", "--agent", "codex"); err != nil {
		t.Fatal(err)
	}
	if ag.Argv[0][0] != "codex" {
		t.Errorf("--agent over config: argv[0] = %q, want codex", ag.Argv[0][0])
	}

	// LEAD_SPEC_AGENT beats the config pick (say only).
	t.Setenv("LEAD_SPEC_AGENT", "codex")
	ag, deps = newDeps(t)
	if _, err := executeSay(t, deps, "x", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if ag.Argv[0][0] != "codex" {
		t.Errorf("LEAD_SPEC_AGENT over config: argv[0] = %q, want codex", ag.Argv[0][0])
	}
}

func TestSay_BadInboxConfigFallsBackToAgy(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt": `{broken`,
		"empty":   `{"agent":""}`,
	} {
		ag := &recordingSpecAgent{Output: `[{"title":"t","body":"b"}]`}
		stateFile := filepath.Join(t.TempDir(), "state", "workflows.json")
		writeInboxConfig(t, stateFile, content)
		deps := Deps{Gh: &testutil.FakeGhClient{}, SpecAgent: ag, StateFile: stateFile, WorkDir: t.TempDir()}
		if _, err := executeSay(t, deps, "x", "--dry-run"); err != nil {
			t.Fatalf("%s config: %v", name, err)
		}
		if ag.Argv[0][0] != "agy" {
			t.Errorf("%s config: argv[0] = %q, want agy fallback", name, ag.Argv[0][0])
		}
	}
}

// recordingLauncher records the headless argv dispatch hands to the agent.
type recordingLauncher struct {
	argv [][]string
}

func (l *recordingLauncher) Start(ctx context.Context, dir string, argv []string, logPath string) (dispatch.Process, error) {
	l.argv = append(l.argv, append([]string(nil), argv...))
	return nopProcess{}, nil
}

type nopProcess struct{}

func (nopProcess) Pid() int    { return 1 << 30 }
func (nopProcess) Wait() error { return nil }

func dispatchDeps(t *testing.T) (Deps, *recordingLauncher) {
	t.Helper()
	root := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "state", "workflows.json")
	l := &recordingLauncher{}
	deps := Deps{
		Gh: &testutil.FakeGhClient{
			Labeled: map[string][]ports.IssueSummary{"ready": {{Number: 7, Title: "dispatch me"}}},
			Issues:  map[int]ports.Issue{7: {Number: 7, Title: "dispatch me", Body: "b", State: "OPEN"}},
		},
		Git:       &fakeGitRunner{root: root, origin: "https://github.com/o/r.git"},
		StateFile: stateFile,
		WorkDir:   root,
		Launcher:  l,
	}
	return deps, l
}

func TestDispatch_InboxConfigAgentIsDefault(t *testing.T) {
	deps, l := dispatchDeps(t)
	writeInboxConfig(t, deps.StateFile, `{"agent":"claude"}`)

	out, err := executeWith(t, deps, "dispatch", "--once")
	if err != nil {
		t.Fatalf("dispatch --once: %v\n%s", err, out)
	}
	if len(l.argv) != 1 || l.argv[0][0] != "claude" || l.argv[0][1] != "-p" {
		t.Fatalf("launcher argv = %q, want claude -p ...", l.argv)
	}
}

func TestDispatch_AgentFlagBeatsInboxConfig(t *testing.T) {
	deps, l := dispatchDeps(t)
	writeInboxConfig(t, deps.StateFile, `{"agent":"claude"}`)

	if _, err := executeWith(t, deps, "dispatch", "--once", "--agent", "codex"); err != nil {
		t.Fatal(err)
	}
	if len(l.argv) != 1 || l.argv[0][0] != "codex" {
		t.Fatalf("--agent over config: argv = %q, want codex ...", l.argv)
	}
}

func TestDispatch_NoInboxConfigFallsBackToAgy(t *testing.T) {
	deps, l := dispatchDeps(t)

	if _, err := executeWith(t, deps, "dispatch", "--once"); err != nil {
		t.Fatal(err)
	}
	if len(l.argv) != 1 || l.argv[0][0] != "agy" {
		t.Fatalf("no config: argv = %q, want agy fallback", l.argv)
	}
}

func TestWork_InboxConfigAgentIsDefault(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)
	writeInboxConfig(t, stateFile, `{"agent":"claude"}`)
	h := &testutil.FakeHerdrRunner{}
	deps.Herdr = h

	out, err := executeWith(t, deps, "work", "36")
	if err != nil {
		t.Fatalf("work 36: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Agent: claude") {
		t.Errorf("output missing 'Agent: claude':\n%s", out)
	}
	if len(h.Sends) != 1 || !strings.Contains(h.Sends[0].Text, `claude "`) {
		t.Errorf("send-text = %+v, want `claude \"<prompt>\"`", h.Sends)
	}
}

func TestWork_AgentFlagBeatsInboxConfig(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)
	writeInboxConfig(t, stateFile, `{"agent":"claude"}`)
	h := &testutil.FakeHerdrRunner{}
	deps.Herdr = h

	out, err := executeWith(t, deps, "run", "36", "--agent", "devin")
	if err != nil {
		t.Fatalf("run 36 --agent devin: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Agent: devin") {
		t.Errorf("output missing 'Agent: devin':\n%s", out)
	}
	if len(h.Sends) != 1 || !strings.Contains(h.Sends[0].Text, `devin "`) {
		t.Errorf("send-text = %+v, want `devin \"<prompt>\"`", h.Sends)
	}
}

// The picker path (no issue number): a selection without an explicit agent
// rides the same config chain; an explicit one (LEAD_TEST_SELECTION=36:devin)
// is kept as before.
func TestWork_PickerWithoutAgentUsesInboxConfig(t *testing.T) {
	repo := initRepo(t)
	deps, fake, stateFile := workflowDeps(t, repo)
	fake.Summaries = []ports.IssueSummary{{Number: 36, Title: "ports adapter"}}
	writeInboxConfig(t, stateFile, `{"agent":"claude"}`)
	h := &testutil.FakeHerdrRunner{}
	deps.Herdr = h
	t.Setenv("LEAD_TEST_SELECTION", "36")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	out, err := executeWith(t, deps, "run")
	if err != nil {
		t.Fatalf("run (picker): %v\n%s", err, out)
	}
	if !strings.Contains(out, "Agent: claude") {
		t.Errorf("picker without agent should use config, got:\n%s", out)
	}
}

func TestWork_PickerExplicitAgentBeatsInboxConfig(t *testing.T) {
	repo := initRepo(t)
	deps, fake, stateFile := workflowDeps(t, repo)
	fake.Summaries = []ports.IssueSummary{{Number: 36, Title: "ports adapter"}}
	writeInboxConfig(t, stateFile, `{"agent":"claude"}`)
	deps.Herdr = &testutil.FakeHerdrRunner{}
	t.Setenv("LEAD_TEST_SELECTION", "36:devin")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	out, err := executeWith(t, deps, "run")
	if err != nil {
		t.Fatalf("run (picker devin): %v\n%s", err, out)
	}
	if !strings.Contains(out, "Agent: devin") {
		t.Errorf("explicit picker agent must beat config, got:\n%s", out)
	}
}

// seedResumeRecord creates the issue branch and records an in-progress
// workflow for issue 36 with the given previously recorded agent and no
// prepared pane.
func seedResumeRecord(t *testing.T, repo, stateFile, prevAgent string) {
	t.Helper()
	if err := git.New().CreateBranch(repo, "issue/36-ports-adapter"); err != nil {
		t.Fatal(err)
	}
	store := &state.Store{Path: stateFile}
	if err := store.Upsert(state.Workflow{
		Issue: 36, Repository: "o/r", Branch: "issue/36-ports-adapter",
		Status: state.StatusInProgress, Agent: prevAgent,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResume_InboxConfigAgentIsDefault(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)
	writeInboxConfig(t, stateFile, `{"agent":"claude"}`)
	seedResumeRecord(t, repo, stateFile, "")
	h := &testutil.FakeHerdrRunner{}
	deps.Herdr = h

	out, err := executeWith(t, deps, "resume", "36")
	if err != nil {
		t.Fatalf("resume 36: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Agent: claude") {
		t.Errorf("resume without recorded agent should use config, got:\n%s", out)
	}
	if len(h.Sends) != 1 || !strings.Contains(h.Sends[0].Text, `claude "`) {
		t.Errorf("send-text = %+v, want `claude \"<prompt>\"`", h.Sends)
	}
}

func TestResume_PreviousAgentBeatsInboxConfig(t *testing.T) {
	repo := initRepo(t)
	deps, _, stateFile := workflowDeps(t, repo)
	writeInboxConfig(t, stateFile, `{"agent":"claude"}`)
	seedResumeRecord(t, repo, stateFile, "devin")
	h := &testutil.FakeHerdrRunner{}
	deps.Herdr = h

	out, err := executeWith(t, deps, "resume", "36")
	if err != nil {
		t.Fatalf("resume 36: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Agent: devin") {
		t.Errorf("recorded agent must beat config, got:\n%s", out)
	}
}
