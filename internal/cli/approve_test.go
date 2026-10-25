package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

// Issue #255: `lead approve` merges an awaiting-human issue, closes it, and
// clears the label in one command.
func TestApprove_MergesAwaitingIssueViaCLI(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "wf.json")
	fake := seedFinishRecord(t, stateFile, nil)
	fake.Issues[36] = ports.Issue{Number: 36, Title: "x", Labels: []string{"risk:b", state.AwaitingHumanLabel}}

	out, err := executeWith(t, Deps{Gh: fake, StateFile: stateFile}, "approve", "36")
	if err != nil {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	if len(fake.MergeCalls) != 1 || len(fake.Closed) != 1 {
		t.Errorf("merges = %v closed = %v; want one merge and one close", fake.MergeCalls, fake.Closed)
	}
	if len(fake.RemovedLabels) != 1 || fake.RemovedLabels[0].Label != state.AwaitingHumanLabel {
		t.Errorf("removed labels = %+v, want awaiting-human removed", fake.RemovedLabels)
	}
	if !strings.Contains(out, "merged") || !strings.Contains(out, "closed") {
		t.Errorf("output = %q, want merge+close report", out)
	}
}

// Issue #255: an issue that is not awaiting-human is refused.
func TestApprove_RejectsNonAwaitingIssueViaCLI(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "wf.json")
	fake := seedFinishRecord(t, stateFile, nil)
	fake.Issues[36] = ports.Issue{Number: 36, Title: "x", Labels: []string{"risk:b"}}

	out, err := executeWith(t, Deps{Gh: fake, StateFile: stateFile}, "approve", "36")
	if err == nil || !strings.Contains(err.Error(), state.AwaitingHumanLabel) {
		t.Fatalf("approve non-awaiting = %v, want rejection\n%s", err, out)
	}
	if len(fake.MergeCalls) != 0 || len(fake.Closed) != 0 {
		t.Errorf("merges = %v closed = %v; want none", fake.MergeCalls, fake.Closed)
	}
}

// Issue #255: --dry-run shows the target PR and review state only.
func TestApprove_DryRunViaCLI(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "wf.json")
	fake := seedFinishRecord(t, stateFile, nil)
	fake.Issues[36] = ports.Issue{Number: 36, Title: "x", Labels: []string{"risk:b", state.AwaitingHumanLabel}}

	out, err := executeWith(t, Deps{Gh: fake, StateFile: stateFile}, "approve", "36", "--dry-run")
	if err != nil {
		t.Fatalf("approve --dry-run: %v\n%s", err, out)
	}
	if len(fake.MergeCalls) != 0 || len(fake.Closed) != 0 {
		t.Errorf("dry-run merged/closed: merges=%v closed=%v", fake.MergeCalls, fake.Closed)
	}
	if !strings.Contains(out, "#7") || !strings.Contains(out, state.ReviewPassLabel) {
		t.Errorf("output = %q, want PR #7 and the review verdict", out)
	}
}
