package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/testutil"
)

var errBoomAuth = errors.New("auth boom")

func writeExe(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestSetup_CheckWriteUninstallViaCLI(t *testing.T) {
	home := t.TempDir()
	deps := Deps{Home: home, Stdin: strings.NewReader("")}

	_, err := executeWith(t, deps, "setup", "--shell", "bash", "--check")
	if err == nil {
		t.Fatal("setup --check on empty HOME = nil, want incomplete failure")
	}
	out, err := executeWith(t, Deps{Home: home, Stdin: strings.NewReader("")}, "setup", "--shell", "bash")
	if err != nil {
		t.Fatalf("setup dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("dry-run output = %q, want dry-run notice", out)
	}
	out, err = executeWith(t, Deps{Home: home, Stdin: strings.NewReader("")}, "setup",
		"--shell", "bash", "--write", "--yes", "--no-keybinding")
	if err != nil {
		t.Fatalf("setup --write: %v\n%s", err, out)
	}
	if !strings.Contains(out, "doctor:") {
		t.Errorf("setup output = %q, want trailing doctor summary", out)
	}
	if _, err := executeWith(t, Deps{Home: home}, "setup", "--shell", "bash", "--check"); err != nil {
		t.Errorf("setup --check after write = %v, want complete", err)
	}
	if _, err := executeWith(t, Deps{Home: home}, "setup", "--shell", "bash", "--uninstall"); err != nil {
		t.Errorf("setup --uninstall: %v", err)
	}
	if _, err := executeWith(t, Deps{Home: home}, "setup", "--shell", "bash", "--check"); err == nil {
		t.Error("setup --check after uninstall = nil, want incomplete")
	}
}

func TestDoctor_ReportsAndFailsOnAuthViaCLI(t *testing.T) {
	home := t.TempDir()

	out, err := executeWith(t,
		Deps{Gh: &testutil.FakeGhClient{}, Home: home},
		"doctor", "--offline", "--shell", "bash")
	if err != nil {
		t.Fatalf("doctor healthy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ok gh auth") {
		t.Errorf("doctor output = %q, want auth ok", out)
	}

	out, err = executeWith(t,
		Deps{Gh: &testutil.FakeGhClient{AuthErr: errBoomAuth}, Home: home},
		"doctor", "--offline", "--shell", "bash")
	if err == nil {
		t.Fatalf("doctor with auth failure = nil\n%s", out)
	}
	if !strings.Contains(out, "FAIL gh auth") {
		t.Errorf("doctor output = %q, want FAIL gh auth", out)
	}

	out, err = executeWith(t,
		Deps{Gh: &testutil.FakeGhClient{}, Home: home},
		"doctor", "--offline", "--json", "--shell", "bash")
	if err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"required"`) {
		t.Errorf("doctor --json = %q, want machine shape", out)
	}
}

func TestUpdate_CheckViaCLI(t *testing.T) {
	// executeWith stamps Version v0.0.0-test: equal tag = up-to-date.
	out, err := executeWith(t,
		Deps{Gh: &testutil.FakeGhClient{LatestTag: "v0.0.0-test"}, ExePath: "/tmp/lead-test-binary"},
		"update", "--check")
	if err != nil {
		t.Fatalf("update --check up-to-date: %v\n%s", err, out)
	}
	if !strings.Contains(out, "up to date") {
		t.Errorf("update --check output = %q", out)
	}
	// This test binary reports Version v0.0.0-test via executeWith; a newer
	// tag must surface as available (non-zero).
	_, err = executeWith(t,
		Deps{Gh: &testutil.FakeGhClient{LatestTag: "v9.9.9"}, ExePath: "/tmp/lead-test-binary"},
		"update", "--check")
	if err == nil {
		t.Fatal("update --check with newer tag = nil, want available signal")
	}
}

func TestUpdate_BrewManagedViaCLI(t *testing.T) {
	binDir := testutil.TempBin(t)
	brewSh := "#!/bin/sh\nif [ \"$1\" = \"--prefix\" ]; then echo /opt/homebrew; fi\n"
	writeExe(t, binDir, "brew", brewSh)

	out, err := executeWith(t,
		Deps{Gh: &testutil.FakeGhClient{LatestTag: "v9.9.9"},
			ExePath: "/opt/homebrew/Cellar/lead/0.1.0/bin/lead"},
		"update")
	if err != nil {
		t.Fatalf("brew-managed update: %v\n%s", err, out)
	}
	if !strings.Contains(out, "brew upgrade") {
		t.Errorf("brew-managed output = %q, want upgrade guidance", out)
	}
}
