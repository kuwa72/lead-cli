package finish

import (
	"testing"

	"github.com/kuwa72/lead-cli/internal/state"
)

func TestDefaultForMode(t *testing.T) {
	cases := map[string]Policy{
		state.ModeImplement: PolicyAuto,
		state.ModeSplit:     PolicyAuto,
		state.ModeDocs:      PolicyNever,
		state.ModeResearch:  PolicyNever,
		"bogus":             PolicyConfirm, // unknown modes fail safe
		"":                  PolicyConfirm,
	}
	for mode, want := range cases {
		if got := DefaultForMode(mode); got != want {
			t.Errorf("DefaultForMode(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestResolvePriorityFlagOverRecordOverRepoOverDefault(t *testing.T) {
	// Flag wins over everything.
	if got := Resolve(state.ModeImplement, "never", "never", "auto"); got != PolicyAuto {
		t.Errorf("flag win: got %q", got)
	}
	// Record wins over repo and default.
	if got := Resolve(state.ModeImplement, "never", "auto", ""); got != PolicyNever {
		t.Errorf("record win: got %q", got)
	}
	// Repo wins over mode default.
	if got := Resolve(state.ModeImplement, "", "confirm", ""); got != PolicyConfirm {
		t.Errorf("repo win: got %q", got)
	}
	// Mode default applies when nothing is set.
	if got := Resolve(state.ModeImplement, "", "", ""); got != PolicyAuto {
		t.Errorf("default: got %q", got)
	}
	if got := Resolve(state.ModeDocs, "", "", ""); got != PolicyNever {
		t.Errorf("docs default: got %q", got)
	}
}

func TestResolveInvalidFallsBackToConfirm(t *testing.T) {
	// RFC §7.1: invalid values fall back to the safe side (confirm).
	for _, args := range [][4]string{
		{state.ModeImplement, "bogus", "", ""},
		{state.ModeImplement, "", "bogus", ""},
		{state.ModeImplement, "", "", "bogus"},
	} {
		if got := Resolve(args[0], args[1], args[2], args[3]); got != PolicyConfirm {
			t.Errorf("Resolve(%v) = %q, want safe confirm fallback", args, got)
		}
	}
}
