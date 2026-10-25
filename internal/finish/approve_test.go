package finish

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// Issue #255: `lead approve` only accepts issues the risk gate parked as
// awaiting-human; anything else is refused before any merge.
func TestApprove_RejectsIssueNotAwaitingHuman(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := riskFake("risk:b")

	_, err := Approve(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Timeout: time.Minute, Sleep: noSleep,
	})
	if err == nil || !strings.Contains(err.Error(), state.AwaitingHumanLabel) {
		t.Fatalf("Approve without awaiting-human = %v, want rejection", err)
	}
	if len(fake.MergeCalls) != 0 || len(fake.Closed) != 0 {
		t.Errorf("merges = %v closed = %v, want none", fake.MergeCalls, fake.Closed)
	}
}

// Issue #255: an awaiting-human issue is approved in one step: CI wait,
// squash merge, close, and the awaiting-human label comes off.
func TestApprove_MergesAndClosesAwaitingIssue(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := riskFake("risk:b", state.AwaitingHumanLabel)

	res, err := Approve(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !res.Merged || !res.Closed || !res.ChecksPassed {
		t.Errorf("Result = %+v, want checks+merged+closed", res)
	}
	if want := []int{7}; !reflect.DeepEqual(fake.MergeCalls, want) {
		t.Errorf("merges = %v, want %v", fake.MergeCalls, want)
	}
	if want := []int{36}; !reflect.DeepEqual(fake.Closed, want) {
		t.Errorf("closed = %v, want %v", fake.Closed, want)
	}
	if want := []testutil.LabelCall{{Number: 36, Label: state.AwaitingHumanLabel}}; !reflect.DeepEqual(fake.RemovedLabels, want) {
		t.Errorf("removed labels = %+v, want awaiting-human removed", fake.RemovedLabels)
	}
}

// Issue #255: --dry-run reports the PR and review state without merging.
func TestApprove_DryRunReportsWithoutMerging(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := riskFake("risk:b", state.AwaitingHumanLabel)

	res, err := Approve(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		DryRun: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("Approve dry-run: %v", err)
	}
	if res.Merged || res.Closed || len(fake.MergeCalls) != 0 || len(fake.Closed) != 0 {
		t.Errorf("dry-run merged/closed: res=%+v merges=%v closed=%v", res, fake.MergeCalls, fake.Closed)
	}
	if !strings.Contains(res.Message, "#7") || !strings.Contains(res.Message, state.ReviewPassLabel) {
		t.Errorf("message = %q, want PR number and review state", res.Message)
	}
}

// Issue #255: a missing review:pass warns but does not stop the human.
func TestApprove_WarnsWithoutReviewPass(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := riskFake("risk:b", state.AwaitingHumanLabel)
	fake.PRLabels = nil

	res, err := Approve(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		DryRun: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("Approve dry-run: %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Message), "warning") {
		t.Errorf("message = %q, want a review:pass warning", res.Message)
	}

	res, err = Approve(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("Approve without review:pass: %v", err)
	}
	if !res.Merged || !strings.Contains(strings.ToLower(res.Message), "warning") {
		t.Errorf("Result = %+v, want a merge with the warning kept", res)
	}
}
