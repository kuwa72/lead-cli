// Package cli defines lead's command skeleton (issue #39).
//
// Framework decision: cobra (see docs/rfc-26-distribution.md §6.3, §10 item 1).
// Rationale: `lead completion <bash|zsh|fish|powershell>` generation comes for
// free, the help体系 is uniform, and future subcommands (e.g. `server`/`api`
// for #48) nest naturally. The cost is two small deps (cobra, pflag).
//
// `setup`/`doctor`/`update` are honest stubs here: their RunE returns
// a "not yet implemented" error pointing at the implementing issue (#44).
// `work` without an issue number needs the #37 TUI; with a number it
// performs branch/worktree/state handling (agent dispatch lands in #37).
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kuwa72/lead-cli/internal/adapters/agent"
	"github.com/kuwa72/lead-cli/internal/adapters/fzf"
	"github.com/kuwa72/lead-cli/internal/adapters/ghcli"
	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/adapters/herdr"
	"github.com/kuwa72/lead-cli/internal/dispatch"
	"github.com/kuwa72/lead-cli/internal/doctor"
	"github.com/kuwa72/lead-cli/internal/finish"
	"github.com/kuwa72/lead-cli/internal/inbox"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/projinit"
	"github.com/kuwa72/lead-cli/internal/server"
	"github.com/kuwa72/lead-cli/internal/setup"
	"github.com/kuwa72/lead-cli/internal/spec"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/tui"
	"github.com/kuwa72/lead-cli/internal/update"
	"github.com/kuwa72/lead-cli/internal/workflow"
)

// VersionInfo carries ldflags-injected build metadata from cmd/lead.
type VersionInfo struct {
	Version string
	Commit  string
	Date    string
}

func (v VersionInfo) String() string {
	return fmt.Sprintf("lead version %s (commit: %s, built: %s)", v.Version, v.Commit, v.Date)
}

func notImplemented(issue int) error {
	return fmt.Errorf("not yet implemented (see #%d)", issue)
}

// GitRunner abstracts the git operations `work`/`clean` need.
// *git.Runner implements it; tests substitute fakes or temp repos.
// (Alias of workflow.GitRunner so CLI and server share one contract.)
type GitRunner = workflow.GitRunner

// Deps injects external dependencies. Zero Deps means production defaults
// (real gh CLI, real git, resolved state file, process working directory,
// user home, stdin, and the running binary path).
type Deps struct {
	Gh         ports.GhClient
	Git        GitRunner
	StateFile  string
	WorkDir    string
	Home       string
	Stdin      io.Reader
	ExePath    string
	LookPath   func(string) (string, error)
	BrewPrefix func() (string, error)
	Herdr      ports.HerdrRunner
	// Selector picks issues when `run` has no number.
	// Nil means the embedded go-fzf picker (or LEAD_TEST_SELECTION below).
	Selector tui.Selector
	// Launcher starts headless agents for `dispatch`. Nil means real processes.
	Launcher dispatch.Launcher
	// SpecAgent runs the headless spec agent for `say`. Nil means real processes.
	SpecAgent spec.AgentRunner
}

func (d Deps) specAgent() spec.AgentRunner {
	if d.SpecAgent != nil {
		return d.SpecAgent
	}
	return &spec.ExecRunner{LookPath: d.LookPath}
}

func (d Deps) launcher() dispatch.Launcher {
	if d.Launcher != nil {
		return d.Launcher
	}
	return &dispatch.ExecLauncher{LookPath: d.LookPath}
}

func (d Deps) gh() ports.GhClient {
	if d.Gh != nil {
		return d.Gh
	}
	return ghcli.New()
}

func (d Deps) gitRunner() GitRunner {
	if d.Git != nil {
		return d.Git
	}
	return git.New()
}

func (d Deps) stateFile() string {
	if d.StateFile != "" {
		return d.StateFile
	}
	return state.ResolvePath()
}

func (d Deps) workDir() (string, error) {
	if d.WorkDir != "" {
		return d.WorkDir, nil
	}
	return os.Getwd()
}

// repoSlug is "owner/repo" of the working directory's origin, or "" when
// not inside a git repository with a GitHub remote (doctor #67 checks).
func (d Deps) repoSlug() string {
	cwd, err := d.workDir()
	if err != nil {
		return ""
	}
	root, err := d.gitRunner().RepoRoot(cwd)
	if err != nil {
		return ""
	}
	return git.RepoSlug(d.gitRunner().OriginURL(root))
}

