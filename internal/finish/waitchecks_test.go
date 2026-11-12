package finish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

func TestHasCIWorkflows(t *testing.T) {
	t.Run("empty dir", func(t *testing.T) {
		if HasCIWorkflows(t.TempDir()) {
			t.Fatal("HasCIWorkflows = true in empty dir, want false")
		}
	})
	t.Run("non-yaml files ignored", func(t *testing.T) {
		dir := t.TempDir()
		wfDir := filepath.Join(dir, ".github", "workflows")
		if err := os.MkdirAll(wfDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wfDir, "README.md"), []byte("# ci"), 0o644); err != nil {
			t.Fatal(err)
		}
		if HasCIWorkflows(dir) {
			t.Fatal("HasCIWorkflows = true with only README.md, want false")
		}
	})
	for _, name := range []string{"ci.yml", "ci.yaml"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			wfDir := filepath.Join(dir, ".github", "workflows")
			if err := os.MkdirAll(wfDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wfDir, name), []byte("on: push\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if !HasCIWorkflows(dir) {
				t.Fatalf("HasCIWorkflows = false when %s exists, want true", name)
			}
		})
	}
}

func TestWaitChecksInDir_ZeroChecksWithoutWorkflows_SkipsImmediately(t *testing.T) {
	dir := t.TempDir()
	fake := baseFake()
	fake.Checks = nil
	fake.Files = []string{"src/main.go"}

	err := WaitChecksInDir(context.Background(), fake, 7, dir, 5*time.Millisecond, time.Millisecond, noSleep)
	if err != nil {
		t.Fatalf("WaitChecksInDir returned %v when no workflows exist and 0 checks returned, want nil", err)
	}
}

func TestWaitChecksInDir_ZeroChecksWithWorkflows_StillWaits(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "ci.yml"), []byte("on: pull_request\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := baseFake()
	fake.Checks = nil
	fake.Files = []string{"src/main.go"}

	err := WaitChecksInDir(context.Background(), fake, 7, dir, 15*time.Millisecond, 5*time.Millisecond, time.Sleep)
	if err == nil || !strings.Contains(err.Error(), "timeout waiting for checks") {
		t.Fatalf("WaitChecksInDir = %v, want timeout waiting for checks when workflow exists", err)
	}
}

func TestWaitChecksInDir_ZeroChecksWithWorkflowAddedInPR_StillWaits(t *testing.T) {
	dir := t.TempDir()
	fake := baseFake()
	fake.Checks = nil
	fake.Files = []string{".github/workflows/ci.yml"}

	err := WaitChecksInDir(context.Background(), fake, 7, dir, 15*time.Millisecond, 5*time.Millisecond, time.Sleep)
	if err == nil || !strings.Contains(err.Error(), "timeout waiting for checks") {
		t.Fatalf("WaitChecksInDir = %v, want timeout when PR itself adds CI workflow", err)
	}
}

func TestWaitChecksInDir_ZeroChecksWithPrFilesErr_StillWaits(t *testing.T) {
	dir := t.TempDir()
	fake := baseFake()
	fake.Checks = nil
	fake.FilesErr = errors.New("gh unavailable")

	err := WaitChecksInDir(context.Background(), fake, 7, dir, 15*time.Millisecond, 5*time.Millisecond, time.Sleep)
	if err == nil || !strings.Contains(err.Error(), "timeout waiting for checks") {
		t.Fatalf("WaitChecksInDir = %v, want conservative timeout when PrFiles fails", err)
	}
}

func TestWaitChecksInDir_WithChecksEvenWithoutWorkflows_EnforcesChecks(t *testing.T) {
	dir := t.TempDir()
	fake := baseFake()
	fake.Files = []string{"src/main.go"}

	// Skipping check must still fail even when no local .github/workflows exists.
	fake.Checks = []ports.PRCheck{{Name: "test", Bucket: "skipping"}}
	err := WaitChecksInDir(context.Background(), fake, 7, dir, time.Minute, time.Millisecond, noSleep)
	if err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Fatalf("WaitChecksInDir(skipping) = %v, want skipped error", err)
	}

	fake.Checks = []ports.PRCheck{{Name: "test", Bucket: "fail"}}
	err = WaitChecksInDir(context.Background(), fake, 7, dir, time.Minute, time.Millisecond, noSleep)
	if err == nil || !strings.Contains(err.Error(), "failing") {
		t.Fatalf("WaitChecksInDir(fail) = %v, want failing error", err)
	}
}

func TestRun_ZeroChecksWithoutWorkflows_Merges(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.Checks = nil
	fake.Files = []string{"main.go"}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		WorkDir: t.TempDir(),
		Merge:   true,
		Timeout: 5 * time.Millisecond,
		Sleep:   noSleep,
	})
	if err != nil {
		t.Fatalf("Run failed when no CI workflows exist: %v", err)
	}
	if !res.Merged || !res.Closed {
		t.Errorf("Result = %+v, want Merged=true Closed=true", res)
	}
}
