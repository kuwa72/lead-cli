// Command lead is the single-binary orchestrator CLI.
package main

import (
	"os"

	"github.com/kuwa72/lead-cli/internal/cli"
)

// Version info is injected at build time via -ldflags "-X main.Version=...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func main() {
	root := cli.NewRootCmd(Version, Commit, Date)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
