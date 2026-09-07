package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectShell(t *testing.T) {
	if got, err := DetectShell("zsh"); err != nil || got != ShellZsh {
		t.Errorf("DetectShell(zsh) = %q, %v", got, err)
	}
	t.Setenv("SHELL", "/bin/bash")
	if got, err := DetectShell(""); err != nil || got != ShellBash {
		t.Errorf("DetectShell($SHELL) = %q, %v", got, err)
	}
	if _, err := DetectShell("csh"); err == nil {
		t.Error("DetectShell(csh) = nil, want unsupported error")
	}
	t.Setenv("SHELL", "")
	if _, err := DetectShell(""); err == nil {
		t.Error("DetectShell(empty) = nil, want error")
	}
}

func TestPathsFor(t *testing.T) {
	home := "/tmp/fakehome"
	p := PathsFor(ShellBash, home)
	if p.CompletionFile != filepath.Join(home, ".local/share/bash-completion/completions/lead") {
		t.Errorf("bash completion = %q", p.CompletionFile)
	}
	if p.RCFile != filepath.Join(home, ".bashrc") {
		t.Errorf("bash rc = %q", p.RCFile)
	}
	if got := PathsFor(ShellZsh, home).CompletionFile; got != filepath.Join(home, ".zsh/completions/_lead") {
		t.Errorf("zsh completion = %q", got)
	}
	if got := PathsFor(ShellFish, home).CompletionFile; got != filepath.Join(home, ".config/fish/completions/lead.fish") {
		t.Errorf("fish completion = %q", got)
	}
	if got := PathsFor(ShellFish, home).RCFile; got != filepath.Join(home, ".config/fish/config.fish") {
		t.Errorf("fish rc = %q", got)
	}
}

func TestBashCompletionFallbackDir(t *testing.T) {
	home := t.TempDir()
	// Neither parent exists → primary path.
	if got := bashCompletionFile(home); !strings.HasSuffix(got, "bash-completion/completions/lead") {
		t.Errorf("default bash completion = %q", got)
	}
	// Only the fallback parent exists → fallback path.
	home2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home2, ".bash_completion.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := bashCompletionFile(home2); got != filepath.Join(home2, ".bash_completion.d/lead") {
		t.Errorf("fallback bash completion = %q", got)
	}
}

func TestBlockUpsertRemoveRoundTrip(t *testing.T) {
	plain := "export FOO=1\n"
	with := UpsertBlock(plain, "source X")
	if !HasBlock(with) {
		t.Fatalf("UpsertBlock missing markers:\n%s", with)
	}
	if got := strings.Count(with, BlockStart); got != 1 {
		t.Errorf("block markers = %d, want exactly 1", got)
	}
	// Idempotent: second upsert replaces instead of duplicating.
	again := UpsertBlock(with, "source Y")
	if got := strings.Count(again, BlockStart); got != 1 {
		t.Errorf("re-upsert markers = %d, want 1", got)
	}
	if strings.Contains(again, "source X") {
		t.Errorf("re-upsert kept old snippet:\n%s", again)
	}
	removed, ok := RemoveBlock(again)
	if !ok || HasBlock(removed) {
		t.Errorf("RemoveBlock = %q, %v; want block gone", removed, ok)
	}
	if !strings.Contains(removed, "export FOO=1") {
		t.Errorf("RemoveBlock dropped user content: %q", removed)
	}
	if _, ok := RemoveBlock(plain); ok {
		t.Error("RemoveBlock on plain content = true, want false")
	}
}

func fakeGen(t *testing.T) func(Shell) (string, error) {
	t.Helper()
	return func(sh Shell) (string, error) { return "# completion for lead (" + string(sh) + ")\n", nil }
}

func TestRunDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	rep, err := Run(Options{Home: home, Sh: ShellBash, GenCompletion: fakeGen(t)})
	if err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}
	if rep.Changed {
		t.Error("dry-run Changed = true, want no writes without --write")
	}
	if len(rep.Lines) == 0 {
		t.Error("dry-run produced no preview lines")
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Errorf("dry-run created files in HOME: %v", entries)
	}
}

func TestRunWriteCreatesManagedFiles(t *testing.T) {
	home := t.TempDir()
	rep, err := Run(Options{Home: home, Sh: ShellBash, Write: true, Yes: true, GenCompletion: fakeGen(t)})
	if err != nil {
		t.Fatalf("Run --write: %v", err)
	}
	if !rep.Changed {
		t.Error("Changed = false, want true after writes")
	}
	rc, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	if err != nil {
		t.Fatalf("rc not created: %v", err)
	}
	if !HasBlock(string(rc)) {
		t.Errorf("rc missing managed block:\n%s", rc)
	}
	comp, err := os.ReadFile(filepath.Join(home, ".local/share/bash-completion/completions/lead"))
	if err != nil {
		t.Fatalf("completion not placed: %v", err)
	}
	if !strings.Contains(string(comp), "completion for lead") {
		t.Errorf("completion content = %q", comp)
	}
	// Idempotent: second --write changes nothing.
	rep2, err := Run(Options{Home: home, Sh: ShellBash, Write: true, Yes: true, GenCompletion: fakeGen(t)})
	if err != nil {
		t.Fatalf("re-Run --write: %v", err)
	}
	if rep2.Changed {
		t.Errorf("re-write Changed = true, want idempotent no-op (lines: %v)", rep2.Lines)
	}
}

