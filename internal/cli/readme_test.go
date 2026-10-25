package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// repoRoot locates the module root from this test file; TestMain moves the
// process cwd into a temp dir, so the checkout must be found by source path.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

var readmeCommandRow = regexp.MustCompile("^\\|\\s*`lead\\s+([a-z-]+)")

// readmeSubcommands parses the README "## Subcommands" table into command names.
func readmeSubcommands(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	var cmds []string
	inSection := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "## ") {
			inSection = strings.TrimSpace(line) == "## Subcommands"
			continue
		}
		if !inSection {
			continue
		}
		if m := readmeCommandRow.FindStringSubmatch(line); m != nil {
			cmds = append(cmds, m[1])
		}
	}
	if len(cmds) == 0 {
		t.Fatal("README.md has no `lead <command>` rows under ## Subcommands")
	}
	return cmds
}

// helpSubcommands returns the commands cobra lists in `lead --help`, minus
// the built-in help command (README documents user-facing commands only).
func helpSubcommands(t *testing.T) []string {
	t.Helper()
	stdout, _, err := execute(t, "--help")
	if err != nil {
		t.Fatalf("lead --help: %v", err)
	}
	var cmds []string
	inCommands := false
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "Available Commands:") {
			inCommands = true
			continue
		}
		if inCommands {
			if strings.TrimSpace(line) == "" {
				break
			}
			if fields := strings.Fields(line); len(fields) > 0 && fields[0] != "help" {
				cmds = append(cmds, fields[0])
			}
		}
	}
	if len(cmds) == 0 {
		t.Fatalf("lead --help listed no commands, got:\n%s", stdout)
	}
	return cmds
}

func TestReadmeSubcommandsMatchHelp(t *testing.T) {
	want := helpSubcommands(t)
	got := readmeSubcommands(t)
	sort.Strings(want)
	sort.Strings(got)

	if strings.Join(want, ",") == strings.Join(got, ",") {
		return
	}

	inHelp := map[string]bool{}
	for _, c := range want {
		inHelp[c] = true
	}
	inReadme := map[string]bool{}
	for _, c := range got {
		inReadme[c] = true
	}
	var missing, extra []string
	for _, c := range want {
		if !inReadme[c] {
			missing = append(missing, c)
		}
	}
	for _, c := range got {
		if !inHelp[c] {
			extra = append(extra, c)
		}
	}
	t.Errorf("README ## Subcommands table drifted from `lead --help`\nmissing from README: %v\nextra in README: %v\nhelp:   %v\nREADME: %v",
		missing, extra, want, got)
}
