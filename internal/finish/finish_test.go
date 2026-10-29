package finish

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ledger"
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

// testSHA is the PR head the base fixtures' review comment passed.
const testSHA = "0123456789abcdef0123456789abcdef01234567"

func baseFake() *testutil.FakeGhClient {
	return &testutil.FakeGhClient{
		Checks:           []ports.PRCheck{{Name: "test", Bucket: "pass", State: "SUCCESS"}},
		PR:               ports.PRInfo{Number: 7, State: "OPEN", Mergeable: "MERGEABLE"},
		AutoMergeAllowed: true,
		// risk:a keeps the pre-#245 merge fixtures merging; the risk-gate
		// tests below opt into other label sets explicitly. issue #246:
		// automatic merge also needs the independent review's pass, and
		// issue #249 binds that pass to the reviewed head SHA.
		Issues:    map[int]ports.Issue{36: {Number: 36, Title: "x", Labels: []string{"risk:a"}}},
		PRLabels:  []string{state.ReviewPassLabel},
		PRHeadSHA: testSHA,
		PRThread:  []ports.Comment{{Body: "<!-- lead-review sha=" + testSHA + " verdict=pass -->"}},
	}
}

func riskFake(labels ...string) *testutil.FakeGhClient {
	f := baseFake()
	f.Issues[36] = ports.Issue{Number: 36, Title: "x", Labels: labels}
	return f
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
	// issue #250: the merge is pinned to the reviewed head.
	if want := []string{testSHA}; !reflect.DeepEqual(fake.MergeHeads, want) {
		t.Errorf("merge heads = %v, want %v", fake.MergeHeads, want)
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
		t.Error("Run without record = nil, want guidance to `lead run` first")
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

// Guardrail (RFC inbox §7, issue #93 Part B): a PR that touches protected
// convention files must not auto-merge even under policy auto.
func TestRun_GuardrailLowersAutoToConfirm(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.Files = []string{"internal/x.go", "AGENTS.md", ".github/workflows/ci.yml"}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("guardrail pause = %v, want clean pause", err)
	}
	if res.Merged || len(fake.MergeCalls) != 0 || !res.ChecksPassed {
		t.Errorf("Result = %+v merges=%v; guarded PR must pause after checks without merging", res, fake.MergeCalls)
	}
	if !reflect.DeepEqual(fake.FilesCalls, []int{7}) {
		t.Errorf("PrFiles asked for %v, want [7]", fake.FilesCalls)
	}
	if !reflect.DeepEqual(res.Guarded, []string{"AGENTS.md", ".github/workflows/ci.yml"}) {
		t.Errorf("Guarded = %q", res.Guarded)
	}
	for _, want := range []string{"AGENTS.md", ".github/workflows/ci.yml", "protected files", "--override-guardrail"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("message %q lacks %q", res.Message, want)
		}
	}
	got := loadOne(t, path)
	if got.Status != state.StatusInProgress {
		t.Errorf("status = %q, want still in_progress", got.Status)
	}
}

// issue #230: the headless completion definition merges through
// `lead finish --merge`, so --merge alone must NOT count as the human
// confirmation that lifts the guardrail. Only --override-guardrail does.
func TestRun_GuardrailMergeFlagDoesNotOverride(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.Files = []string{".claude/settings.json"}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Merged || len(fake.MergeCalls) != 0 || len(fake.Closed) != 0 {
		t.Errorf("Result = %+v merges=%v closed=%v; --merge alone must not merge a guarded PR", res, fake.MergeCalls, fake.Closed)
	}
	if !strings.Contains(res.Message, "--override-guardrail") {
		t.Errorf("message %q lacks the human override hint", res.Message)
	}
}

func TestRun_GuardrailOverrideMerges(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.Files = []string{".claude/settings.json"}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, OverrideGuardrail: true, Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Merged || len(fake.MergeCalls) != 1 {
		t.Errorf("Result = %+v merges=%v; explicit --override-guardrail is the human confirmation", res, fake.MergeCalls)
	}
}