func TestRunWriteNeedsApproval(t *testing.T) {
	home := t.TempDir()
	// "n" declines: nothing written.
	if _, err := Run(Options{Home: home, Sh: ShellBash, Write: true, Stdin: strings.NewReader("n\n"), GenCompletion: fakeGen(t)}); err != nil {
		t.Fatalf("declined Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Error("declined setup wrote rc file")
	}
	// EOF (non-interactive) defaults to no.
	if _, err := Run(Options{Home: home, Sh: ShellBash, Write: true, Stdin: strings.NewReader(""), GenCompletion: fakeGen(t)}); err != nil {
		t.Fatalf("EOF Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Error("non-interactive setup wrote rc file without approval")
	}
	// "y" approves (plus keybinding answer "n").
	if _, err := Run(Options{Home: home, Sh: ShellBash, Write: true, Stdin: strings.NewReader("n\ny\n"), GenCompletion: fakeGen(t)}); err != nil {
		t.Fatalf("approved Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); err != nil {
		t.Errorf("approved setup did not write rc: %v", err)
	}
}

func TestKeybindingOptIn(t *testing.T) {
	home := t.TempDir()
	// Default (even with approval): no keybinding lines.
	if _, err := Run(Options{Home: home, Sh: ShellBash, Write: true, Yes: true, GenCompletion: fakeGen(t)}); err != nil {
		t.Fatal(err)
	}
	rc, _ := os.ReadFile(filepath.Join(home, ".bashrc"))
	if strings.Contains(string(rc), "C-g") {
		t.Errorf("default setup enabled keybinding:\n%s", rc)
	}
	// Interactive "y" to the keybinding question enables it.
	home2 := t.TempDir()
	if _, err := Run(Options{Home: home2, Sh: ShellBash, Write: true, Stdin: strings.NewReader("y\ny\n"), GenCompletion: fakeGen(t)}); err != nil {
		t.Fatal(err)
	}
	rc2, _ := os.ReadFile(filepath.Join(home2, ".bashrc"))
	if !strings.Contains(string(rc2), "C-g") {
		t.Errorf("opt-in setup missing keybinding:\n%s", rc2)
	}
	// --no-keybinding skips the question even interactively.
	home3 := t.TempDir()
	if _, err := Run(Options{Home: home3, Sh: ShellBash, Write: true, NoKeybinding: true, Stdin: strings.NewReader("y\ny\n"), GenCompletion: fakeGen(t)}); err != nil {
		t.Fatal(err)
	}
	rc3, _ := os.ReadFile(filepath.Join(home3, ".bashrc"))
	if strings.Contains(string(rc3), "C-g") {
		t.Errorf("--no-keybinding setup enabled binding:\n%s", rc3)
	}
}

func TestRunBacksUpForeignCompletion(t *testing.T) {
	home := t.TempDir()
	p := PathsFor(ShellBash, home)
	if err := os.MkdirAll(filepath.Dir(p.CompletionFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.CompletionFile, []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{Home: home, Sh: ShellBash, Write: true, Yes: true, GenCompletion: fakeGen(t)}); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(p.CompletionFile + ".bak")
	if err != nil || string(bak) != "foreign" {
		t.Errorf("backup = %q, %v; want preserved foreign content", bak, err)
	}
	got, _ := os.ReadFile(p.CompletionFile)
	if !strings.Contains(string(got), "completion for lead") {
		t.Errorf("completion not replaced: %q", got)
	}
}

func TestRunCheckReportsCompleteness(t *testing.T) {
	home := t.TempDir()
	rep, err := Run(Options{Home: home, Sh: ShellBash, Check: true, GenCompletion: fakeGen(t)})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if rep.Complete {
		t.Error("check on empty HOME = complete, want incomplete")
	}
	if _, err := Run(Options{Home: home, Sh: ShellBash, Write: true, Yes: true, GenCompletion: fakeGen(t)}); err != nil {
		t.Fatal(err)
	}
	rep, err = Run(Options{Home: home, Sh: ShellBash, Check: true, GenCompletion: fakeGen(t)})
	if err != nil {
		t.Fatalf("check after setup: %v", err)
	}
	if !rep.Complete {
		t.Errorf("check after setup = incomplete (lines: %v)", rep.Lines)
	}
}

func TestRunUninstallRemovesManagedFiles(t *testing.T) {
	home := t.TempDir()
	if _, err := Run(Options{Home: home, Sh: ShellBash, Write: true, Yes: true, GenCompletion: fakeGen(t)}); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(Options{Home: home, Sh: ShellBash, Uninstall: true, GenCompletion: fakeGen(t)})
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !rep.Changed {
		t.Error("uninstall Changed = false, want removals reported")
	}
	rc, _ := os.ReadFile(filepath.Join(home, ".bashrc"))
	if HasBlock(string(rc)) {
		t.Errorf("block remains after uninstall:\n%s", rc)
	}
	p := PathsFor(ShellBash, home)
	if _, err := os.Stat(p.CompletionFile); !os.IsNotExist(err) {
		t.Error("completion remains after uninstall")
	}
	// Idempotent: second uninstall is a no-op.
	rep2, err := Run(Options{Home: home, Sh: ShellBash, Uninstall: true, GenCompletion: fakeGen(t)})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Changed {
		t.Error("re-uninstall Changed = true, want no-op")
	}
}

func TestRunUninstallRefusesForeignCompletion(t *testing.T) {
	home := t.TempDir()
	p := PathsFor(ShellBash, home)
	if err := os.MkdirAll(filepath.Dir(p.CompletionFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.CompletionFile, []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{Home: home, Sh: ShellBash, Uninstall: true, GenCompletion: fakeGen(t)}); err != nil {
		t.Fatalf("uninstall with foreign file: %v", err)
	}
	if _, err := os.Stat(p.CompletionFile); err != nil {
		t.Error("foreign completion was deleted; must be preserved")
	}
}
