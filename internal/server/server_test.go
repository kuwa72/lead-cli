package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

func testDeps(t *testing.T) (Deps, *testutil.FakeGhClient, string) {
	t.Helper()
	fake := &testutil.FakeGhClient{
		Summaries: []ports.IssueSummary{{Number: 36, Title: "ports adapter"}},
		Issues: map[int]ports.Issue{
			36: {Number: 36, Title: "ports adapter", Body: "body", State: "OPEN"},
		},
		Checks: []ports.PRCheck{{Name: "test", Bucket: "pass"}},
		PR:     ports.PRInfo{Number: 7, State: "OPEN", Mergeable: "MERGEABLE"},
	}
	stateFile := filepath.Join(t.TempDir(), "wf.json")
	return Deps{
		Gh: fake, Git: git.New(), StateFile: stateFile,
		WorkDir: t.TempDir(), Version: "v0.0.0-test",
	}, fake, stateFile
}

func serve(t *testing.T, deps Deps) (sock string, close func()) {
	t.Helper()
	sock = filepath.Join(t.TempDir(), "lead.sock")
	srv, err := Listen(sock, deps)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { srv.Close() })
	return sock, func() { srv.Close() }
}

func callOp(t *testing.T, sock, op string, args any) map[string]any {
	t.Helper()
	raw, err := Call(sock, op, args)
	if err != nil {
		t.Fatalf("Call %s: %v", op, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s response: %v\n%s", op, err, raw)
	}
	return decoded
}

func TestSnapshot_ListsWorkflows(t *testing.T) {
	deps, _, _ := testDeps(t)
	sock, _ := serve(t, deps)

	got := callOp(t, sock, "snapshot", nil)
	if got["version"] != "v0.0.0-test" {
		t.Errorf("snapshot version = %v", got["version"])
	}
	if wfs, ok := got["workflows"].([]any); !ok || len(wfs) != 0 {
		t.Errorf("workflows = %v, want empty list", got["workflows"])
	}
}

func TestIssuesList_ServesGhSummaries(t *testing.T) {
	deps, _, _ := testDeps(t)
	sock, _ := serve(t, deps)

	got := callOp(t, sock, "issues.list", nil)
	items, ok := got["issues"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("issues = %v, want one summary", got["issues"])
	}
	if items[0].(map[string]any)["number"] != float64(36) {
		t.Errorf("issue = %v, want #36", items[0])
	}
}

func TestWorkDispatch_MatchesCLIResult(t *testing.T) {
	deps, _, stateFile := testDeps(t)
	// WorkDir needs a git repo for dispatch.
	repo := initGitRepo(t)
	deps.WorkDir = repo
	sock, _ := serve(t, deps)

	got := callOp(t, sock, "work.dispatch", map[string]any{"issue": 36})
	if got["branch"] != "issue/36-ports-adapter" {
		t.Errorf("dispatch = %v, want branch issue/36-ports-adapter", got)
	}
	if got["status"] != "in_progress" {
		t.Errorf("dispatch = %v, want in_progress", got)
	}
	// Same record the CLI path produces (equivalence): branch recorded.
	store := testStore(stateFile)
	w, ok, err := store.Get(36, "")
	if err != nil || !ok || w.Branch != "issue/36-ports-adapter" {
		t.Errorf("record = %+v, %v, %v; want CLI-identical entry", w, ok, err)
	}
	// Idempotent re-dispatch.
	if _, err := Call(sock, "work.dispatch", map[string]any{"issue": 36}); err != nil {
		t.Errorf("re-dispatch: %v", err)
	}
}

func TestPromptStage_ThenSnapshotShowsIt(t *testing.T) {
	deps, _, _ := testDeps(t)
	sock, _ := serve(t, deps)

	staged := callOp(t, sock, "prompt.stage", map[string]any{"issue": 36, "prompt": "do it"})
	id, ok := staged["id"].(string)
	if !ok || !strings.HasPrefix(id, "st-") {
		t.Fatalf("stage = %v, want staged id", staged)
	}
	snap := callOp(t, sock, "snapshot", nil)
	stagedList, ok := snap["staged"].([]any)
	if !ok || len(stagedList) != 1 {
		t.Errorf("snapshot staged = %v, want one entry (review, never auto-sent)", snap["staged"])
	}
}

func TestCIStatus_ReportsChecks(t *testing.T) {
	deps, _, _ := testDeps(t)
	sock, _ := serve(t, deps)

	got := callOp(t, sock, "ci.status", map[string]any{"pr": 7})
	if got["all_pass"] != true {
		t.Errorf("ci.status = %v, want all_pass", got)
	}
}

func TestNotify_AppearsInSnapshot(t *testing.T) {
	deps, _, _ := testDeps(t)
	sock, _ := serve(t, deps)

	if _, err := Call(sock, "notify", map[string]any{"message": "hello"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	snap := callOp(t, sock, "snapshot", nil)
	notes, ok := snap["notifications"].([]any)
	if !ok || len(notes) != 1 {
		t.Errorf("notifications = %v, want one entry", snap["notifications"])
	}
}

func TestUnknownOp_Errors(t *testing.T) {
	deps, _, _ := testDeps(t)
	sock, _ := serve(t, deps)

	if _, err := Call(sock, "bogus.op", nil); err == nil {
		t.Error("unknown op = nil, want error")
	}
}

func TestSocket_PermissionsLockedDown(t *testing.T) {
	deps, _, _ := testDeps(t)
	dir := t.TempDir()
	sock := filepath.Join(dir, "lead.sock")
	srv, err := Listen(sock, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	if st, err := os.Stat(sock); err != nil {
		t.Fatal(err)
	} else if st.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %o, want 600 (owner only)", st.Mode().Perm())
	}
	if st, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if st.Mode().Perm() != 0o700 {
		t.Errorf("socket dir mode = %o, want 700", st.Mode().Perm())
	}
}

func TestDial_MissingServerRefused(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nope.sock")
	if _, err := Call(sock, "snapshot", nil); err == nil {
		t.Error("Call with no server = nil, want connection refusal")
	}
}

func TestSchema_ListsOps(t *testing.T) {
	raw := Schema()
	var decoded struct {
		Ops []struct {
			Name string `json:"name"`
		} `json:"ops"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	want := []string{"snapshot", "issues.list", "work.dispatch", "prompt.stage", "ci.status", "notify"}
	names := map[string]bool{}
	for _, op := range decoded.Ops {
		names[op.Name] = true
	}
	for _, w := range want {
		if !names[w] {
			t.Errorf("schema missing op %q", w)
		}
	}
}

func TestWorkDispatch_UnknownIssueFails(t *testing.T) {
	deps, _, _ := testDeps(t)
	deps.WorkDir = initGitRepo(t)
	sock, _ := serve(t, deps)

	if _, err := Call(sock, "work.dispatch", map[string]any{"issue": 999}); err == nil {
		t.Error("dispatch unknown issue = nil, want propagation")
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func testStore(path string) *state.Store { return &state.Store{Path: path} }
