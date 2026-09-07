package finish

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

func errCloseBoom() error { return errors.New("close boom") }

func seedRecord(t *testing.T, path string, w state.Workflow) {
	t.Helper()
	if w.Issue == 0 {
		w.Issue = 36
	}
	if w.Mode == "" {
		w.Mode = state.ModeImplement
	}
	if w.Branch == "" {
		w.Branch = "issue/36-x"
	}
	if w.Status == "" {
		w.Status = state.StatusInProgress
	}
	if w.Repository == "" {
		w.Repository = "o/r"
	}
	if err := (&state.Store{Path: path}).Upsert(w); err != nil {
		t.Fatal(err)
	}
}

// seedWithPR seeds a record attached to PR 7 (the standard PR-path fixture).
func seedWithPR(t *testing.T, path string, w state.Workflow) {
	t.Helper()
	if len(w.PullRequests) == 0 {
		w.PullRequests = []state.PRRef{{Number: 7, Status: "open"}}
	}
	seedRecord(t, path, w)
}

func baseFake() *testutil.FakeGhClient {
	return &testutil.FakeGhClient{
		Checks:           []ports.PRCheck{{Name: "test", Bucket: "pass", State: "SUCCESS"}},
		PR:               ports.PRInfo{Number: 7, State: "OPEN", Mergeable: "MERGEABLE"},
		AutoMergeAllowed: true,
		Issues:           map[int]ports.Issue{36: {Number: 36, Title: "x"}},
	}
}

func noSleep(d time.Duration) {}

func loadOne(t *testing.T, path string) state.Workflow {
	t.Helper()
	all, err := (&state.Store{Path: path}).List()
	if err != nil || len(all) != 1 {
		t.Fatalf("state = %+v, %v; want one record", all, err)
	}
	return all[0]
}

func TestRun_FullChainWaitMergeClose(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Close: true, Comment: "done", Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Merged || !res.Closed || !res.ChecksPassed {
		t.Errorf("Result = %+v, want merged+closed+checks", res)
	}
	if len(fake.MergeCalls) != 1 || fake.MergeCalls[0] != 7 {
		t.Errorf("merges = %v, want one merge of PR 7", fake.MergeCalls)
	}
	if len(fake.Closed) != 1 || fake.Closed[0] != 36 {
		t.Errorf("closed = %v, want issue 36", fake.Closed)
	}
	if len(fake.Comments) != 1 || fake.Comments[0].Body != "done" {
		t.Errorf("comments = %+v, want completion comment", fake.Comments)
	}
	got := loadOne(t, path)
	if got.Status != state.StatusClosed {
		t.Errorf("recorded status = %q, want closed", got.Status)
	}
	if len(got.PullRequests) != 1 || got.PullRequests[0].Status != "merged" {
		t.Errorf("recorded PRs = %+v, want merged", got.PullRequests)
	}
}

func TestRun_WaitsForPendingChecks(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.Checks = nil
	fake.ChecksSeq = [][]ports.PRCheck{
		{},
		{{Name: "test", Bucket: "pending"}},
		{{Name: "test", Bucket: "pass"}},
	}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Merged || fake.ChecksCalls < 3 {
		t.Errorf("Result = %+v, checks calls = %d; want wait-through-pending then merge", res, fake.ChecksCalls)
	}
}

func TestRun_FailingChecksStopWithoutMerge(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.Checks = []ports.PRCheck{{Name: "test", Bucket: "fail", State: "FAILURE"}}

	_, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err == nil {
		t.Fatal("Run with failing checks = nil, want stop")
	}
	if len(fake.MergeCalls) != 0 {
		t.Errorf("merges = %v, want none on failing CI", fake.MergeCalls)
	}
	if len(fake.Closed) != 0 {
		t.Errorf("closed = %v, want none on failing CI", fake.Closed)
	}
	if got := loadOne(t, path); got.Status != state.StatusInProgress {
		t.Errorf("recorded status = %q, want untouched in_progress", got.Status)
	}
}

func TestRun_ConflictingPRStopsWithoutMerge(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.PR = ports.PRInfo{Number: 7, State: "OPEN", Mergeable: "CONFLICTING"}

	_, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Errorf("Run on conflicting PR = %v, want conflict stop", err)
	}
	if len(fake.MergeCalls) != 0 {
		t.Errorf("merges = %v, want none on conflict", fake.MergeCalls)
	}
}

