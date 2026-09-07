// Package setup implements `lead setup`: shell completion placement,
// opt-in Ctrl-G keybinding, and marker-fenced rc blocks (issue #44).
// See docs/rfc-26-distribution.md §6.
//
// Principles: display-first (dry-run by default), approval before writes,
// idempotent re-runs, backups of foreign files, and safe no-ops on
// non-interactive stdin.
package setup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Shell is a supported login shell.
type Shell string

const (
	ShellBash Shell = "bash"
	ShellZsh  Shell = "zsh"
	ShellFish Shell = "fish"
)

// DetectShell resolves the target shell: explicit flag wins, else $SHELL's
// basename. Anything else is an error (powershell gets completions via
// `lead completion` but no rc management here).
func DetectShell(flag string) (Shell, error) {
	if flag != "" {
		return parseShell(flag)
	}
	base := filepath.Base(os.Getenv("SHELL"))
	if base == "" || base == "/" || base == "." {
		return "", fmt.Errorf("cannot detect shell (SHELL unset); pass --shell <bash|zsh|fish>")
	}
	return parseShell(base)
}

func parseShell(s string) (Shell, error) {
	switch Shell(strings.ToLower(s)) {
	case ShellBash, ShellZsh, ShellFish:
		return Shell(strings.ToLower(s)), nil
	default:
		return "", fmt.Errorf("unsupported shell %q: want bash, zsh, or fish", s)
	}
}

// Paths locates the managed completion file and rc file under home.
type Paths struct {
	CompletionFile string
	RCFile         string
}

// PathsFor returns managed paths for sh under home (RFC §6.4).
func PathsFor(sh Shell, home string) Paths {
	switch sh {
	case ShellZsh:
		return Paths{
			CompletionFile: filepath.Join(home, ".zsh/completions/_lead"),
			RCFile:         filepath.Join(home, ".zshrc"),
		}
	case ShellFish:
		return Paths{
			CompletionFile: filepath.Join(home, ".config/fish/completions/lead.fish"),
			RCFile:         filepath.Join(home, ".config/fish/config.fish"),
		}
	default:
		return Paths{
			CompletionFile: bashCompletionFile(home),
			RCFile:         filepath.Join(home, ".bashrc"),
		}
	}
}

// bashCompletionFile prefers the XDG completion dir and falls back to
// ~/.bash_completion.d when only that parent exists.
func bashCompletionFile(home string) string {
	primary := filepath.Join(home, ".local/share/bash-completion/completions/lead")
	fallbackDir := filepath.Join(home, ".bash_completion.d")
	if st, err := os.Stat(filepath.Dir(primary)); err == nil && st.IsDir() {
		return primary
	}
	if st, err := os.Stat(fallbackDir); err == nil && st.IsDir() {
		return filepath.Join(fallbackDir, "lead")
	}
	return primary
}

const (
	// BlockStart/BlockEnd fence the managed rc section (idempotency markers).
	BlockStart = "# lead >>> (managed by `lead setup`; do not edit)"
	BlockEnd   = "# <<< lead <<<"
)

// Snippet builds the rc block body: completion sourcing plus the optional
// Ctrl-G keybinding. Fish completions autoload, so fish only needs the
// binding (empty body when the binding is off).
func Snippet(sh Shell, completionFile string, keybinding bool) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	var sb strings.Builder
	switch sh {
	case ShellFish:
		if keybinding {
			sb.WriteString("if status is-interactive\n")
			sb.WriteString("    bind \\cg 'lead work; commandline -f repaint'\n")
			sb.WriteString("end\n")
		}
	default:
		if sh == ShellZsh {
			sb.WriteString(fmt.Sprintf("fpath=(%s $fpath)\n", q(filepath.Dir(completionFile))))
		} else {
			sb.WriteString(fmt.Sprintf("source %s\n", q(completionFile)))
		}
		if keybinding {
			if sh == ShellZsh {
				sb.WriteString("bindkey -s '^G' 'lead work\\n'\n")
			} else {
				sb.WriteString("if [[ $- == *i* ]]; then\n")
				sb.WriteString(`  bind -x '"\C-g": lead work'` + "\n")
				sb.WriteString("fi\n")
			}
		}
	}
	return sb.String()
}

// HasBlock reports whether content carries the managed block.
func HasBlock(content string) bool {
	return strings.Contains(content, BlockStart) && strings.Contains(content, BlockEnd)
}

