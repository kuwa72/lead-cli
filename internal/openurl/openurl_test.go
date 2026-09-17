package openurl_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/openurl"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

const testURL = "https://github.com/o/r/issues/36"

// runCall records one Deps.Run invocation.
type runCall struct {
	name string
	args []string
}

// envFunc returns a Getenv backed by a fixed map.
func envFunc(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// lookPathFunc resolves only the listed binaries.
func lookPathFunc(found ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, n := range found {
		set[n] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/fake/bin/" + name, nil
		}
		return "", fmt.Errorf("%s: not found", name)
	}
}

// fileFunc returns a ReadFile backed by a fixed map; missing paths error.
func fileFunc(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("no such file")
	}
}

func TestOpen_WSLUsesPowerShell(t *testing.T) {
	var calls []runCall
	d := openurl.Deps{
		Getenv:   envFunc(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}),
		LookPath: lookPathFunc("powershell.exe", "xdg-open"),
		GOOS:     "linux",
		Run: func(ctx context.Context, name string, args ...string) error {
			calls = append(calls, runCall{name, args})
			return nil
		},
	}
	if err := openurl.Open(context.Background(), testURL, d); err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []runCall{{"powershell.exe", []string{
		"-NoProfile", "-Command", "Start-Process '" + testURL + "'",
	}}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
}

func TestOpen_WSLDetectedViaOSRelease(t *testing.T) {
	var calls []runCall
	d := openurl.Deps{
		Getenv:   envFunc(nil),
		ReadFile: fileFunc(map[string]string{"/proc/sys/kernel/osrelease": "6.18.33.2-Microsoft-standard-WSL2"}),
		LookPath: lookPathFunc("powershell.exe"),
		GOOS:     "linux",
		Run: func(ctx context.Context, name string, args ...string) error {
			calls = append(calls, runCall{name, args})
			return nil
		},
	}
	if err := openurl.Open(context.Background(), testURL, d); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(calls) != 1 || calls[0].name != "powershell.exe" {
		t.Fatalf("calls = %+v, want powershell.exe", calls)
	}
}

func TestOpen_WSLDetectedViaProcVersion(t *testing.T) {
	var calls []runCall
	d := openurl.Deps{
		Getenv:   envFunc(nil),
		ReadFile: fileFunc(map[string]string{"/proc/version": "Linux version 5.15.90.1-microsoft-standard-WSL2"}),
		LookPath: lookPathFunc("powershell.exe"),
		GOOS:     "linux",
		Run: func(ctx context.Context, name string, args ...string) error {
			calls = append(calls, runCall{name, args})
			return nil
		},
	}
	if err := openurl.Open(context.Background(), testURL, d); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(calls) != 1 || calls[0].name != "powershell.exe" {
		t.Fatalf("calls = %+v, want powershell.exe", calls)
	}
}

func TestOpen_PowerShellEscapesSingleQuotes(t *testing.T) {
	var calls []runCall
	d := openurl.Deps{
		Getenv:   envFunc(map[string]string{"WSL_INTEROP": "/run/WSL/1_interop"}),
		LookPath: lookPathFunc("powershell.exe"),
		GOOS:     "linux",
		Run: func(ctx context.Context, name string, args ...string) error {
			calls = append(calls, runCall{name, args})
			return nil
		},
	}
	url := "https://example.com/?a=1&b=o'x y"
	if err := openurl.Open(context.Background(), url, d); err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := "Start-Process 'https://example.com/?a=1&b=o''x y'"
	if len(calls) != 1 || calls[0].args[2] != want {
		t.Fatalf("args = %+v, want -Command %q", calls, want)
	}
}

func TestOpen_ExplicitBrowserWins(t *testing.T) {
	var calls []runCall
	d := openurl.Deps{
		Getenv: envFunc(map[string]string{
			"WSL_DISTRO_NAME": "Ubuntu",
			"GH_BROWSER":      "mybrowser --new-window",
		}),
		LookPath: lookPathFunc("mybrowser", "powershell.exe"),
		GOOS:     "linux",
		Run: func(ctx context.Context, name string, args ...string) error {
			calls = append(calls, runCall{name, args})
			return nil
		},
	}
	if err := openurl.Open(context.Background(), testURL, d); err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []runCall{{"mybrowser", []string{"--new-window", testURL}}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v, want %+v (powershell.exe must not run)", calls, want)
	}
}

