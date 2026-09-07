package testutil

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

var errBoom = errors.New("boom")

func TestInstallDummy_LogsArgsAndRunsBody(t *testing.T) {
	logPath := InstallDummy(t, "probe", `printf 'out-%s' "$1"`)

	out, err := exec.Command("probe", "a b", "c").Output()
	if err != nil {
		t.Fatalf("dummy exec: %v", err)
	}
	if string(out) != "out-a b" {
		t.Errorf("dummy stdout = %q, want body output", out)
	}
	lines := LogLines(t, logPath)
	if len(lines) != 2 || lines[0] != "<a b>" || lines[1] != "<c>" {
		t.Errorf("arg log = %q, want [<a b> <c>]", lines)
	}
	if _, err := exec.LookPath("probe"); err != nil {
		t.Errorf("dummy not on PATH: %v", err)
	}
}

func TestInstallDummy_ExitStatusPropagates(t *testing.T) {
	InstallDummy(t, "boom", `exit 3`)

	err := exec.Command("boom").Run()
	if err == nil {
		t.Fatal("failing dummy = nil error, want exit status")
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("dummy error = %q, want exit status 3", err)
	}
}

func TestInstallDummy_AppendsAcrossCallsAndClearLog(t *testing.T) {
	logPath := InstallDummy(t, "multi", `:`)

	if err := exec.Command("multi", "one").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("multi", "two").Run(); err != nil {
		t.Fatal(err)
	}
	if got := len(LogLines(t, logPath)); got != 2 {
		t.Fatalf("log lines after 2 calls = %d, want 2", got)
	}
	ClearLog(t, logPath)
	if got := LogText(t, logPath); got != "" {
		t.Errorf("log after ClearLog = %q, want empty", got)
	}
}

func TestTempBin_PrependsPath(t *testing.T) {
	dir := TempBin(t)
	if err := os.WriteFile(filepath.Join(dir, "thing"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("thing"); err != nil {
		t.Errorf("TempBin not on PATH: %v", err)
	}
}

func TestEmptyBin_HidesRealBinaries(t *testing.T) {
	EmptyBin(t)
	if _, err := exec.LookPath("sh"); err == nil {
		t.Error("LookPath(sh) succeeded under EmptyBin, want PATH isolation")
	}
}
func TestFakeGhClient_ServesCannedIssuesAndRecordsCalls(t *testing.T) {
	f := &FakeGhClient{
		Summaries: []ports.IssueSummary{{Number: 1, Title: "one"}},
		Issues:    map[int]ports.Issue{1: {Number: 1, Title: "one", Body: "b", State: "OPEN"}},
	}
	ctx := context.Background()

	got, err := f.ListOpen(ctx)
	if err != nil || len(got) != 1 || got[0].Title != "one" {
		t.Fatalf("ListOpen = %+v, %v; want canned summary", got, err)
	}
	iss, err := f.View(ctx, 1)
	if err != nil || iss.Body != "b" || iss.State != "OPEN" {
		t.Fatalf("View = %+v, %v; want canned issue", iss, err)
	}
	if f.ListCalls != 1 || len(f.ViewCalls) != 1 || f.ViewCalls[0] != 1 {
		t.Errorf("calls not recorded: list=%d view=%v", f.ListCalls, f.ViewCalls)
	}

	f.ListErr = errBoom
	if _, err := f.ListOpen(ctx); err != errBoom {
		t.Errorf("ListOpen error = %v, want injected %v", err, errBoom)
	}
	if _, err := f.View(ctx, 999); err == nil {
		t.Error("View unknown issue = nil error, want failure")
	}
}

func TestFakeHerdrRunner_RecordsSplitAndSend(t *testing.T) {
	f := &FakeHerdrRunner{}
	ctx := context.Background()

	id, err := f.Split(ctx, ports.DirectionRight, 0.5)
	if err != nil || id != "fake-pane" {
		t.Fatalf("Split = %q, %v; want fake-pane, nil", id, err)
	}
	if err := f.SendText(ctx, id, `agy -i "hi"`); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if len(f.Splits) != 1 || f.Splits[0].Dir != ports.DirectionRight || f.Splits[0].Ratio != 0.5 {
		t.Errorf("splits = %+v, want one right/0.5 call", f.Splits)
	}
	if len(f.Sends) != 1 || f.Sends[0].Text != `agy -i "hi"` {
		t.Errorf("sends = %+v, want prepared command recorded", f.Sends)
	}

	f.SplitErr = errBoom
	if _, err := f.Split(ctx, ports.DirectionRight, 0.5); err != errBoom {
		t.Errorf("Split error = %v, want injected %v", err, errBoom)
	}
}

func TestFakeAgentLauncher_RecordsLaunches(t *testing.T) {
	f := &FakeAgentLauncher{}
	ctx := context.Background()

	if err := f.Launch(ctx, "agy", "prompt"); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if len(f.Launches) != 1 || f.Launches[0] != (LaunchCall{Agent: "agy", Prompt: "prompt"}) {
		t.Errorf("launches = %+v, want one agy call", f.Launches)
	}
	f.LaunchErr = errBoom
	if err := f.Launch(ctx, "agy", "prompt"); err != errBoom {
		t.Errorf("Launch error = %v, want injected %v", err, errBoom)
	}
}

func TestCheckGolden_MatchMissingAndMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.golden")

	if err := CheckGolden(path, []byte("hello\n")); err == nil {
		t.Error("CheckGolden missing file = nil, want failure suggesting UPDATE_GOLDEN=1")
	} else if !strings.Contains(err.Error(), "UPDATE_GOLDEN=1") {
		t.Errorf("missing-file error = %q, want UPDATE_GOLDEN=1 hint", err)
	}

	t.Setenv("UPDATE_GOLDEN", "1")
	if err := CheckGolden(path, []byte("hello\n")); err != nil {
		t.Fatalf("CheckGolden with UPDATE_GOLDEN=1: %v", err)
	}
	t.Setenv("UPDATE_GOLDEN", "")

	if err := CheckGolden(path, []byte("hello\n")); err != nil {
		t.Errorf("CheckGolden match: %v", err)
	}
	if err := CheckGolden(path, []byte("bye\n")); err == nil {
		t.Error("CheckGolden mismatch = nil, want failure")
	} else if !strings.Contains(err.Error(), "golden mismatch") {
		t.Errorf("mismatch error = %q, want 'golden mismatch'", err)
	}
}

func TestAssertGoldenString_UsesGoldenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "cmd.golden")

	t.Setenv("UPDATE_GOLDEN", "1")
	AssertGoldenString(t, path, "agy -i \"hi\"\n")
	t.Setenv("UPDATE_GOLDEN", "")

	AssertGoldenString(t, path, "agy -i \"hi\"\n")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("golden file not created (parents included): %v", err)
	}
}
