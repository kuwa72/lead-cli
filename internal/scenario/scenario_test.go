package scenario

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseSteps(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want []string
	}{
		{
			name: "numbered list",
			md:   "## Steps\n\n1. first\n2. second\n",
			want: []string{"first", "second"},
		},
		{
			name: "unnumbered list",
			md:   "## Steps\n\n- first\n* second\n+ third\n",
			want: []string{"first", "second", "third"},
		},
		{
			name: "parenthesised numbers",
			md:   "## Steps\n1) first\n2) second\n",
			want: []string{"first", "second"},
		},
		{
			name: "empty section",
			md:   "## Steps\n\n",
			want: nil,
		},
		{
			name: "no steps section",
			md:   "# Scenario\n\n1. not a step\n",
			want: nil,
		},
		{
			name: "code fence is not a step",
			md:   "## Steps\n\n1. run this:\n\n```sh\n2. not a step\n```\n\n2. then that\n",
			want: []string{"run this:", "then that"},
		},
		{
			name: "next section ends steps",
			md:   "## Steps\n1. first\n\n## Notes\n1. not a step\n",
			want: []string{"first"},
		},
		{
			name: "nested list is not a step",
			md:   "## Steps\n1. first\n   - nested detail\n2. second\n",
			want: []string{"first", "second"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.md)
			var texts []string
			for _, s := range got.Steps {
				texts = append(texts, s.Text)
			}
			if !reflect.DeepEqual(texts, tc.want) {
				t.Fatalf("Parse steps = %#v, want %#v", texts, tc.want)
			}
			for i, s := range got.Steps {
				if s.Number != i+1 {
					t.Errorf("step %d numbered %d", i, s.Number)
				}
			}
		})
	}
}

