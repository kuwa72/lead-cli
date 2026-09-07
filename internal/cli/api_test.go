package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/server"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

func TestAPISchema_PrintsValidDefinition(t *testing.T) {
	out, err := executeWith(t, Deps{}, "api", "schema")
	if err != nil {
		t.Fatalf("api schema: %v", err)
	}
	var decoded struct {
		Ops []struct {
			Name string `json:"name"`
		} `json:"ops"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("schema not JSON: %v\n%s", err, out)
	}
	if len(decoded.Ops) == 0 {
		t.Error("schema has no ops")
	}
}

func startTestServer(t *testing.T, repo string) (string, *testutil.FakeGhClient) {
	t.Helper()
	fake := &testutil.FakeGhClient{
		Summaries: []ports.IssueSummary{{Number: 36, Title: "ports adapter"}},
		Issues: map[int]ports.Issue{
			36: {Number: 36, Title: "ports adapter", Body: "body", State: "OPEN"},
		},
		Checks: []ports.PRCheck{{Name: "test", Bucket: "pass"}},
	}
	sock := filepath.Join(t.TempDir(), "lead.sock")
	srv, err := server.Listen(sock, server.Deps{
		Gh: fake, Git: git.New(),
		StateFile: filepath.Join(t.TempDir(), "wf.json"),
		WorkDir:   repo, Version: "v0.0.0-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { srv.Close() })
	return sock, fake
}

func TestAPISnapshot_QueriesServer(t *testing.T) {
	repo := initRepo(t)
	sock, _ := startTestServer(t, repo)

	out, err := executeWith(t, Deps{}, "api", "snapshot", "--socket", sock)
	if err != nil {
		t.Fatalf("api snapshot: %v\n%s", err, out)
	}
	var decoded struct {
		Workflows []any  `json:"workflows"`
		Version   string `json:"version"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("snapshot not JSON: %v\n%s", err, out)
	}
	if decoded.Version != "v0.0.0-test" {
		t.Errorf("snapshot version = %q", decoded.Version)
	}
}

func TestAPICall_DispatchesWork(t *testing.T) {
	repo := initRepo(t)
	sock, _ := startTestServer(t, repo)

	out, err := executeWith(t, Deps{WorkDir: repo}, "api", "call", "work.dispatch",
		"--socket", sock, "--args", `{"issue":36}`)
	if err != nil {
		t.Fatalf("api call work.dispatch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "issue/36-ports-adapter") {
		t.Errorf("dispatch output = %q, want branch", out)
	}
	if ok, err := git.New().BranchExists(repo, "issue/36-ports-adapter"); err != nil || !ok {
		t.Errorf("branch exists = %v, %v", ok, err)
	}
}

func TestAPICall_UnknownOpFails(t *testing.T) {
	repo := initRepo(t)
	sock, _ := startTestServer(t, repo)

	if _, err := executeWith(t, Deps{}, "api", "call", "bogus.op", "--socket", sock); err == nil {
		t.Error("api call bogus.op = nil, want error")
	}
}

func TestAPISnapshot_MissingServerFails(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nope.sock")
	if _, err := executeWith(t, Deps{}, "api", "snapshot", "--socket", sock); err == nil {
		t.Error("api snapshot with no server = nil, want refusal")
	}
}
