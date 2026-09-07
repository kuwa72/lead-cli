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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kuwa72/lead-cli/internal/adapters/ghcli"
	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/doctor"
	"github.com/kuwa72/lead-cli/internal/finish"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/setup"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/update"
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
type GitRunner interface {
	RepoRoot(dir string) (string, error)
	CreateBranch(repoDir, branch string) error
	WorktreeAdd(repoDir, path, branch string) error
	WorktreeRemove(repoDir, path string, force bool) error
	OriginURL(repoDir string) string
}

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
coding agent (Herdr side-pane or inline), then wait CI and merge.`,
		SilenceUsage: true, // runtime/stub errors print the error, not full usage
		// Historical #35 behavior: bare `lead --version`/`-v` prints the same
		// stamped string as `lead version` (cobra's built-in --version flag
		// cannot take a shorthand or custom template output, so handle it here).
		RunE: func(cmd *cobra.Command, args []string) error {
			if v, _ := cmd.Flags().GetBool("version"); v {
				fmt.Fprintln(cmd.OutOrStdout(), info.String())
				return nil
			}
			return cmd.Help()
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

	workCmd := &cobra.Command{
		Use:   "work [issue-number]",
		Short: "Start working on an issue (branch + worktree + state)",
		Long: `With an issue number: fetch the issue, create/check out the working
branch, optionally create a worktree, and record the workflow state.
Without a number, issue selection needs the #37 TUI (not yet implemented).
Agent dispatch lands in #37; this command prints the next steps.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return notImplemented(37)
			}
			return runWork(cmd, deps, args[0])
		},
	}
	// Flags per docs/rfc-25-workflow-flexibility.md §6.1.
	workCmd.Flags().String("mode", state.ModeImplement, "execution mode (implement|split|research|docs)")
	workCmd.Flags().String("branch", "", "use existing branch instead of creating one")
	workCmd.Flags().String("pr", "", "attach to existing PR instead of creating one")
	workCmd.Flags().String("worktree", "", "create isolated worktree: bare flag = auto path, or --worktree=<path>")
	workCmd.Flags().String("part", "", "work unit within a multi-PR issue")
	workCmd.Flags().Bool("draft", false, "create PR as draft")
	workCmd.Flags().String("agent", "", "coding agent (default: agy)")
	// Bare `--worktree` means "auto path under <repo>/.worktrees".
	workCmd.Flags().Lookup("worktree").NoOptDefVal = "auto"

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

	root.AddCommand(versionCmd, workCmd, statusCmd, cleanCmd, finishCmd, setupCmd, completionCmd, doctorCmd, updateCmd)
	return root
}

// runWork implements `lead work <number>`: branch + optional worktree +
// state record. Re-running with the same branch is idempotent.
func runWork(cmd *cobra.Command, deps Deps, raw string) error {
	number, err := strconv.Atoi(raw)
	if err != nil || number <= 0 {
		return fmt.Errorf("invalid issue number %q", raw)
	}
	mode, _ := cmd.Flags().GetString("mode")
	branchFlag, _ := cmd.Flags().GetString("branch")
	part, _ := cmd.Flags().GetString("part")
	wtFlag := cmd.Flags().Lookup("worktree")

	iss, err := deps.gh().View(cmd.Context(), number)
	if err != nil {
		return fmt.Errorf("work #%d: %w", number, err)
	}
	cwd, err := deps.workDir()
	if err != nil {
		return fmt.Errorf("work #%d: working directory: %w", number, err)
	}
	g := deps.gitRunner()
	repoRoot, err := g.RepoRoot(cwd)
	if err != nil {
		return fmt.Errorf("work #%d: %w", number, err)
	}
	branch := branchFlag
	if branch == "" {
		branch = git.BranchName(number, iss.Title)
	}
	wantWorktree := wtFlag != nil && wtFlag.Changed
	if !wantWorktree {
		if err := g.CreateBranch(repoRoot, branch); err != nil {
			return fmt.Errorf("work #%d: branch %s: %w", number, branch, err)
		}
	}
	worktree := ""
	if wantWorktree {
		// NOTE: create the branch inside the new worktree (`git worktree add
		// -b`); checking it out in the main repo first would make the branch
		// busy and the add would fail.
		worktree = wtFlag.Value.String()
		if worktree == "" || worktree == "auto" {
			worktree = filepath.Join(repoRoot, ".worktrees", fmt.Sprintf("issue-%d", number))
			if part != "" {
				worktree += "-" + part
			}
		}
		if err := g.WorktreeAdd(repoRoot, worktree, branch); err != nil {
			return fmt.Errorf("work #%d: worktree %s: %w", number, worktree, err)
		}
	}

	store := &state.Store{Path: deps.stateFile()}
	existing, ok, err := store.Get(number, part)
	if err != nil {
		return fmt.Errorf("work #%d: %w", number, err)
	}
	repo := g.OriginURL(repoRoot)
	if repo == "" {
		repo = "local"
	}
	w := state.Workflow{
		Repository: repo,
		Issue:      number,
		Mode:       mode,
		Part:       part,
		Branch:     branch,
		Worktree:   worktree,
		Status:     state.StatusInProgress,
	}
	if ok {
		// Idempotent re-work: keep progress status and any fields the user
		// did not re-specify (worktree stays unless --worktree given).
		if err := state.CheckTransition(existing.Status, state.StatusInProgress); err != nil && existing.Status != state.StatusInProgress {
			return fmt.Errorf("work #%d: %w", number, err)
		}
		w.Status = existing.Status
		if worktree == "" {
			w.Worktree = existing.Worktree
		}
		w.PullRequests = existing.PullRequests
		w.MergePolicy = existing.MergePolicy
		w.Artifacts = existing.Artifacts
	}
	if err := store.Upsert(w); err != nil {
		return fmt.Errorf("work #%d: %w", number, err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Issue #%d  %s  %s\n", number, mode, w.Status)
	fmt.Fprintf(out, "Branch: %s", branch)
	if w.Worktree != "" {
		fmt.Fprintf(out, "  Worktree: %s", w.Worktree)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Next: review the prompt, launch the agent (see #37), then `lead status`.")
	return nil
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
		Version: info.Version, Offline: offline,
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
