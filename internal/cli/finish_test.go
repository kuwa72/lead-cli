package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// seedFinishRecord stores an in-progress workflow attached to PR 7.
func seedFinishRecord(t *testing.T, stateFile string, mutate func(*state.Workflow)) *testutil.FakeGhClient {
	t.Helper()
	w := state.Workflow{
		Repository:   "o/r",
		Issue:        36,
		Mode:         state.ModeImplement,
		Branch:       "issue/36-x",
		Status:       state.StatusInProgress,
		PullRequests: []state.PRRef{{Number: 7, Status: "open"}},
	}
	if mutate != nil {
		mutate(&w)
	}
	if err := (&state.Store{Path: stateFile}).Upsert(w); err != nil {
		t.Fatal(err)
	}
	return &testutil.FakeGhClient{
		Checks:           []ports.PRCheck{{Name: "test", Bucket: "pass", State: "SUCCESS"}},
		PR:               ports.PRInfo{Number: 7, State: "OPEN", Mergeable: "MERGEABLE"},
		AutoMergeAllowed: true,
	}
}

func TestFinish_FullChainViaCLI(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "wf.json")
	fake := seedFinishRecord(t, stateFile, nil)

	out, err := executeWith(t, Deps{Gh: fake, StateFile: stateFile}, "finish", "36", "--merge", "--close")
	if err != nil {
		t.Fatalf("finish: %v\n%s", err, out)
	}
	if !strings.Contains(out, "merged") || !strings.Contains(out, "closed") {
		t.Errorf("finish output = %q, want merge+close report", out)
	}
	if len(fake.MergeCalls) != 1 || len(fake.Closed) != 1 {
		t.Errorf("merges = %v, closed = %v; want one merge and one close", fake.MergeCalls, fake.Closed)
	}
	all, err := (&state.Store{Path: stateFile}).List()
	if err != nil || len(all) != 1 || all[0].Status != state.StatusClosed {
		t.Errorf("state = %+v, %v; want closed record", all, err)
	}
}

func TestFinish_FailingChecksStopViaCLI(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "wf.json")
	fake := seedFinishRecord(t, stateFile, nil)
	fake.Checks = []ports.PRCheck{{Name: "test", Bucket: "fail", State: "FAILURE"}}

	_, err := executeWith(t, Deps{Gh: fake, StateFile: stateFile}, "finish", "36", "--merge", "--close")
	if err == nil {
		t.Fatal("finish on failing checks = nil, want stop")
	}
	if len(fake.MergeCalls) != 0 || len(fake.Closed) != 0 {
		t.Errorf("merges = %v, closed = %v; want none", fake.MergeCalls, fake.Closed)
	}
}

func TestFinish_ConfirmPauseViaCLI(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "wf.json")
	fake := seedFinishRecord(t, stateFile, func(w *state.Workflow) { w.MergePolicy = "confirm" })

	out, err := executeWith(t, Deps{Gh: fake, StateFile: stateFile}, "finish", "36")
	if err != nil {
		t.Fatalf("confirm pause = %v, want clean pause", err)
	}
	if !strings.Contains(out, "--merge") {
		t.Errorf("pause output = %q, want --merge guidance", out)
	}
	if len(fake.MergeCalls) != 0 {
		t.Errorf("merges = %v, want none on pause", fake.MergeCalls)
	}
}

func TestFinish_NoRecordViaCLI(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "wf.json")
	fake := &testutil.FakeGhClient{}

	_, err := executeWith(t, Deps{Gh: fake, StateFile: stateFile}, "finish", "36", "--merge")
	if err == nil {
		t.Error("finish without record = nil, want failure")
	}
}