func (d Deps) home() string {
	if d.Home != "" {
		return d.Home
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func (d Deps) stdin() io.Reader {
	if d.Stdin != nil {
		return d.Stdin
	}
	return os.Stdin
}

func (d Deps) exePath() string {
	if d.ExePath != "" {
		return d.ExePath
	}
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return exe
}

func (d Deps) lookPath() func(string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath
	}
	return exec.LookPath
}

func (d Deps) herdrRunner() ports.HerdrRunner {
	if d.Herdr != nil {
		return d.Herdr
	}
	return herdr.NewRunner()
}

// selector resolves the issue picker: explicit Deps first, then the
// LEAD_TEST_SELECTION hook (<number>[:<agent|browser>] for headless tests),
// then the interactive embedded picker.
func (d Deps) selector() tui.Selector {
	if d.Selector != nil {
		return d.Selector
	}
	if raw := os.Getenv("LEAD_TEST_SELECTION"); raw != "" {
		if sel, err := parseTestSelection(raw); err == nil {
			return &tui.FakeSelector{Selection: sel}
		}
	}
	return fzf.New()
}

// parseTestSelection parses the LEAD_TEST_SELECTION hook:
// "36" (default agent), "36:devin", or "36:browser".
func parseTestSelection(raw string) (tui.Selection, error) {
	parts := strings.SplitN(raw, ":", 2)
	number, err := strconv.Atoi(parts[0])
	if err != nil || number <= 0 {
		return tui.Selection{}, fmt.Errorf("invalid LEAD_TEST_SELECTION %q: want <number>[:<agent|browser>]", raw)
	}
	if len(parts) == 1 {
		return tui.Selection{IssueNumber: number, Agent: agent.DefaultAgent, Action: tui.ActionWork}, nil
	}
	if parts[1] == "" {
		return tui.Selection{}, fmt.Errorf("invalid LEAD_TEST_SELECTION %q: empty action", raw)
	}
	if parts[1] == "browser" {
		return tui.Selection{IssueNumber: number, Action: tui.ActionBrowse}, nil
	}
	return tui.Selection{IssueNumber: number, Agent: parts[1], Action: tui.ActionWork}, nil
}

// NewRootCmd builds the lead command tree with production defaults.
// Callers set Out/Err/Args in tests.
func NewRootCmd(version, commit, date string) *cobra.Command {
	return NewRootCmdWithDeps(version, commit, date, Deps{})
}

// NewRootCmdWithDeps builds the command tree with injected dependencies.
func NewRootCmdWithDeps(version, commit, date string, deps Deps) *cobra.Command {
	info := VersionInfo{Version: version, Commit: commit, Date: date}

	root := &cobra.Command{
		Use:   "lead",
		Short: "GitHub Issue–triggered multi-agent orchestrator",
		Long: `lead drives Issues to completion: select an issue, branch, launch a
coding agent (Herdr side-pane or inline), then wait CI and merge.

Without a subcommand lead opens the inbox (docs/rfc-inbox-ux.md §5): the
needs-review / blocked issues plus locally derived merged/running sections,
with single-key approve (a), edit-and-approve (e), reject (x), comment (t).
It needs an interactive terminal; use --help for the subcommand list.`,
		SilenceUsage: true, // runtime/stub errors print the error, not full usage
		// Historical #35 behavior: bare `lead --version`/`-v` prints the same
		// stamped string as `lead version` (cobra's built-in --version flag
		// cannot take a shorthand or custom template output, so handle it here).
		RunE: func(cmd *cobra.Command, args []string) error {
			if v, _ := cmd.Flags().GetBool("version"); v {
				fmt.Fprintln(cmd.OutOrStdout(), info.String())
				return nil
			}
			return runInbox(cmd, deps)
		},
	}
	root.PersistentFlags().BoolP("version", "v", false, "print version information and exit")

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), info.String())
			return nil
		},
	}

	// `run` is the manual override of the inbox flow (docs/rfc-inbox-ux.md §6);
	// `work` stays as a hidden alias for scripts written before the rename.
	newRunCmd := func(use string, hidden bool) *cobra.Command {
		c := &cobra.Command{
			Use:    use + " [issue-number]",
			Short:  "Manually start one issue (branch + worktree + interactive agent)",
			Hidden: hidden,
			Long: `Manual override of the dispatch flow. With an issue number: fetch the
issue, create/check out the working branch, optionally create a worktree,
record the workflow state, and prepare the agent in a Herdr pane (or print
the command inline). Without a number, the embedded picker selects the
issue. Normal operation is ` + "`lead dispatch`" + `, which needs no human step.`,
			Args: cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if len(args) == 0 {
					return runWorkTUI(cmd, deps)
				}
				return runWork(cmd, deps, args[0])
			},
		}
		// Flags per docs/rfc-25-workflow-flexibility.md §6.1.
		c.Flags().String("mode", state.ModeImplement, "execution mode (implement|split|research|docs)")
		c.Flags().String("branch", "", "use existing branch instead of creating one")
		c.Flags().String("pr", "", "attach to existing PR instead of creating one")
		c.Flags().String("worktree", "", "create isolated worktree: bare flag = auto path, or --worktree=<path>")
		c.Flags().String("part", "", "work unit within a multi-PR issue")
		c.Flags().Bool("draft", false, "create PR as draft")
		c.Flags().String("agent", "", "coding agent (default: agy)")
		// Bare `--worktree` means "auto path under <repo>/.worktrees".
		c.Flags().Lookup("worktree").NoOptDefVal = "auto"
		return c
	}
	runCmd := newRunCmd("run", false)
	workCmd := newRunCmd("work", true)

	dispatchCmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Hand ready issues to headless agents (worktree per issue)",
		Long: `Foreground dispatcher (docs/rfc-inbox-ux.md §7). Each pass lists open
issues labelled "ready", skips locally blocked ones, and for up to
--parallel of them creates a worktree and runs the agent non-interactively
with the issue and completion definition as its prompt. When the agent
returns and the issue is closed on GitHub, the worktree and state record
are removed. Otherwise the attempt is counted; after 3 failures the issue
is relabelled "blocked" with the cause posted as a comment.
Without --once the pass repeats every --interval until Ctrl-C; running
agents are not killed on exit.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDispatch(cmd, deps)
		},
	}
	dispatchCmd.Flags().Int("parallel", dispatch.DefaultParallel, "max agents running at once")
	dispatchCmd.Flags().Bool("once", false, "run a single pass and exit")
	dispatchCmd.Flags().Duration("interval", 30*time.Second, "pause between passes")
	dispatchCmd.Flags().String("agent", "", "headless implementation agent (default: agy)")
	dispatchCmd.Flags().Bool("skip-protection-check", false, "start even if the default branch lacks protection / required checks (warns)")

	sayCmd := &cobra.Command{
		Use:   "say <one-liner>",
		Short: "Turn a one-liner into needs-review issues via the spec agent",
		Long: `Spec AI (docs/rfc-inbox-ux.md §8). Runs the spec agent headlessly with
