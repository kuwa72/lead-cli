package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// recordingSpecAgent stands in for the headless spec agent (issue #94).
type recordingSpecAgent struct {
	Output string
	Argv   [][]string
	Dirs   []string
	Logs   []string
}

func (a *recordingSpecAgent) Run(ctx context.Context, dir string, argv []string, logPath string) ([]byte, error) {
	a.Argv = append(a.Argv, argv)
	a.Dirs = append(a.Dirs, dir)
	a.Logs = append(a.Logs, logPath)
	return []byte(a.Output), nil
}

func executeSay(t *testing.T, deps Deps, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmdWithDeps("v0.0.0-test", "abc1234", "2026-09-08", deps)
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"say"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestSay_Help(t *testing.T) {
	stdout, _, err := execute(t, "say", "--help")
	if err != nil {
		t.Fatalf("lead say --help: %v", err)
	}
	for _, want := range []string{"lead say", "--agent", "--dry-run", "--follow-up", "--redraft"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help missing %q:\n%s", want, stdout)
		}
	}
}

func TestSay_RequiresOneLiner(t *testing.T) {
	if _, _, err := execute(t, "say"); err == nil {
		t.Error("lead say without one-liner succeeded")
	}
}

func TestSay_WiresAgentFlagAndCreatesIssue(t *testing.T) {
	fake := &testutil.FakeGhClient{Issues: map[int]ports.Issue{}}
	ag := &recordingSpecAgent{Output: `[{"title":"feat(cli): x","body":"## Purpose\nx"}]`}
	stateFile := filepath.Join(t.TempDir(), "state", "workflows.json")
	deps := Deps{Gh: fake, SpecAgent: ag, StateFile: stateFile, WorkDir: t.TempDir()}

	out, err := executeSay(t, deps, "make x work", "--agent", "claude")
	if err != nil {
		t.Fatalf("lead say: %v\n%s", err, out)
	}
	if len(ag.Argv) != 1 || ag.Argv[0][0] != "claude" || ag.Argv[0][1] != "-p" || ag.Argv[0][2] != "--dangerously-skip-permissions" {
		t.Fatalf("agent argv = %q", ag.Argv)
	}
	if !strings.Contains(ag.Argv[0][3], "make x work") {
		t.Error("prompt lacks one-liner")
	}
	if ag.Dirs[0] != deps.WorkDir {
		t.Errorf("agent dir = %q, want %q", ag.Dirs[0], deps.WorkDir)
	}
	if want := filepath.Join(filepath.Dir(stateFile), "logs"); !strings.HasPrefix(ag.Logs[0], want+string(filepath.Separator)+"say-") {
		t.Errorf("log path = %q, want under %s/say-*", ag.Logs[0], want)
	}
	if len(fake.Created) != 1 || fake.Created[0].Title != "feat(cli): x" || fake.Created[0].Labels[0] != "needs-review" {
		t.Errorf("created = %+v", fake.Created)
	}
	if !strings.Contains(out, "created #101") {
		t.Errorf("output = %q", out)
	}
}

func TestSay_AgentFromEnvWhenFlagAbsent(t *testing.T) {
	t.Setenv("LEAD_SPEC_AGENT", "codex")
	fake := &testutil.FakeGhClient{}
	ag := &recordingSpecAgent{Output: `[{"title":"t","body":"b"}]`}
	deps := Deps{Gh: fake, SpecAgent: ag, StateFile: filepath.Join(t.TempDir(), "w.json"), WorkDir: t.TempDir()}
	if _, err := executeSay(t, deps, "x", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if ag.Argv[0][0] != "codex" || ag.Argv[0][1] != "exec" {
		t.Errorf("argv = %q", ag.Argv[0])
	}
	if len(fake.Created) != 0 {
		t.Error("dry-run created issues")
	}
}

func TestSay_FollowUpAndRedraftFlagsReachRunner(t *testing.T) {
	fake := &testutil.FakeGhClient{Issues: map[int]ports.Issue{42: {Number: 42, Title: "parent", Body: "p"}}}
	ag := &recordingSpecAgent{Output: `[{"title":"fix: child","body":"no ref"}]`}
	deps := Deps{Gh: fake, SpecAgent: ag, StateFile: filepath.Join(t.TempDir(), "w.json"), WorkDir: t.TempDir()}
	if _, err := executeSay(t, deps, "ng", "--agent", "claude", "--follow-up", "42"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Created) != 1 || !strings.Contains(fake.Created[0].Body, "#42") {
		t.Errorf("follow-up body = %+v", fake.Created)
	}

	fake2 := &testutil.FakeGhClient{Issues: map[int]ports.Issue{7: {Number: 7, Title: "old", Body: "old body"}}}
	ag2 := &recordingSpecAgent{Output: `{"title":"old","body":"new body"}`}
	deps2 := Deps{Gh: fake2, SpecAgent: ag2, StateFile: filepath.Join(t.TempDir(), "w.json"), WorkDir: t.TempDir()}
	if _, err := executeSay(t, deps2, "tweak", "--agent", "claude", "--redraft", "7"); err != nil {
		t.Fatal(err)
	}
	if len(fake2.Edits) != 1 || fake2.Edits[0].Number != 7 || fake2.Edits[0].Body != "new body" || len(fake2.Comments) != 1 || len(fake2.Created) != 0 {
		t.Errorf("redraft: edits=%+v comments=%+v created=%+v", fake2.Edits, fake2.Comments, fake2.Created)
	}

	if _, err := executeSay(t, deps, "x", "--follow-up", "1", "--redraft", "2"); err == nil {
		t.Error("follow-up + redraft accepted")
	}
}
