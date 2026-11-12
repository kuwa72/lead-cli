package finish

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
)

type fakeSmokeAgent struct {
	out []byte
	err error

	calls int
	dir   string
	argv  []string
	log   string
}

func (f *fakeSmokeAgent) Run(_ context.Context, dir string, argv []string, logPath string) ([]byte, error) {
	f.calls++
	f.dir = dir
	f.argv = append([]string(nil), argv...)
	f.log = logPath
	return f.out, f.err
}

func TestAgentSmoke_Pass(t *testing.T) {
	dir := t.TempDir()
	logDir := t.TempDir()
	ag := &fakeSmokeAgent{out: []byte("all checks green\nSMOKE: pass\n")}
	opts := PostMergeOptions{
		Issue:     36,
		PR:        7,
		Agent:     ag,
		AgentName: "agy",
		LogDir:    logDir,
	}

	if err := AgentSmoke(context.Background(), opts, dir); err != nil {
		t.Fatalf("AgentSmoke = %v, want nil", err)
	}
	if ag.calls != 1 {
		t.Fatalf("agent calls = %d, want 1", ag.calls)
	}
	if ag.dir != dir {
		t.Errorf("agent dir = %q, want %q", ag.dir, dir)
	}
	if len(ag.argv) == 0 || ag.argv[0] != "agy" {
		t.Errorf("agent argv = %v, want agy binary first", ag.argv)
	}
	if !strings.HasPrefix(ag.log, logDir) {
		t.Errorf("logPath = %q, want under %q", ag.log, logDir)
	}
}

func TestAgentSmoke_Fail(t *testing.T) {
	dir := t.TempDir()
	ag := &fakeSmokeAgent{
		out: []byte("npm test failed\nSMOKE: fail\n"),
		err: errors.New("exit status 1"),
	}
	err := AgentSmoke(context.Background(), PostMergeOptions{
		PR:    7,
		Agent: ag,
	}, dir)
	if err == nil {
		t.Fatal("AgentSmoke returned nil on SMOKE: fail, want error")
	}
	if !strings.Contains(err.Error(), "SMOKE: fail") && !strings.Contains(err.Error(), "npm test failed") {
		t.Errorf("error = %v, want smoke failure context", err)
	}
}

func TestAgentSmoke_MissingVerdictFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		out  string
	}{
		{name: "no verdict", out: "built ok, looks fine"},
		{name: "empty output", out: ""},
		{name: "trailing text after pass", out: "SMOKE: pass\nextra commentary\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ag := &fakeSmokeAgent{out: []byte(tc.out)}
			err := AgentSmoke(context.Background(), PostMergeOptions{
				PR:    7,
				Agent: ag,
			}, t.TempDir())
			if err == nil {
				t.Fatalf("AgentSmoke(%q) returned nil, want fail-closed error", tc.out)
			}
		})
	}
}

func TestAgentSmoke_IncludesAgentsMD(t *testing.T) {
	dir := t.TempDir()
	wantHint := "custom smoke verification: make check"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Verification\n"+wantHint+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := &fakeSmokeAgent{out: []byte("SMOKE: pass\n")}
	if err := AgentSmoke(context.Background(), PostMergeOptions{
		Issue: 12,
		PR:    7,
		Agent: ag,
	}, dir); err != nil {
		t.Fatalf("AgentSmoke = %v", err)
	}
	joined := strings.Join(ag.argv, "\n")
	if !strings.Contains(joined, wantHint) {
		t.Errorf("prompt in argv did not include AGENTS.md content %q; argv=%v", wantHint, ag.argv)
	}
}

func TestAgentSmoke_NilAgentReturnsError(t *testing.T) {
	err := AgentSmoke(context.Background(), PostMergeOptions{PR: 7}, t.TempDir())
	if err == nil {
		t.Fatal("AgentSmoke with nil Agent returned nil, want error")
	}
}

func TestAgentSmoke_NonGoRepoDoesNotRequireGo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := &fakeSmokeAgent{out: []byte("node check ok\nSMOKE: pass\n")}
	if err := AgentSmoke(context.Background(), PostMergeOptions{
		PR:    7,
		Agent: ag,
	}, dir); err != nil {
		t.Fatalf("AgentSmoke in Node repo failed: %v", err)
	}
}

func TestPostMerge_AgentSmokeFail_RevertsAndFilesIncident(t *testing.T) {
	fake := baseFake()
	fake.PRMergeCommit = testSHA
	g := &fakePostMergeGit{root: t.TempDir()}
	ag := &fakeSmokeAgent{out: []byte("smoke failed\nSMOKE: fail\n")}

	res, err := PostMerge(context.Background(), fake, PostMergeOptions{
		Issue: 36, PR: 7, Git: g, Repo: "o/r", Agent: ag, AgentName: "agy",
	})
	if err == nil {
		t.Fatal("PostMerge returned nil on agent SMOKE: fail, want error")
	}
	if !res.SmokeFailed || !res.Reverted || res.Incident == 0 || res.RevertPR == 0 {
		t.Errorf("Result = %+v, want SmokeFailed=true Reverted=true Incident!=0 RevertPR!=0", res)
	}
	if len(g.reverted) != 1 || g.reverted[0] != testSHA {
		t.Errorf("reverted = %v, want [%s]", g.reverted, testSHA)
	}
	if len(fake.PRCreates) != 1 || len(fake.Created) != 1 {
		t.Errorf("PRCreates=%d Created=%d, want 1 revert PR and 1 incident", len(fake.PRCreates), len(fake.Created))
	}
}

func TestPostMerge_SetupErrors_DoNotRevert(t *testing.T) {
	cases := []struct {
		name  string
		agent AgentRunner
	}{
		{name: "nil agent", agent: nil},
		{name: "binary not found port error", agent: &fakeSmokeAgent{err: &ports.BinaryNotFoundError{Binary: "agy"}}},
		{name: "exec ErrNotFound", agent: &fakeSmokeAgent{err: exec.ErrNotFound}},
		{name: "missing verdict output", agent: &fakeSmokeAgent{out: []byte("no verdict emitted")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := baseFake()
			fake.PRMergeCommit = testSHA
			g := &fakePostMergeGit{root: t.TempDir()}
			res, err := PostMerge(context.Background(), fake, PostMergeOptions{
				Issue: 36, PR: 7, Git: g, Repo: "o/r", Agent: tc.agent, AgentName: "agy",
			})
			if err == nil {
				t.Fatalf("PostMerge returned nil for %s, want fail-closed error", tc.name)
			}
			if res.Passed || res.SmokeFailed || res.Reverted {
				t.Errorf("Result = %+v, want Passed=false SmokeFailed=false Reverted=false", res)
			}
			if len(g.reverted) != 0 {
				t.Errorf("reverted = %v, want no revert on setup/verdict error", g.reverted)
			}
			if len(fake.Created) != 0 {
				t.Errorf("incident created (%+v) on setup/verdict error", fake.Created)
			}
		})
	}
}
