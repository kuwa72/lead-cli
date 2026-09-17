// Package openurl opens URLs in a browser with environment-aware opener
// selection (issue #217). gh delegates opener discovery to
// xdg-open/wslview, which are absent on minimal WSL installs; lead picks
// the opener itself so `lead work`/`lead inbox` browsing works there.
//
// Selection order (matching gh's own precedence):
//  1. explicit browser: GH_BROWSER > `gh config get browser` > BROWSER
//  2. WSL: powershell.exe -NoProfile -Command Start-Process '<url>'
//  3. darwin: open <url>
//  4. other linux/unix: xdg-open <url>
package openurl

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Deps injects the environment so tests never launch a real browser
// (mirrors internal/doctor's Deps pattern).
type Deps struct {
	// Getenv reads environment variables; defaults to os.Getenv.
	Getenv func(string) string
	// LookPath resolves a binary on PATH; defaults to exec.LookPath.
	LookPath func(string) (string, error)
	// ReadFile reads detection files such as /proc/version;
	// defaults to os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// GOOS overrides runtime.GOOS; "" means the real platform.
	GOOS string
	// Run executes the chosen opener; defaults to exec.CommandContext
	// with stdout/stderr attached to the process's.
	Run func(ctx context.Context, name string, args ...string) error
	// GhConfigGet returns `gh config get <key>`; "" or a non-nil error
	// means the key is unset. Nil skips the gh-config step.
	GhConfigGet func(ctx context.Context, key string) (string, error)
}

func (d Deps) getenv(k string) string {
	if d.Getenv != nil {
		return d.Getenv(k)
	}
	return os.Getenv(k)
}

func (d Deps) lookPath(name string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath(name)
	}
	return exec.LookPath(name)
}

func (d Deps) readFile(p string) ([]byte, error) {
	if d.ReadFile != nil {
		return d.ReadFile(p)
	}
	return os.ReadFile(p)
}

func (d Deps) goos() string {
	if d.GOOS != "" {
		return d.GOOS
	}
	return runtime.GOOS
}

func (d Deps) run(ctx context.Context, name string, args ...string) error {
	if d.Run != nil {
		return d.Run(ctx, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Open launches a browser for url using the best available opener.
func Open(ctx context.Context, url string, d Deps) error {
	if launcher := explicitBrowser(ctx, d); launcher != "" {
		return runLauncher(ctx, d, launcher, url)
	}

	type candidate struct {
		name string
		args []string
	}
	var candidates []candidate
	switch d.goos() {
	case "darwin":
		candidates = []candidate{{"open", []string{url}}}
	case "windows":
		candidates = []candidate{{"powershell.exe", powerShellArgs(url)}}
	default: // linux and friends
		if IsWSL(d) {
			candidates = []candidate{
				{"powershell.exe", powerShellArgs(url)},
				{"xdg-open", []string{url}},
			}
		} else {
			candidates = []candidate{{"xdg-open", []string{url}}}
		}
	}

	var tried []string
	for _, c := range candidates {
		if _, err := d.lookPath(c.name); err != nil {
			tried = append(tried, c.name)
			continue
		}
		return d.run(ctx, c.name, c.args...)
	}
	return fmt.Errorf("no browser opener found for %s (tried %s); "+
		"set a browser via GH_BROWSER, BROWSER, or `gh config set browser <command>`",
		url, strings.Join(tried, ", "))
}

// IsWSL reports whether the environment is Windows Subsystem for Linux:
// WSL_DISTRO_NAME/WSL_INTEROP set, or a "microsoft" (case-insensitive)
// kernel string in /proc/version or /proc/sys/kernel/osrelease (the
// uname -r equivalent).
func IsWSL(d Deps) bool {
	if d.getenv("WSL_DISTRO_NAME") != "" || d.getenv("WSL_INTEROP") != "" {
		return true
	}
	for _, p := range []string{"/proc/version", "/proc/sys/kernel/osrelease"} {
		if b, err := d.readFile(p); err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft") {
			return true
		}
	}
	return false
}

// explicitBrowser resolves a user-configured launcher in gh's order:
// GH_BROWSER > gh config browser > BROWSER. "" means none configured.
func explicitBrowser(ctx context.Context, d Deps) string {
	if v := d.getenv("GH_BROWSER"); v != "" {
		return v
	}
	if d.GhConfigGet != nil {
		if v, err := d.GhConfigGet(ctx, "browser"); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return d.getenv("BROWSER")
}

// runLauncher splits the configured command line (e.g. "firefox
// --new-window") and appends the URL as the final argv entry, like
// go-gh's browser package.
func runLauncher(ctx context.Context, d Deps, launcher, url string) error {
	args, err := splitCommand(launcher)
	if err != nil || len(args) == 0 {
		return fmt.Errorf("cannot parse browser command %q: %w", launcher, err)
	}
	if _, err := d.lookPath(args[0]); err != nil {
		return fmt.Errorf("browser %q (from GH_BROWSER/BROWSER/`gh config set browser`) "+
			"not found on PATH; cannot open %s", args[0], url)
	}
	return d.run(ctx, args[0], append(args[1:], url)...)
}

// powerShellArgs builds `-NoProfile -Command Start-Process '<url>'` so
// the Windows-side default browser opens the URL. The URL is single
// quoted (' → ”) so '&' or spaces cannot become command syntax.
func powerShellArgs(url string) []string {
	return []string{"-NoProfile", "-Command", "Start-Process '" + strings.ReplaceAll(url, "'", "''") + "'"}
}

// splitCommand is a minimal shlex: whitespace-separated tokens with
// single-quoted (literal) and double-quoted (backslash-escapes) spans.
func splitCommand(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inTok := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\n':
			if inTok {
				out = append(out, cur.String())
				cur.Reset()
				inTok = false
			}
		case '\'':
			inTok = true
			for i++; i < len(s) && s[i] != '\''; i++ {
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated single quote")
			}
		case '"':
			inTok = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated double quote")
			}
		default:
			inTok = true
			cur.WriteByte(c)
		}
	}
	if inTok {
		out = append(out, cur.String())
	}
	return out, nil
}