// issue #230: dispatch's state record has no PR number, so the headless
// `lead finish <issue> --merge` looks it up from the recorded branch.
func TestRun_NoRecordedPRDiscoversByBranch(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedRecord(t, path, state.Workflow{Branch: "issue/36-x"})
	fake := baseFake()
	fake.PRByBranch = map[string]int{"issue/36-x": 7}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Merged {
		t.Errorf("Result = %+v, want merge after discovering PR 7", res)
	}
	if !reflect.DeepEqual(fake.PRByBranchCalls, []string{"issue/36-x"}) {
		t.Errorf("branch lookups = %v, want the recorded branch", fake.PRByBranchCalls)
	}
	if !reflect.DeepEqual(fake.MergeCalls, []int{7}) {
		t.Errorf("merges = %v, want PR 7", fake.MergeCalls)
	}
}

func TestRun_GuardrailLeavesUnrelatedPRsOnAuto(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.Files = []string{"internal/x.go", "docs/rfc.md"}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil || !res.Merged || len(res.Guarded) != 0 {
		t.Errorf("Result = %+v, %v; want normal auto merge", res, err)
	}
	if got := loadOne(t, path); got.MergePolicy != "" || got.PolicyReason != "" {
		t.Errorf("record policy = %q/%q, want untouched", got.MergePolicy, got.PolicyReason)
	}
}

func TestRun_GuardrailFileListFailureStops(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.FilesErr = errors.New("gh boom")

	_, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err == nil || !strings.Contains(err.Error(), "gh boom") {
		t.Fatalf("err = %v, want file-list failure surfaced (fail closed)", err)
	}
	if len(fake.MergeCalls) != 0 {
		t.Errorf("merged despite unknown file list")
	}
}

func TestRun_RepoURLIsReducedToSlugForAPI(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{Repository: "https://github.com/o/r.git"})
	fake := baseFake()
	fake.AutoMergeAllowed = false

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil || res.Merged {
		t.Fatalf("Result = %+v, %v; want auto-merge fallback pause", res, err)
	}
	if !reflect.DeepEqual(fake.AutoMergeRepos, []string{"o/r"}) {
		t.Errorf("repo passed to gh api = %v, want [o/r]", fake.AutoMergeRepos)
	}
}

// CI alone is enough: review:pass is advisory since #278.
func TestRun_CIOnlyMerges(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := riskFake("risk:a")

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil || !res.Merged {
		t.Fatalf("Result = %+v, %v; want risk:a merge", res, err)
	}
	if !reflect.DeepEqual(fake.MergeCalls, []int{7}) || !reflect.DeepEqual(fake.Closed, []int{36}) {
		t.Errorf("merges = %v closed = %v; want PR 7 merged and issue 36 closed", fake.MergeCalls, fake.Closed)
	}
}

// issue #246/#249/#250/#278: review markers still provide the scenario
// score; a failed comment or head lookup fails closed.
// A failed PR-comment lookup cannot prove a review pass, so finish must
// stop (fail closed).
func TestRun_ReviewCommentLookupFailureStops(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := riskFake("risk:a")
	fake.PRThreadErr = errors.New("comments boom")

	_, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err == nil || !strings.Contains(err.Error(), "comments boom") {
		t.Fatalf("err = %v, want the comment lookup failure surfaced", err)
	}
	if len(fake.MergeCalls) != 0 {
		t.Errorf("merged despite an unknown review verdict")
	}
}

// A failed head-SHA lookup cannot prove the review is current either.
func TestRun_HeadSHALookupFailureStops(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := riskFake("risk:a")
	fake.PRHeadSHAErr = errors.New("head boom")

	_, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err == nil || !strings.Contains(err.Error(), "head boom") {
		t.Fatalf("err = %v, want the head lookup failure surfaced", err)
	}
	if len(fake.MergeCalls) != 0 {
		t.Errorf("merged despite an unknown head SHA")
	}
}