// UpsertBlock replaces the managed block or appends it (newline-terminated).
func UpsertBlock(content, snippet string) string {
	block := BlockStart + "\n" + strings.TrimSuffix(snippet, "\n") + "\n" + BlockEnd + "\n"
	start := strings.Index(content, BlockStart)
	end := strings.Index(content, BlockEnd)
	if start >= 0 && end > start {
		end += len(BlockEnd)
		rest := content[end:]
		rest = strings.TrimLeft(rest, "\n")
		return content[:start] + block + rest
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + block
}

// RemoveBlock drops the managed block, preserving user content.
// ok=false means no block was present.
func RemoveBlock(content string) (string, bool) {
	start := strings.Index(content, BlockStart)
	end := strings.Index(content, BlockEnd)
	if start < 0 || end <= start {
		return content, false
	}
	end += len(BlockEnd)
	out := content[:start] + strings.TrimLeft(content[end:], "\n")
	return out, true
}

// managedCompletion reports whether path holds lead-generated content.
// Refusal to touch foreign files depends on this.
func managedCompletion(content string) bool {
	return strings.Contains(content, "lead")
}

// Options controls one setup run.
type Options struct {
	Home         string
	Sh           Shell
	Write        bool
	Check        bool
	Uninstall    bool
	NoKeybinding bool
	Yes          bool // skip approval prompt (assume yes)
	Stdin        io.Reader
	Stdout       io.Writer
	// GenCompletion renders the completion script for sh.
	GenCompletion func(Shell) (string, error)
}

// Report describes what happened (Lines are user-facing).
type Report struct {
	Lines    []string
	Changed  bool
	Complete bool // for --check: everything is in place
}

// Run executes setup per Options.
func Run(opts Options) (Report, error) {
	if opts.GenCompletion == nil {
		return Report{}, fmt.Errorf("setup: GenCompletion is required")
	}
	paths := PathsFor(opts.Sh, opts.Home)
	switch {
	case opts.Uninstall:
		return runUninstall(opts, paths)
	case opts.Check:
		return runCheck(opts, paths)
	default:
		return runInstall(opts, paths)
	}
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// prompter asks yes/no questions over a single scanner: one reader must
// not be wrapped in multiple bufio.Scanners (the first would buffer all).
type prompter struct {
	sc  *bufio.Scanner
	out io.Writer
}

func newPrompter(stdin io.Reader, out io.Writer) *prompter {
	if out == nil {
		out = os.Stdout
	}
	if stdin == nil {
		return &prompter{out: out}
	}
	return &prompter{sc: bufio.NewScanner(stdin), out: out}
}

// ask prompts a yes/no question. Non-interactive stdin (nil/EOF) is a no.
func (p *prompter) ask(question string) bool {
	if p.out != nil {
		fmt.Fprint(p.out, question)
	}
	if p.sc == nil {
		return false
	}
	if !p.sc.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(p.sc.Text()))
	return ans == "y" || ans == "yes"
}

func runCheck(opts Options, paths Paths) (Report, error) {
	var rep Report
	wantComp, err := opts.GenCompletion(opts.Sh)
	if err != nil {
		return rep, err
	}
	rep.Lines = append(rep.Lines, fmt.Sprintf("shell: %s", opts.Sh))
	complete := true
	if got := readFile(paths.CompletionFile); got == "" {
		rep.Lines = append(rep.Lines, fmt.Sprintf("completion: missing (%s)", paths.CompletionFile))
		complete = false
	} else if got != wantComp {
		rep.Lines = append(rep.Lines, fmt.Sprintf("completion: differs (%s)", paths.CompletionFile))
		complete = false
	} else {
		rep.Lines = append(rep.Lines, fmt.Sprintf("completion: ok (%s)", paths.CompletionFile))
	}
	rc := readFile(paths.RCFile)
	snip := Snippet(opts.Sh, paths.CompletionFile, false)
	if snip == "" {
		rep.Lines = append(rep.Lines, "rc: no block needed for this shell/mode")
	} else if !HasBlock(rc) {
		rep.Lines = append(rep.Lines, fmt.Sprintf("rc: block missing (%s)", paths.RCFile))
		complete = false
	} else {
		rep.Lines = append(rep.Lines, fmt.Sprintf("rc: ok (%s)", paths.RCFile))
	}
	rep.Complete = complete
	if !complete {
		rep.Lines = append(rep.Lines, "Next: run `lead setup --write`")
	}
	return rep, nil
}

func runUninstall(opts Options, paths Paths) (Report, error) {
	var rep Report
	if rc := readFile(paths.RCFile); HasBlock(rc) {
		trimmed, _ := RemoveBlock(rc)
		if err := writeFile(paths.RCFile, trimmed); err != nil {
			return rep, err
		}
		rep.Changed = true
		rep.Lines = append(rep.Lines, fmt.Sprintf("removed rc block (%s)", paths.RCFile))
	} else {
		rep.Lines = append(rep.Lines, "rc: no block (nothing to do)")
	}
	if got := readFile(paths.CompletionFile); got != "" {
		if !managedCompletion(got) {
			rep.Lines = append(rep.Lines, fmt.Sprintf("completion: foreign file kept (%s)", paths.CompletionFile))
		} else {
			if err := os.Remove(paths.CompletionFile); err != nil {
				return rep, fmt.Errorf("remove %s: %w", paths.CompletionFile, err)
			}
			rep.Changed = true
			rep.Lines = append(rep.Lines, fmt.Sprintf("removed completion (%s)", paths.CompletionFile))
		}
	} else {
		rep.Lines = append(rep.Lines, "completion: absent (nothing to do)")
	}
	return rep, nil
}

func runInstall(opts Options, paths Paths) (Report, error) {
	var rep Report
	wantComp, err := opts.GenCompletion(opts.Sh)
	if err != nil {
		return rep, err
	}

	// Keybinding is opt-in (RFC §6.1 principle 3): ask unless suppressed.
	ask := newPrompter(opts.Stdin, opts.Stdout)
	keybinding := false
	if !opts.NoKeybinding && ask.ask("Enable Ctrl-G keybinding for `lead work`? [y/N] ") {
		keybinding = true
	}
	snip := Snippet(opts.Sh, paths.CompletionFile, keybinding)

	compCur := readFile(paths.CompletionFile)
	compAction := "keep"
	switch {
	case compCur == "":
		compAction = "new"
	case compCur != wantComp:
		compAction = "replace (backup .bak)"
	}
	rcCur := readFile(paths.RCFile)
	rcAction := "keep"
	if snip == "" {
		rcAction = "none needed"
	} else if !HasBlock(rcCur) {
		rcAction = "append block"
		if rcCur == "" {
			rcAction = "create rc + block"
		}
	} else if UpsertBlock(rcCur, snip) != rcCur {
		rcAction = "update block"
	}

	rep.Lines = append(rep.Lines, fmt.Sprintf("shell: %s", opts.Sh))
	rep.Lines = append(rep.Lines, fmt.Sprintf("completion: %s %s", compAction, paths.CompletionFile))
	rep.Lines = append(rep.Lines, fmt.Sprintf("rc: %s %s", rcAction, paths.RCFile))
	rep.Lines = append(rep.Lines, fmt.Sprintf("keybinding: %s", map[bool]string{true: "on (Ctrl-G)", false: "off"}[keybinding]))

	if !opts.Write {
		rep.Lines = append(rep.Lines, "dry run: no changes (pass --write to apply)")
		return rep, nil
	}
	if !opts.Yes && !ask.ask("Proceed? [y/N] ") {
		rep.Lines = append(rep.Lines, "aborted: no changes")
		return rep, nil
	}

	switch compAction {
	case "new", "replace (backup .bak)":
		if compAction == "replace (backup .bak)" {
			if err := backupOnce(paths.CompletionFile); err != nil {
				return rep, err
			}
			rep.Lines = append(rep.Lines, fmt.Sprintf("backup: %s.bak", paths.CompletionFile))
		}
		if err := writeFile(paths.CompletionFile, wantComp); err != nil {
			return rep, err
		}
		rep.Changed = true
	}
	if snip != "" {
		if next := UpsertBlock(rcCur, snip); next != rcCur {
			if err := writeFile(paths.RCFile, next); err != nil {
				return rep, err
			}
			rep.Changed = true
		}
	}
	if rep.Changed {
		rep.Lines = append(rep.Lines, "done: restart the shell or source the rc file")
	} else {
		rep.Lines = append(rep.Lines, "already set up: no changes")
	}
	return rep, nil
}

// backupOnce copies path to path+".bak" unless a backup already exists.
func backupOnce(path string) error {
	bak := path + ".bak"
	if _, err := os.Stat(bak); err == nil {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(bak, b, 0o644)
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
