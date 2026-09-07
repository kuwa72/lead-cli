// Package cli defines lead's command skeleton (issue #39).
//
// Framework decision: cobra (see docs/rfc-26-distribution.md §6.3, §10 item 1).
// Rationale: `lead completion <bash|zsh|fish|powershell>` generation comes for
// free, the help体系 is uniform, and future subcommands (e.g. `server`/`api`
// for #48) nest naturally. The cost is two small deps (cobra, pflag).
//
// `work`/`setup`/`doctor`/`update` are honest stubs here: their RunE returns
// a "not yet implemented" error pointing at the implementing issue.
// Full behavior lands in #40 (work) and #44 (setup/doctor/update).
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
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

// NewRootCmd builds the lead command tree. Callers set Out/Err/Args in tests.
func NewRootCmd(version, commit, date string) *cobra.Command {
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
		Short: "Start working on an issue (branch + agent dispatch)",
		Long: `Select an issue (or pass its number), create a working branch, and
dispatch a coding agent. Full behavior lands in #40 (TUI selection in #37).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented(40)
		},
	}
	// Flags per docs/rfc-25-workflow-flexibility.md §6.1 (parsed now, honored in #40).
	workCmd.Flags().String("mode", "", "execution mode")
	workCmd.Flags().String("branch", "", "use existing branch instead of creating one")
	workCmd.Flags().String("pr", "", "attach to existing PR instead of creating one")
	workCmd.Flags().String("worktree", "", "isolated worktree directory")
	workCmd.Flags().String("part", "", "work unit within a multi-PR issue")
	workCmd.Flags().Bool("draft", false, "create PR as draft")
	workCmd.Flags().String("agent", "", "coding agent (default: agy)")

	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "Interactive environment setup (completions, config)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented(44)
		},
	}

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
		Short: "Diagnose environment (gh auth, herdr, agents)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented(44)
		},
	}

	updateCmd := &cobra.Command{
		Use:   "update",
		Short: "Update lead to the latest release",
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented(44)
		},
	}

	root.AddCommand(versionCmd, workCmd, setupCmd, completionCmd, doctorCmd, updateCmd)
	return root
}