the one-liner, this repository's AGENTS.md and (with --follow-up) the
original issue, then files the drafts it returns with
` + "`gh issue create --label needs-review`" + ` (titles are linted, siblings
cross-reference each other). --redraft <n> rewrites issue n's body instead
and posts a comment describing the change. --dry-run prints the would-be
issues and touches nothing on GitHub. The agent's output is logged under
the state directory (logs/say-*.log). Agent: --agent, else $LEAD_SPEC_AGENT,
else agy.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSay(cmd, deps, args[0])
		},
	}
	sayCmd.Flags().String("agent", "", "headless spec agent (default: $LEAD_SPEC_AGENT, then agy)")
	sayCmd.Flags().Bool("dry-run", false, "print the would-be issues; no gh mutation")
	sayCmd.Flags().Int("follow-up", 0, "file follow-up issue(s) for issue <n> (実機 NG); body references #<n>")
	sayCmd.Flags().Int("redraft", 0, "rewrite issue <n>'s title/body from the one-liner and comment the change")

	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "Interactive environment setup (completions, keybinding)",
		Long: `Detect the shell, place the completion script, and manage the
marker-fenced rc block. Dry-run by default; --write applies after approval.
Finishes with a doctor summary.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(cmd, deps, info)
		},
	}
	setupCmd.Flags().Bool("write", false, "write changes after approval")
	setupCmd.Flags().String("shell", "", "target shell (bash|zsh|fish; default: detect $SHELL)")
	setupCmd.Flags().Bool("no-keybinding", false, "never ask for the Ctrl-G keybinding")
	setupCmd.Flags().Bool("check", false, "verify placement only (no changes)")
	setupCmd.Flags().Bool("uninstall", false, "remove the managed block and completion files")
	setupCmd.Flags().Bool("yes", false, "assume yes to approval prompts")

	completionCmd := &cobra.Command{
		Use:   "completion <bash|zsh|fish|powershell>",
		Short: "Generate shell completion script to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return cmd.Root().GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return cmd.Root().GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletion(cmd.OutOrStdout())
			default:
				return fmt.Errorf("unsupported shell %q: want bash, zsh, fish, or powershell", args[0])
			}
		},
	}

	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose environment (gh auth, herdr, agents, shell integration)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd, deps, info)
		},
	}
	doctorCmd.Flags().Bool("offline", false, "skip network probes")
	doctorCmd.Flags().Bool("json", false, "machine-readable output")
	doctorCmd.Flags().String("shell", "", "target shell for integration checks (default: detect $SHELL)")

	updateCmd := &cobra.Command{
		Use:   "update",
		Short: "Update lead to the latest release (direct installs only)",
		Long: `Checks releases/latest and replaces this binary (sha256-verified,
atomic swap). Brew-managed installs print ` + "`brew upgrade` guidance instead." + `
--check exits 0 when up-to-date, 1 when an update is available.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd, deps, info)
		},
	}
	updateCmd.Flags().Bool("check", false, "report availability only (no download)")
	updateCmd.Flags().Bool("yes", false, "skip the replacement approval prompt")
	updateCmd.Flags().String("version", "", "install a specific tag (default: latest)")

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize this project for the lead workflow (lead-flow skill)",
		Long: `Install the /lead-flow skill and update AGENTS.md so a coding agent
in this project can run the lead issue-driven TDD workflow.
Dry-run by default; --write applies after approval.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectInit(cmd, deps)
		},
	}
	initCmd.Flags().Bool("write", false, "write changes after approval")
	initCmd.Flags().Bool("check", false, "verify installation only (no changes)")
	initCmd.Flags().Bool("uninstall", false, "remove the managed skill files and AGENTS.md block")
	initCmd.Flags().Bool("yes", false, "assume yes to approval prompts")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show recorded workflows (Issue/Branch/Worktree/state)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, deps)
		},
	}
	statusCmd.Flags().Bool("json", false, "machine-readable output")

	cleanCmd := &cobra.Command{
		Use:   "clean <issue-number>",
		Short: "Safely remove a workflow's worktree and state record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClean(cmd, deps, args[0])
		},
	}
	cleanCmd.Flags().String("part", "", "work unit within a multi-PR issue")

	finishCmd := &cobra.Command{
		Use:   "finish <issue-number>",
		Short: "Wait CI, squash-merge the PR, and close the issue",
		Long: `Re-verify the recorded PR, wait for CI, then merge (squash) and close