// issue #273: a skipped check is not a pass. GitHub reports skipped/neutral
// checks as mergeable, so a path-filtered job could let an untested PR merge.
func TestWaitChecks_SkippingIsNotPass(t *testing.T) {
	fake := &testutil.FakeGhClient{
		Checks: []ports.PRCheck{{Name: "test", Bucket: "skipping", State: "SKIPPED"}},
	}
	err := WaitChecks(context.Background(), fake, 7, time.Minute, time.Millisecond, noSleep)
	if err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Fatalf("err = %v, want skipped-check failure", err)
	}
}

func TestWaitChecks_PassStillSucceeds(t *testing.T) {
	fake := &testutil.FakeGhClient{
		Checks: []ports.PRCheck{{Name: "test", Bucket: "pass", State: "SUCCESS"}},
	}
	if err := WaitChecks(context.Background(), fake, 7, time.Minute, time.Millisecond, noSleep); err != nil {
		t.Fatalf("err = %v, want pass", err)
	}
}

// issue #274: finish appends a merge record; it must not count as another run.
func TestRun_RecordsMergeInLedger(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	ledgerPath := t.TempDir() + "/runs.jsonl"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()

	if _, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep, LedgerPath: ledgerPath,
	}); err != nil {
		t.Fatal(err)
	}
	recs, err := ledger.Read(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	if recs[0].Phase != ledger.PhaseMerge || recs[0].Outcome != ledger.OutcomeCompleted {
		t.Fatalf("record = %+v", recs[0])
	}
}

// issue #274 review: --outcome blocked is not a success.
func TestMergeOutcome_BlockedIsNotCompleted(t *testing.T) {
	if got := mergeOutcome(Result{}, nil, Options{Outcome: "blocked"}); got != ledger.OutcomeBlocked {
		t.Fatalf("outcome = %q, want %q", got, ledger.OutcomeBlocked)
	}
	if got := mergeOutcome(Result{}, errors.New("boom"), Options{}); got != ledger.OutcomeError {
		t.Fatalf("outcome = %q, want error", got)
	}
}

// issue #278: a scenario score recorded by `lead review` gates the merge
// even when the diff review passed.
func TestRun_ScenarioFailureStops(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.PRThread = []ports.Comment{{Body: "<!-- lead-review sha=" + testSHA + " verdict=pass scenarios=1/2 -->"}}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("scenario pause = %v, want a clean pause", err)
	}
	if res.Merged || len(fake.MergeCalls) != 0 {
		t.Fatalf("Result = %+v merges=%v; want no merge at 1/2", res, fake.MergeCalls)
	}
	if !strings.Contains(res.Message, "1/2") {
		t.Errorf("message = %q, want the scenario score", res.Message)
	}
	if len(fake.Comments) != 1 {
		t.Errorf("comments = %+v, want the pause reason on the issue", fake.Comments)
	}
}

func TestRun_ScenarioStaleHeadStops(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.PRThread = []ports.Comment{{Body: "<!-- lead-review sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa verdict=pass scenarios=1/1 -->"}}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil || res.Merged {
		t.Fatalf("Result = %+v, %v; want a stale-scenario pause", res, err)
	}
	if len(fake.MergeCalls) != 0 {
		t.Errorf("merged with a scenario recorded for another head")
	}
}

func TestRun_ScenarioPassMerges(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.PRThread = []ports.Comment{{Body: "<!-- lead-review sha=" + testSHA + " verdict=needs-work scenarios=2/2 -->"}}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Close: true, Timeout: time.Minute, Sleep: noSleep,
	})
	if err != nil || !res.Merged {
		t.Fatalf("Result = %+v, %v; want a merge: the diff review is advisory", res, err)
	}
}