func TestRun_ConfirmWithoutMergeStopsAfterChecks(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{MergePolicy: "confirm"})
	fake := baseFake()

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("confirm stop = %v, want clean pause (not failure)", err)
	}
	if res.Merged || !res.ChecksPassed {
		t.Errorf("Result = %+v, want checks-passed pause without merge", res)
	}
	if !strings.Contains(res.Message, "--merge") {
		t.Errorf("message = %q, want --merge guidance", res.Message)
	}
}

func TestRun_NeverPolicyStopsAfterChecks(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{Mode: state.ModeDocs, MergePolicy: "never"})
	fake := baseFake()

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("never stop = %v, want clean stop", err)
	}
	if res.Merged || len(fake.MergeCalls) != 0 {
		t.Errorf("Result = %+v, merges = %v; never policy must not merge", res, fake.MergeCalls)
	}
}

func TestRun_AutoMergeDisabledFallsBack(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.AutoMergeAllowed = false // repo setting disallows auto-merge

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("auto-disabled fallback = %v, want clean pause", err)
	}
	if res.Merged || len(fake.MergeCalls) != 0 {
		t.Errorf("Result = %+v; must not auto-merge when repo disallows it", res)
	}
	if !strings.Contains(res.Message, "auto-merge") {
		t.Errorf("message = %q, want auto-merge-disabled fallback display", res.Message)
	}
}

func TestRun_NoCloseSkipsClose(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, NoClose: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil || !res.Merged || res.Closed {
		t.Errorf("Result = %+v, %v; want merged without close", res, err)
	}
	if len(fake.Closed) != 0 {
		t.Errorf("closed = %v, want none with --no-close", fake.Closed)
	}
	if got := loadOne(t, path); got.Status != state.StatusCompleted {
		t.Errorf("recorded status = %q, want completed (not closed)", got.Status)
	}
}

func TestRun_SplitParentIsNotAutoClosed(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{Mode: state.ModeSplit, Part: ""})
	fake := baseFake()

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("split parent: %v", err)
	}
	if !res.Merged || res.Closed {
		t.Errorf("Result = %+v; split parent merges but never auto-closes", res)
	}
	if len(fake.Closed) != 0 {
		t.Errorf("closed = %v, want none for split parent", fake.Closed)
	}
}

func TestRun_AlreadyMergedSkipsToClose(t *testing.T) {
	// Retry path: `lead finish --close` after a merge must not re-merge.
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.PR = ports.PRInfo{Number: 7, State: "MERGED", Mergeable: "MERGEABLE"}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil || res.Merged || !res.Closed {
		t.Errorf("Result = %+v, %v; want close-only retry", res, err)
	}
	if len(fake.MergeCalls) != 0 {
		t.Errorf("merges = %v, want none on already-merged PR", fake.MergeCalls)
	}
}

func TestRun_CloseFailureKeepsMerge(t *testing.T) {
	// RFC §7.2: a failed auto-close must not roll back the merge.
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.CloseErr = errCloseBoom()

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err == nil {
		t.Fatal("Run with failing close = nil, want error")
	}
	if !res.Merged {
		t.Errorf("Result = %+v, want merged kept despite close failure", res)
	}
	if got := loadOne(t, path); got.Status != state.StatusCompleted {
		t.Errorf("recorded status = %q, want completed (merge kept)", got.Status)
	}
}

func TestRun_NoRecordFails(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	fake := baseFake()

	if _, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep,
	}); err == nil {
		t.Error("Run without record = nil, want guidance to `lead work` first")
	}
}

func TestRun_TimeoutStopsWait(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.Checks = []ports.PRCheck{{Name: "test", Bucket: "pending"}}

	_, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: 50 * time.Millisecond, Sleep: noSleep,
	})
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Run on stuck checks = %v, want timeout", err)
	}
	if len(fake.MergeCalls) != 0 {
		t.Errorf("merges = %v, want none on timeout", fake.MergeCalls)
	}
}

func TestRun_ResearchWithoutPRNeedsOutcome(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedRecord(t, path, state.Workflow{Mode: state.ModeResearch, PullRequests: nil})
	fake := baseFake()

	if _, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Timeout: time.Minute, Sleep: noSleep,
	}); err == nil {
		t.Error("research finish without outcome = nil, want artifact requirement")
	}
	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Outcome: "researched", Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil || !res.Closed {
		t.Errorf("research with outcome = %+v, %v; want recorded + closed", res, err)
	}
}