func TestPathKeepsScenariosOutsideTheWorktree(t *testing.T) {
	got := Path("/state/lead", "owner/repo", 42)
	want := filepath.Join("/state/lead", "scenarios", "owner_repo", "42.md")
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	if got := Path("/state/lead", "", 7); got != filepath.Join("/state/lead", "scenarios", "local", "7.md") {
		t.Fatalf("Path without repo slug = %q", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenarios", "o_r", "42.md")
	body := "## Steps\n\n1. one\n"
	if err := Save(path, body); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != body {
		t.Fatalf("Load = %q, want %q", got, body)
	}
}

func TestListReturnsSavedIssues(t *testing.T) {
	stateDir := t.TempDir()
	for _, n := range []int{42, 7} {
		if err := Save(Path(stateDir, "o/r", n), "## Steps\n1. x\n"); err != nil {
			t.Fatalf("Save #%d: %v", n, err)
		}
	}
	got, err := List(stateDir, "o/r")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var issues []int
	for _, e := range got {
		issues = append(issues, e.Issue)
	}
	if !reflect.DeepEqual(issues, []int{7, 42}) {
		t.Fatalf("List issues = %v, want [7 42]", issues)
	}
}

type fakeAgent struct {
	out      []byte
	err      error
	calls    int
	dir      string
	prompt   string
	writeRun string
}

func (a *fakeAgent) Run(_ context.Context, dir string, argv []string, _ string) ([]byte, error) {
	a.calls++
	a.dir = dir
	last := argv[len(argv)-1]
	if i := strings.LastIndex(last, "file: "); i >= 0 {
		path := strings.TrimSpace(last[i+len("file: "):])
		if b, err := os.ReadFile(path); err == nil {
			a.prompt = string(b)
		}
	}
	if a.writeRun != "" {
		out := promptOutputDir(a.prompt)
		if out == "" {
			return nil, os.ErrInvalid
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(out, "run.sh"), []byte(a.writeRun), 0o755); err != nil {
			return nil, err
		}
	}
	return a.out, a.err
}

func promptOutputDir(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "- 出力先: "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func TestRunExecutesGeneratedTestsAndCleansUp(t *testing.T) {
	worktree := t.TempDir()
	scenarioPath := filepath.Join(t.TempDir(), "42.md")
	if err := Save(scenarioPath, "## Steps\n\n1. first\n2. second\n"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	agent := &fakeAgent{
		out:      []byte("SCENARIO 1: pass\nSCENARIO 2: fail\n"),
		writeRun: "#!/bin/sh\necho 'SCENARIO 1: pass'\necho 'SCENARIO 2: fail'\n",
	}
	r := &Runner{Agent: agent, Opts: Options{
		Issue:        42,
		ScenarioPath: scenarioPath,
		WorkDir:      worktree,
		Agent:        "agy",
		LogDir:       t.TempDir(),
	}}
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Score() != "1/2" {
		t.Fatalf("Score = %q, want 1/2 (steps: %+v)", res.Score(), res.Steps)
	}
	if len(res.Steps) != 2 || res.Steps[0].Verdict != VerdictPass || res.Steps[1].Verdict != VerdictFail {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if agent.calls != 1 {
		t.Fatalf("agent calls = %d, want 1", agent.calls)
	}
	if agent.dir != worktree {
		t.Fatalf("agent ran in %q, want worktree %q", agent.dir, worktree)
	}
	if !strings.Contains(agent.prompt, "first") || !strings.Contains(agent.prompt, "second") {
		t.Fatalf("prompt does not carry the scenario steps:\n%s", agent.prompt)
	}
	if _, err := os.Stat(filepath.Join(worktree, workDirName)); !os.IsNotExist(err) {
		t.Fatalf("generated tests were left in the worktree: %v", err)
	}
	entries, err := os.ReadDir(worktree)
	if err != nil {
		t.Fatalf("read worktree: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("worktree is not clean after the run: %v", entries)
	}
}

func TestRunWithoutScenariosReturnsZero(t *testing.T) {
	scenarioPath := filepath.Join(t.TempDir(), "42.md")
	if err := Save(scenarioPath, "## Steps\n\n"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	agent := &fakeAgent{}
	r := &Runner{Agent: agent, Opts: Options{
		Issue:        42,
		ScenarioPath: scenarioPath,
		WorkDir:      t.TempDir(),
		Agent:        "agy",
		LogDir:       t.TempDir(),
	}}
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Score() != "0/0" || res.Total != 0 {
		t.Fatalf("Score = %q, want 0/0", res.Score())
	}
	if agent.calls != 0 {
		t.Fatalf("agent calls = %d, want 0", agent.calls)
	}
}

func TestRunWithoutGeneratedRunnerFails(t *testing.T) {
	scenarioPath := filepath.Join(t.TempDir(), "42.md")
	if err := Save(scenarioPath, "## Steps\n1. only\n"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	agent := &fakeAgent{out: []byte("SCENARIO 1: pass\n")}
	r := &Runner{Agent: agent, Opts: Options{
		Issue:        42,
		ScenarioPath: scenarioPath,
		WorkDir:      t.TempDir(),
		Agent:        "agy",
		LogDir:       t.TempDir(),
	}}
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded without a generated run.sh; agent stdout must not count as execution")
	}
}

func TestRunCancelRemovesWorktreeCopy(t *testing.T) {
	worktree := t.TempDir()
	scenarioPath := filepath.Join(t.TempDir(), "42.md")
	if err := Save(scenarioPath, "## Steps\n1. hangs until cancel\n"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	agent := &fakeAgent{writeRun: "#!/bin/sh\nsleep 30\n"}
	r := &Runner{Agent: agent, Opts: Options{
		Issue:        42,
		ScenarioPath: scenarioPath,
		WorkDir:      worktree,
		Agent:        "agy",
		LogDir:       t.TempDir(),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := r.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("Run error = %v, want an interruption", err)
	}
	if _, statErr := os.Stat(filepath.Join(worktree, workDirName)); !os.IsNotExist(statErr) {
		t.Fatalf("%s left in the worktree after cancel: %v", workDirName, statErr)
	}
}

func TestRunTimesOutHangingTests(t *testing.T) {
	scenarioPath := filepath.Join(t.TempDir(), "42.md")
	if err := Save(scenarioPath, "## Steps\n1. hangs\n"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	agent := &fakeAgent{writeRun: "#!/bin/sh\nsleep 30\n"}
	r := &Runner{Agent: agent, Opts: Options{
		Issue:        42,
		ScenarioPath: scenarioPath,
		WorkDir:      t.TempDir(),
		Agent:        "agy",
		LogDir:       t.TempDir(),
		ExecTimeout:  100 * time.Millisecond,
	}}
	start := time.Now()
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Run error = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s, want a bounded wait", elapsed)
	}
}

// issue #295: ready freezes the issue body's ## シナリオ as a ## Steps
// scenario outside the worktree; a second ready must not overwrite it.
func TestEnsureFromIssue_FreezesOnce(t *testing.T) {
	stateDir := t.TempDir()
	body := "# title\n\n## シナリオ\n\n1. `lead ready 42` → ready が付く\n2. `go test ./...` が通る\n"
	created, err := EnsureFromIssue(stateDir, "o/r", 42, body)
	if err != nil {
		t.Fatalf("EnsureFromIssue: %v", err)
	}
	if !created {
		t.Fatal("EnsureFromIssue did not create the scenario")
	}
	path := Path(stateDir, "o/r", 42)
	raw, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.HasPrefix(raw, "## Steps\n\n1.") {
		t.Errorf("stored scenario = %q, want a ## Steps document", raw)
	}
	if got := len(Parse(raw).Steps); got != 2 {
		t.Errorf("parsed steps = %d, want 2", got)
	}

	created, err = EnsureFromIssue(stateDir, "o/r", 42, "# title\n\n## シナリオ\n\n1. `different`\n")
	if err != nil {
		t.Fatalf("second EnsureFromIssue: %v", err)
	}
	if created {
		t.Error("EnsureFromIssue overwrote an existing scenario")
	}
	raw, _ = Load(path)
	if !strings.Contains(raw, "lead ready 42") {
		t.Errorf("frozen scenario changed: %q", raw)
	}
}

func TestEnsureFromIssue_NoSectionIsANoop(t *testing.T) {
	stateDir := t.TempDir()
	created, err := EnsureFromIssue(stateDir, "o/r", 43, "# title\n\n## 受入条件\n\n- [ ] x\n")
	if err != nil || created {
		t.Fatalf("EnsureFromIssue(no section) = %v, %v; want false, nil", created, err)
	}
	if _, err := os.Stat(Path(stateDir, "o/r", 43)); !os.IsNotExist(err) {
		t.Errorf("no-section issue got a scenario file: %v", err)
	}
}

// issue #295 review: ready and dispatch can race; the freeze must be
// write-once, with exactly one creator.
func TestEnsureFromIssue_ConcurrentWritesOnce(t *testing.T) {
	stateDir := t.TempDir()
	body := "# x\n\n## シナリオ\n\n1. `go test ./...` が通る\n"
	var wg sync.WaitGroup
	created := make(chan bool, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := EnsureFromIssue(stateDir, "o/r", 44, body)
			created <- ok
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(created)
	close(errs)
	n := 0
	for ok := range created {
		if ok {
			n++
		}
	}
	for err := range errs {
		t.Errorf("EnsureFromIssue: %v", err)
	}
	if n != 1 {
		t.Errorf("creators = %d, want exactly 1", n)
	}
	raw, err := Load(Path(stateDir, "o/r", 44))
	if err != nil || !strings.Contains(raw, "## Steps") {
		t.Errorf("frozen scenario = %q, %v", raw, err)
	}
}