func TestOpen_BrowserPrecedenceMatchesGh(t *testing.T) {
	// gh's real order: GH_BROWSER > `gh config get browser` > BROWSER.
	cases := []struct {
		name     string
		env      map[string]string
		ghConfig string
		wantName string
	}{
		{"gh browser beats BROWSER", map[string]string{"BROWSER": "envbrowser"}, "cfgbrowser", "cfgbrowser"},
		{"GH_BROWSER beats gh config", map[string]string{"GH_BROWSER": "ghenv", "BROWSER": "envbrowser"}, "cfgbrowser", "ghenv"},
		{"BROWSER last", map[string]string{"BROWSER": "envbrowser"}, "", "envbrowser"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []runCall
			d := openurl.Deps{
				Getenv:   envFunc(tc.env),
				LookPath: lookPathFunc("ghenv", "cfgbrowser", "envbrowser", "powershell.exe"),
				GOOS:     "linux",
				GhConfigGet: func(ctx context.Context, key string) (string, error) {
					if key != "browser" {
						t.Errorf("GhConfigGet key = %q, want browser", key)
					}
					return tc.ghConfig, nil
				},
				Run: func(ctx context.Context, name string, args ...string) error {
					calls = append(calls, runCall{name, args})
					return nil
				},
			}
			if err := openurl.Open(context.Background(), testURL, d); err != nil {
				t.Fatalf("Open: %v", err)
			}
			if len(calls) != 1 || calls[0].name != tc.wantName {
				t.Fatalf("calls = %+v, want %s", calls, tc.wantName)
			}
		})
	}
}

func TestOpen_DarwinUsesOpen(t *testing.T) {
	var calls []runCall
	d := openurl.Deps{
		Getenv:   envFunc(nil),
		LookPath: lookPathFunc("open"),
		GOOS:     "darwin",
		Run: func(ctx context.Context, name string, args ...string) error {
			calls = append(calls, runCall{name, args})
			return nil
		},
	}
	if err := openurl.Open(context.Background(), testURL, d); err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []runCall{{"open", []string{testURL}}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
}

func TestOpen_LinuxFallsBackXdgOpen(t *testing.T) {
	var calls []runCall
	d := openurl.Deps{
		Getenv:   envFunc(nil),
		ReadFile: fileFunc(map[string]string{"/proc/version": "Linux version 6.8.0-generic"}),
		LookPath: lookPathFunc("xdg-open"),
		GOOS:     "linux",
		Run: func(ctx context.Context, name string, args ...string) error {
			calls = append(calls, runCall{name, args})
			return nil
		},
	}
	if err := openurl.Open(context.Background(), testURL, d); err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []runCall{{"xdg-open", []string{testURL}}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
}

func TestOpen_WSLFallsBackToXdgOpenWithoutPowerShell(t *testing.T) {
	var calls []runCall
	d := openurl.Deps{
		Getenv:   envFunc(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}),
		LookPath: lookPathFunc("xdg-open"),
		GOOS:     "linux",
		Run: func(ctx context.Context, name string, args ...string) error {
			calls = append(calls, runCall{name, args})
			return nil
		},
	}
	if err := openurl.Open(context.Background(), testURL, d); err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []runCall{{"xdg-open", []string{testURL}}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
}

func TestOpen_NoOpenerErrorsWithGuidance(t *testing.T) {
	d := openurl.Deps{
		Getenv:   envFunc(nil),
		ReadFile: fileFunc(map[string]string{"/proc/version": "Linux version 6.8.0-generic"}),
		LookPath: lookPathFunc(),
		GOOS:     "linux",
		Run: func(ctx context.Context, name string, args ...string) error {
			t.Fatalf("Run must not be called, got %s %v", name, args)
			return nil
		},
	}
	err := openurl.Open(context.Background(), testURL, d)
	if err == nil {
		t.Fatal("Open with no opener = nil error")
	}
	for _, want := range []string{testURL, "GH_BROWSER", "BROWSER", "gh config set browser"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// TestOpen_PowerShellViaPATHInjection runs a real dummy powershell.exe
// through exec and asserts the exact argv it received (AGENTS.md rule:
// PATH-injected dummy + arg log, not a stubbed runner).
func TestOpen_PowerShellViaPATHInjection(t *testing.T) {
	logPath := testutil.InstallDummy(t, "powershell.exe", ":")
	d := openurl.Deps{
		Getenv:   envFunc(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}),
		GOOS:     "linux",
		ReadFile: fileFunc(nil),
	}
	if err := openurl.Open(context.Background(), testURL, d); err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []string{"<-NoProfile>", "<-Command>", "<Start-Process '" + testURL + "'>"}
	if got := testutil.LogLines(t, logPath); !reflect.DeepEqual(got, want) {
		t.Errorf("powershell.exe argv = %q, want %q", got, want)
	}
}

// TestOpen_ExplicitBrowserViaPATHInjection runs a real dummy browser
// selected by GH_BROWSER and asserts it received the URL.
func TestOpen_ExplicitBrowserViaPATHInjection(t *testing.T) {
	logPath := testutil.InstallDummy(t, "custombrowser", ":")
	d := openurl.Deps{
		Getenv:   envFunc(map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "GH_BROWSER": "custombrowser"}),
		GOOS:     "linux",
		ReadFile: fileFunc(nil),
	}
	if err := openurl.Open(context.Background(), testURL, d); err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []string{"<" + testURL + ">"}
	if got := testutil.LogLines(t, logPath); !reflect.DeepEqual(got, want) {
		t.Errorf("custombrowser argv = %q, want %q", got, want)
	}
}
