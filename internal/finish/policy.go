// Package finish implements `lead finish`: PR re-verification, CI wait,
// policy-gated squash merge, and issue close (issue #41).
// See docs/rfc-25-workflow-flexibility.md §7.
//
// Relationship with bin/ci-wait: the wait loop here is a Go re-implementation
// of ci-wait's polling semantics (pending/empty checks keep waiting, fail
// buckets stop, conflicts stop) so it is unit-testable with fakes and dummy
// gh. bin/ci-wait stays for direct human use (AGENTS.md references it);
// removal or delegation is out of scope.
package finish

import (
	"path"
	"strings"

	"github.com/kuwa72/lead-cli/internal/state"
)

// ProtectedPaths are the project-convention files an unattended agent must
// not merge changes to on its own (docs/rfc-inbox-ux.md §7 guardrail, §9).
// A trailing "/" matches the whole directory; a trailing "*" matches a
// prefix; a bare name matches that file at the repository root (AGENTS.md
// also anywhere below, since nested rule files bind agents the same way).
// Single source of truth: `lead finish` reads it, tests exercise it.
var ProtectedPaths = []string{
	"AGENTS.md",
	".github/",
	".claude/",
	".devin/",
	".goreleaser*",
	"install.sh",
}

// GuardedFiles returns the subset of files matching ProtectedPaths, in
// input order.
func GuardedFiles(files []string) []string {
	var hit []string
	for _, f := range files {
		if isProtectedPath(f) {
			hit = append(hit, f)
		}
	}
	return hit
}

func isProtectedPath(file string) bool {
	f := strings.TrimPrefix(strings.TrimSpace(file), "./")
	for _, p := range ProtectedPaths {
		switch {
		case strings.HasSuffix(p, "/"):
			if strings.HasPrefix(f, p) {
				return true
			}
		case strings.HasSuffix(p, "*"):
			if strings.HasPrefix(f, strings.TrimSuffix(p, "*")) {
				return true
			}
		case p == "AGENTS.md":
			if path.Base(f) == p {
				return true
			}
		default:
			if f == p {
				return true
			}
		}
	}
	return false
}

// Policy is the merge policy (RFC §7.1).
type Policy string

const (
	PolicyAuto    Policy = "auto"
	PolicyConfirm Policy = "confirm"
	PolicyNever   Policy = "never"
)

// DefaultForMode returns the mode default: implement/split children auto,
// docs/research never, anything unknown fails safe to confirm.
func DefaultForMode(mode string) Policy {
	switch mode {
	case state.ModeImplement, state.ModeSplit:
		return PolicyAuto
	case state.ModeDocs, state.ModeResearch:
		return PolicyNever
	default:
		return PolicyConfirm
	}
}

func validPolicy(s string) (Policy, bool) {
	switch Policy(s) {
	case PolicyAuto, PolicyConfirm, PolicyNever:
		return Policy(s), true
	default:
		return PolicyConfirm, false
	}
}

// Resolve picks the merge policy by priority (RFC §7.1):
// CLI flag > issue record > repository setting > mode default.
// Invalid values fall back to the safe side (confirm).
func Resolve(mode, recordPolicy, repoPolicy, flag string) Policy {
	if flag != "" {
		if p, ok := validPolicy(flag); ok {
			return p
		}
		return PolicyConfirm
	}
	if recordPolicy != "" {
		if p, ok := validPolicy(recordPolicy); ok {
			return p
		}
		return PolicyConfirm
	}
	if repoPolicy != "" {
		if p, ok := validPolicy(repoPolicy); ok {
			return p
		}
		return PolicyConfirm
	}
	return DefaultForMode(mode)
}
