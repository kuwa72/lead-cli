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
	"github.com/kuwa72/lead-cli/internal/state"
)

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
