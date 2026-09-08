package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates this package's tests from the developer's environment
// (issue #96). Any test that builds the command tree with production
// defaults (NewRootCmd) must not be able to reach the real Herdr session,
// the real repository, or the real state file — even when the shell that
// runs `go test` has HERDR_ENV=1 and a herdr binary on PATH.
//
//   - HERDR_ENV is cleared so herdr.Available() is false: no pane split.
//   - cwd moves to an empty temp dir so real git never sees the checkout.
//   - LEAD_STATE_FILE points into the temp dir so workflows.json is throwaway.
func TestMain(m *testing.M) {
	os.Unsetenv("HERDR_ENV")
	tmp, err := os.MkdirTemp("", "lead-cli-test-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("LEAD_STATE_FILE", filepath.Join(tmp, "state", "workflows.json"))
	if err := os.Chdir(tmp); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}