under the merge policy (RFC §7). Pauses — confirm policy, never policy,
auto-merge-disabled repos — exit 0 with the next step, not an error.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFinish(cmd, deps, args[0])
		},
	}
	finishCmd.Flags().Int("pr", 0, "use this PR number instead of the recorded one")
	finishCmd.Flags().String("part", "", "work unit within a multi-PR issue")
	finishCmd.Flags().Bool("merge", false, "merge after CI passes (overrides confirm pause)")
	finishCmd.Flags().Bool("close", false, "close the issue")
	finishCmd.Flags().Bool("no-close", false, "never close the issue")
	finishCmd.Flags().String("comment", "", "post a completion comment")
	finishCmd.Flags().String("outcome", "", "record a non-PR outcome (research/docs without PR)")
	finishCmd.Flags().String("merge-policy", "", "policy override (auto|confirm|never)")
	finishCmd.Flags().Duration("timeout", 0, "CI wait cap (default 30m)")
	finishCmd.Flags().Duration("poll-interval", 0, "CI poll interval (default 10s)")

	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Run the headless socket API daemon (see design.md §7)",
		Long: `Hold workflows state behind a unix socket (dir 0700, socket 0600).
Single-run CLI mode keeps working without any server. Stops on SIGINT/SIGTERM.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(cmd, deps, info)
		},
	}
	serverCmd.Flags().String("socket", "", "socket path (default: runtime dir or state dir)")
	serverCmd.Flags().String("session", "", "named session (socket lead-<name>.sock)")

	apiCmd := &cobra.Command{
		Use:   "api",
		Short: "Query the socket API (schema/snapshot/call)",
	}
	apiCmd.PersistentFlags().String("socket", "", "socket path")
	apiCmd.PersistentFlags().String("session", "", "named session")

	schemaCmd := &cobra.Command{
		Use:   "schema",
		Short: "Print the socket API definition",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprint(cmd.OutOrStdout(), server.Schema())
			return nil
		},
	}
	snapshotCmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Print server state (workflows, staged prompts, notifications)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAPICall(cmd, deps, "snapshot", "")
		},
	}
	callCmd := &cobra.Command{
		Use:   "call <op>",
		Short: "Call a socket API op with JSON args",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawArgs, _ := cmd.Flags().GetString("args")
			return runAPICall(cmd, deps, args[0], rawArgs)
		},
	}
	callCmd.Flags().String("args", "", "JSON object args (default {})")
	apiCmd.AddCommand(schemaCmd, snapshotCmd, callCmd)

	root.AddCommand(versionCmd, dispatchCmd, runCmd, workCmd, statusCmd, cleanCmd, finishCmd, serverCmd, apiCmd, setupCmd, completionCmd, doctorCmd, updateCmd, initCmd)
	root.AddCommand(sayCmd)
	return root
}

// runInbox implements bare `lead` (issue #91): the inbox TUI. Without a TTY it
// exits non-zero with a hint (#81 requirement), unless LEAD_TEST_INBOX_KEYS
// replays keys headlessly for shell tests (same idea as LEAD_TEST_SELECTION).
func runInbox(cmd *cobra.Command, deps Deps) error {
	cwd, err := deps.workDir()
	if err != nil {
		return fmt.Errorf("inbox: working directory: %w", err)
	}
	root := cwd
	if r, err := deps.gitRunner().RepoRoot(cwd); err == nil && r != "" {
		root = r
	}
	opts := inbox.Options{
		Gh:         deps.gh(),
		Store:      &state.Store{Path: deps.stateFile()},
		Editor:     os.Getenv("EDITOR"),
		AgentsPath: filepath.Join(root, "AGENTS.md"),
		Repo:       inbox.RepoSlug(deps.gitRunner().OriginURL(root)),
	}
	if deps.Herdr != nil {
		opts.Herdr = deps.Herdr
	} else if herdr.Available() {
		opts.Herdr = herdr.New()
	}
	opts.Say = func(ctx context.Context, oneLiner string, followUp int) (string, error) {
		r := &spec.Runner{
			Gh:    deps.gh(),
			Agent: deps.specAgent(),
			Out:   cmd.OutOrStdout(),
			Opts: spec.Options{
				Agent:      os.Getenv("LEAD_SPEC_AGENT"),
				LogDir:     filepath.Join(filepath.Dir(deps.stateFile()), "logs"),
				WorkDir:    root,
				Repository: deps.gitRunner().OriginURL(root),
				FollowUp:   followUp,
			},
		}
		res, err := r.Say(ctx, oneLiner)
		if err != nil {
			return "", err
		}
		var nums []string
		for _, c := range res.Created {
			nums = append(nums, fmt.Sprintf("#%d", c.Number))
		}
		return fmt.Sprintf("%s を起票（needs-review）", strings.Join(nums, ", ")), nil
	}
	if keys := os.Getenv("LEAD_TEST_INBOX_KEYS"); keys != "" {
		opts.Shell = inbox.SyncShell{}
		return inbox.RunHeadless(inbox.New(opts), strings.Split(keys, ","), cmd.OutOrStdout())
	}
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return errors.New("lead: the inbox needs an interactive terminal (stdin/stdout are not a TTY); run `lead --help` for subcommands")
	}
	opts.Refresh = 30 * time.Second
	return inbox.Run(inbox.New(opts))
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// runSay implements `lead say` (RFC inbox §8, issue #94).
func runSay(cmd *cobra.Command, deps Deps, oneLiner string) error {
	flags := cmd.Flags()
	agentName, _ := flags.GetString("agent")
	if agentName == "" {
		agentName = os.Getenv("LEAD_SPEC_AGENT")
	}
	dryRun, _ := flags.GetBool("dry-run")
	followUp, _ := flags.GetInt("follow-up")
	redraft, _ := flags.GetInt("redraft")

	cwd, err := deps.workDir()
	if err != nil {
		return fmt.Errorf("say: working directory: %w", err)
	}
	repo := deps.gitRunner().OriginURL(cwd)
	r := &spec.Runner{
		Gh:    deps.gh(),
		Agent: deps.specAgent(),
		Out:   cmd.OutOrStdout(),
		Opts: spec.Options{
			Agent:      agentName,
			LogDir:     filepath.Join(filepath.Dir(deps.stateFile()), "logs"),
			WorkDir:    cwd,
			Repository: repo,
			DryRun:     dryRun,
			FollowUp:   followUp,
			Redraft:    redraft,
		},
	}
	_, err = r.Say(cmd.Context(), oneLiner)
	return err
}

// runWorkTUI implements `lead work` without a number: embedded picker
// (issue list + cached preview, then agent/browser action).
func runWorkTUI(cmd *cobra.Command, deps Deps) error {
	ctx := cmd.Context()
	summaries, err := deps.gh().ListOpen(ctx)
	if err != nil {
		return fmt.Errorf("work: %w", err)
	}
	if len(summaries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no open issues")
		return nil
	}
	items := make([]tui.IssueItem, len(summaries))
	for i, s := range summaries {
		items[i] = tui.IssueItem{Number: s.Number, Title: s.Title}
	}
	cache := &tui.Cache{Dir: tui.DefaultDir()}
	sel, err := deps.selector().SelectIssue(ctx, items, tui.Previewer(deps.gh(), cache))
	if err != nil {
		return fmt.Errorf("work: %w", err)
	}
	if sel.Action == tui.ActionBrowse {
		if err := deps.gh().BrowseIssue(ctx, sel.IssueNumber); err != nil {
			return fmt.Errorf("work: browse #%d: %w", sel.IssueNumber, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "opened #%d in browser\n", sel.IssueNumber)
		return nil
	}
	if flag := cmd.Flags().Lookup("agent"); flag != nil && !flag.Changed && sel.Agent != "" {
		_ = flag.Value.Set(sel.Agent)
	}
	// The list already gave us the title; skip a second fetch.
	title := ""
	for _, s := range summaries {
		if s.Number == sel.IssueNumber {
			title = s.Title
			break
		}
	}
	return runWorkIssue(cmd, deps, ports.Issue{Number: sel.IssueNumber, Title: title})
}

// runDispatch implements `lead dispatch` (RFC inbox §7 Part A).
func runDispatch(cmd *cobra.Command, deps Deps) error {
	flags := cmd.Flags()
	parallel, _ := flags.GetInt("parallel")
	once, _ := flags.GetBool("once")
	interval, _ := flags.GetDuration("interval")
	agentName, _ := flags.GetString("agent")
	skipProtection, _ := flags.GetBool("skip-protection-check")

	cwd, err := deps.workDir()
	if err != nil {
		return fmt.Errorf("dispatch: working directory: %w", err)
	}
	stateFile := deps.stateFile()
	d := &dispatch.Dispatcher{
		Gh:       deps.gh(),
		Git:      deps.gitRunner(),
		Store:    &state.Store{Path: stateFile},
		Launcher: deps.launcher(),
		Out:      cmd.OutOrStdout(),
		Opts: dispatch.Options{
			Parallel: parallel,
			Agent:    agentName,
			LogDir:   filepath.Join(filepath.Dir(stateFile), "logs"),
			WorkDir:  cwd,

			SkipProtectionCheck: skipProtection,
		},
	}
	if err := d.Preflight(cmd.Context()); err != nil {
		return err
	}
	if once {
		_, err := d.Once(cmd.Context())
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return d.Loop(ctx, interval)
}

// runWork implements `lead run <number>` (and the hidden `work` alias):
// fetch the issue, then branch + optional worktree + state record.
// Re-running is idempotent.
func runWork(cmd *cobra.Command, deps Deps, raw string) error {
	number, err := strconv.Atoi(raw)
	if err != nil || number <= 0 {
		return fmt.Errorf("invalid issue number %q", raw)
	}
	iss, err := deps.gh().View(cmd.Context(), number)
	if err != nil {
		return fmt.Errorf("work #%d: %w", number, err)
	}
	return runWorkIssue(cmd, deps, iss)
}

// runWorkIssue implements the branch/worktree/state half of work given a
// resolved issue (title sources: gh View, or the TUI list which already
// fetched it — never fetch twice).
// runWorkIssue implements the branch/worktree/state half of work given a
// resolved issue (title sources: gh View, or the TUI list which already
// fetched it — never fetch twice). The core lives in internal/workflow so
// the #48 socket server produces identical records.
func runWorkIssue(cmd *cobra.Command, deps Deps, iss ports.Issue) error {
	flags := cmd.Flags()
	mode, _ := flags.GetString("mode")
	branchFlag, _ := flags.GetString("branch")
	part, _ := flags.GetString("part")
	worktreeOpt := ""
	if wtFlag := flags.Lookup("worktree"); wtFlag != nil && wtFlag.Changed {
		worktreeOpt = wtFlag.Value.String()
		if worktreeOpt == "" {
			worktreeOpt = "auto"
		}
	}
	cwd, err := deps.workDir()
	if err != nil {
		return fmt.Errorf("work #%d: working directory: %w", iss.Number, err)
	}
	store := &state.Store{Path: deps.stateFile()}
	res, err := workflow.Start(cmd.Context(), deps.gitRunner(), store, workflow.StartOptions{
		Issue: iss, Mode: mode, Branch: branchFlag, Part: part,
		Worktree: worktreeOpt, WorkDir: cwd,
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Issue #%d  %s  %s\n", iss.Number, res.Mode, res.Status)
	fmt.Fprintf(out, "Branch: %s", res.Branch)
	if res.Worktree != "" {
		fmt.Fprintf(out, "  Worktree: %s", res.Worktree)
	}
	fmt.Fprintln(out)

	agentName, _ := flags.GetString("agent")
	if res.Pane != "" {
		fmt.Fprintf(out, "Agent already prepared in pane %s\n", res.Pane)
	} else {
		pane, err := launchAgent(cmd.Context(), out, deps, res, agentName, iss)
		if err != nil {
			return err
		}
		if w, ok, err := store.Get(iss.Number, part); err == nil && ok {
			w.Pane = pane
			_ = store.Upsert(w)
		}
	}

	fmt.Fprintln(out, "Next: review the prompt, launch the agent, then `lead status`.")
	return nil
}

func buildPrompt(iss ports.Issue) string {
	prompt := fmt.Sprintf("Issue #%d: %s", iss.Number, iss.Title)
	if body := strings.TrimSpace(iss.Body); body != "" {
		prompt += "\n\n" + body
	}
	return prompt
}

func launchAgent(ctx context.Context, out io.Writer, deps Deps, res workflow.StartResult, agentName string, iss ports.Issue) (string, error) {
	launchDir := res.RepoRoot
	if res.Worktree != "" {
		launchDir = res.Worktree
	}
	command := agent.CommandString(agentName, buildPrompt(iss))
	prepared := "cd " + strconv.Quote(launchDir) + " && " + command

	h := deps.herdrRunner()
	if ir, ok := h.(*herdr.InlineRunner); ok && ir.Out == nil {
		ir.Out = out
	}
	pane, err := h.Split(ctx, ports.DirectionRight, 0.5)
	if err != nil {
		return "", fmt.Errorf("work #%d: split pane: %w", iss.Number, err)
	}
	if err := h.SendText(ctx, pane, prepared); err != nil {
		return "", fmt.Errorf("work #%d: send agent command: %w", iss.Number, err)
	}
	return pane, nil
}

// runStatus implements `lead status`: local workflow records (RFC §5.3).
// Live GitHub/Git enrichment lands with #41; corrupt files surface an error.
func runStatus(cmd *cobra.Command, deps Deps) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	all, err := (&state.Store{Path: deps.stateFile()}).List()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if asJSON {
		raw, err := json.MarshalIndent(struct {
			Workflows []state.Workflow `json:"workflows"`
		}{Workflows: all}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(raw))
		return nil
	}
	if len(all) == 0 {
		fmt.Fprintln(out, "no workflows recorded")
		return nil
	}
	for _, w := range all {
		fmt.Fprintf(out, "Issue #%d  %s  %s\n", w.Issue, w.Mode, w.Status)
		fmt.Fprintf(out, "Branch: %s", w.Branch)
		if w.Worktree != "" {
			fmt.Fprintf(out, "  Worktree: %s", w.Worktree)
		}
		fmt.Fprintln(out)
	}
	return nil
}

// genCompletion renders the cobra completion script for a setup shell name.
func genCompletion(root *cobra.Command, shell string) (string, error) {
	var sb strings.Builder
	var err error
	switch shell {
	case "bash":
		err = root.GenBashCompletion(&sb)
	case "zsh":
		err = root.GenZshCompletion(&sb)
	case "fish":
		err = root.GenFishCompletion(&sb, true)
	default:
		return "", fmt.Errorf("unsupported shell %q", shell)
	}
	return sb.String(), err
}

// runSetup implements `lead setup` (RFC §6.2): dry-run preview by default,
// approved writes with --write, verification with --check.
func runSetup(cmd *cobra.Command, deps Deps, info VersionInfo) error {
	flags := cmd.Flags()
	shellFlag, _ := flags.GetString("shell")
	write, _ := flags.GetBool("write")
	check, _ := flags.GetBool("check")
	uninstall, _ := flags.GetBool("uninstall")
	noBinding, _ := flags.GetBool("no-keybinding")
	yes, _ := flags.GetBool("yes")

	sh, err := setup.DetectShell(shellFlag)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	rep, err := setup.Run(setup.Options{
		Home:         deps.home(),
		Sh:           sh,
		Write:        write,
		Check:        check,
		Uninstall:    uninstall,
		NoKeybinding: noBinding,
		Yes:          yes,
		Stdin:        deps.stdin(),
		Stdout:       out,
		GenCompletion: func(s setup.Shell) (string, error) {
			return genCompletion(cmd.Root(), string(s))
		},
	})
	if err != nil {
		return err
	}
	for _, line := range rep.Lines {
		fmt.Fprintln(out, line)
	}
	if check && !rep.Complete {
		return errors.New("setup incomplete")
	}
	if write && rep.Changed {
		// RFC §6.2: setup finishes by running the diagnosis.
		rep2 := doctor.Run(doctor.Deps{
			Gh: deps.gh(), Home: deps.home(), Shell: string(sh),
			Version: info.Version, GenCompletion: func(shell string) (string, error) {
				return genCompletion(cmd.Root(), shell)
			},
		})
		ok, total := 0, 0
		for _, c := range rep2.Checks {
			if c.Required {
				total++
				if c.OK {
					ok++
				}
			}
		}
		fmt.Fprintf(out, "doctor: %d/%d required checks pass\n", ok, total)
	}
	return nil
}

// runDoctor implements `lead doctor` (RFC §6.6). Exit status reflects
// required checks only.
func runDoctor(cmd *cobra.Command, deps Deps, info VersionInfo) error {
	flags := cmd.Flags()
	offline, _ := flags.GetBool("offline")
	asJSON, _ := flags.GetBool("json")
	shellFlag, _ := flags.GetString("shell")

	rep := doctor.Run(doctor.Deps{
		Gh: deps.gh(), Home: deps.home(), Shell: shellFlag,
		Version: info.Version, Offline: offline, Repo: deps.repoSlug(),
		GenCompletion: func(shell string) (string, error) {
			return genCompletion(cmd.Root(), shell)
		},
	})
	out := cmd.OutOrStdout()
	if asJSON {
		fmt.Fprint(out, rep.JSON())
	} else {
		for _, c := range rep.Checks {
			fmt.Fprintf(out, "%s %s: %s\n", c.Mark(), c.Name, c.Detail)
		}
	}
	if !rep.OK() {
		return errors.New("doctor: required checks failed")
	}
	return nil
}

// runUpdate implements `lead update` (RFC §7).
func runUpdate(cmd *cobra.Command, deps Deps, info VersionInfo) error {
	flags := cmd.Flags()
	check, _ := flags.GetBool("check")
	yes, _ := flags.GetBool("yes")
	version, _ := flags.GetString("version")
	out := cmd.OutOrStdout()

	exe := deps.exePath()
	if update.BrewManaged(exe, deps.lookPath(), deps.BrewPrefix) {
		fmt.Fprintln(out, "lead is brew-managed; run: brew upgrade kuwa72/tap/lead")
		return nil
	}

	ctx := cmd.Context()
	if version == "" {
		latest, err := deps.gh().LatestReleaseTag(ctx, update.UpstreamRepo)
		if err != nil {
			return fmt.Errorf("latest release: %w", err)
		}
		version = latest
	}
	avail := update.CompareVersions(info.Version, version) < 0
	if check {
		if avail {
			fmt.Fprintf(out, "Update available: %s -> %s\n", info.Version, version)
			return errUpdateAvailable
		}
		fmt.Fprintf(out, "up to date: %s\n", info.Version)
		return nil
	}
	if !avail {
		fmt.Fprintf(out, "up to date: %s\n", info.Version)
		return nil
	}
	fmt.Fprintf(out, "Updating lead %s -> %s ...\n", info.Version, version)
	if !yes && !promptConfirm(deps.stdin(), fmt.Sprintf("Replace %s with %s? [y/N] ", exe, version)) {
		fmt.Fprintln(out, "aborted: no changes")
		return nil
	}
	if err := update.Replace(cmd.Context(), update.Options{Version: version, ExePath: exe}); err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed lead %s to %s\n", version, exe)
	return nil
}

var errUpdateAvailable = errors.New("update available")

func promptConfirm(stdin io.Reader, question string) bool {
	if stdin == nil {
		return false
	}
	var ans string
	if _, err := fmt.Fscanln(stdin, &ans); err != nil {
		return false
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// defaultSocketDir prefers the runtime dir, else the state file's dir.
func defaultSocketDir(stateFile string) string {
	if r := os.Getenv("XDG_RUNTIME_DIR"); r != "" {
		return r
	}
	return filepath.Dir(stateFile)
}

// resolveSocket picks the socket path: --socket, --session
// (lead-<session>.sock), or the default lead.sock.
func resolveSocket(cmd *cobra.Command, deps Deps) (string, error) {
	if sock, _ := cmd.Flags().GetString("socket"); sock != "" {
		return sock, nil
	}
	session, _ := cmd.Flags().GetString("session")
	name := "lead.sock"
	if session != "" {
		if strings.ContainsAny(session, "/\\") {
			return "", fmt.Errorf("invalid session %q", session)
		}
		name = "lead-" + session + ".sock"
	}
	return filepath.Join(defaultSocketDir(deps.stateFile()), name), nil
}

// runServer implements `lead server`: listen until SIGINT/SIGTERM.
func runServer(cmd *cobra.Command, deps Deps, info VersionInfo) error {
	sock, err := resolveSocket(cmd, deps)
	if err != nil {
		return err
	}
	cwd, _ := deps.workDir()
	srv, err := server.Listen(sock, server.Deps{
		Gh: deps.gh(), Git: deps.gitRunner(), StateFile: deps.stateFile(),
		WorkDir: cwd, Version: info.Version,
	})
	if err != nil {
		return err
	}
	defer srv.Close()
	fmt.Fprintf(cmd.OutOrStdout(), "serving on %s (Ctrl-C to stop)\n", sock)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		srv.Close()
	}()
	if err := srv.Serve(); err != nil && !isClosedConn(err) {
		return err
	}
	return nil
}

func isClosedConn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "use of closed network connection")
}

// runAPICall implements `lead api snapshot|call`: thin client over the socket.
func runAPICall(cmd *cobra.Command, deps Deps, op, rawArgs string) error {
	sock, err := resolveSocket(cmd, deps)
	if err != nil {
		return err
	}
	var args any
	if rawArgs != "" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			return fmt.Errorf("invalid --args JSON: %w", err)
		}
	}
	data, err := server.Call(sock, op, args)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	out := cmd.OutOrStdout()
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, pretty.String())
	return nil
}

func runFinish(cmd *cobra.Command, deps Deps, raw string) error {
	number, err := strconv.Atoi(raw)
	if err != nil || number <= 0 {
		return fmt.Errorf("invalid issue number %q", raw)
	}
	flags := cmd.Flags()
	pr, _ := flags.GetInt("pr")
	part, _ := flags.GetString("part")
	merge, _ := flags.GetBool("merge")
	closeIssue, _ := flags.GetBool("close")
	noClose, _ := flags.GetBool("no-close")
	comment, _ := flags.GetString("comment")
	outcome, _ := flags.GetString("outcome")
	policy, _ := flags.GetString("merge-policy")
	timeout, _ := flags.GetDuration("timeout")
	interval, _ := flags.GetDuration("poll-interval")

	res, err := finish.Run(cmd.Context(), deps.gh(), &state.Store{Path: deps.stateFile()}, number, finish.Options{
		PR: pr, Part: part, Merge: merge, Close: closeIssue, NoClose: noClose,
		Comment: comment, Outcome: outcome, MergePolicy: policy,
		Timeout: timeout, PollInterval: interval,
	})
	if res.Message != "" {
		fmt.Fprintln(cmd.OutOrStdout(), res.Message)
	}
	return err
}

// runClean implements `lead clean <number>`: safely remove the recorded
// worktree (guarded by the git adapter) and delete the state record.
// Missing records or already-gone worktrees succeed (idempotent).
func runClean(cmd *cobra.Command, deps Deps, raw string) error {
	number, err := strconv.Atoi(raw)
	if err != nil || number <= 0 {
		return fmt.Errorf("invalid issue number %q", raw)
	}
	part, _ := cmd.Flags().GetString("part")
	store := &state.Store{Path: deps.stateFile()}
	w, ok, err := store.Get(number, part)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(cmd.OutOrStdout(), "no workflow for #%d (nothing to do)\n", number)
		return nil
	}
	if w.Worktree != "" {
		if _, statErr := os.Stat(w.Worktree); statErr == nil {
			g := deps.gitRunner()
			repoRoot, rootErr := g.RepoRoot(w.Worktree)
			if rootErr != nil {
				return fmt.Errorf("clean #%d: %w", number, rootErr)
			}
			if rmErr := g.WorktreeRemove(repoRoot, w.Worktree, false); rmErr != nil {
				return fmt.Errorf("clean #%d: %w", number, rmErr)
			}
		}
	}
	if _, err := store.Delete(number, part); err != nil {
		return fmt.Errorf("clean #%d: %w", number, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "cleaned #%d (%s)\n", number, w.Branch)
	return nil
}

// runProjectInit implements `lead init`: project-oriented agent
// configuration (lead-flow skill + AGENTS.md managed block). Dry-run by
// default; --write applies after approval.
func runProjectInit(cmd *cobra.Command, deps Deps) error {
	flags := cmd.Flags()
	write, _ := flags.GetBool("write")
	check, _ := flags.GetBool("check")
	uninstall, _ := flags.GetBool("uninstall")
	yes, _ := flags.GetBool("yes")

	cwd, err := deps.workDir()
	if err != nil {
		return fmt.Errorf("init: %w", err)
	}
	repoRoot, err := deps.gitRunner().RepoRoot(cwd)
	if err != nil {
		return fmt.Errorf("init: project root: %w", err)
	}

	rep, err := projinit.Run(projinit.Options{
		Root:      repoRoot,
		Write:     write,
		Check:     check,
		Uninstall: uninstall,
		Yes:       yes,
		Stdin:     deps.stdin(),
	})
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	for _, line := range rep.Lines {
		fmt.Fprintln(out, line)
	}
	if check && !rep.Complete {
		return errors.New("lead-flow not installed")
	}
	return nil
}
