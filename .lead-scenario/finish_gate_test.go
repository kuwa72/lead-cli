// Behaviour check for step 2: finish must not merge an issue whose body
// carries `## シナリオ` when no scenario file was stored for it, while a
// stored scenario with a passing review result still merges.
package finishgate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/finish"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/scenario"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

const headSHA = "0123456789abcdef0123456789abcdef01234567"

func seed(t *testing.T, dir string) *state.Store {
	t.Helper()
	store := &state.Store{Path: filepath.Join(dir, "wf.json")}
	err := store.Upsert(state.Workflow{
		Issue:        36,
		Mode:         state.ModeImplement,
		Branch:       "issue/36-x",
		Status:       state.StatusInProgress,
		Repository:   "o/r",
		PullRequests: []state.PRRef{{Number: 7, Status: "open"}},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return store
}

func fakeGh(body string) *testutil.FakeGhClient {
	return &testutil.FakeGhClient{
		Checks:           []ports.PRCheck{{Name: "test", Bucket: "pass", State: "SUCCESS"}},
		PR:               ports.PRInfo{Number: 7, State: "OPEN", Mergeable: "MERGEABLE"},
		AutoMergeAllowed: true,
		Issues: map[int]ports.Issue{36: {
			Number: 36,
			Title:  "x",
			Body:   body,
			Labels: []string{"risk:a"},
		}},
		PRLabels:  []string{state.ReviewPassLabel},
		PRHeadSHA: headSHA,
	}
}

func run(t *testing.T, store *state.Store, fake *testutil.FakeGhClient) finish.Result {
	t.Helper()
	res, err := finish.Run(context.Background(), fake, store, 36, finish.Options{
		Merge: true, Timeout: time.Minute, Sleep: func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("finish.Run: %v", err)
	}
	return res
}

func TestBodyScenarioWithoutStoredFileDoesNotMerge(t *testing.T) {
	dir := t.TempDir()
	store := seed(t, dir)
	fake := fakeGh("# x\n\n## シナリオ\n\n1. `lead ready 36` → ready が付く\n")

	res := run(t, store, fake)
	if res.Merged || len(fake.MergeCalls) != 0 {
		t.Fatalf("merged with a body scenario and no stored scenario file: res=%+v merges=%v", res, fake.MergeCalls)
	}
	if !strings.Contains(strings.ToLower(res.Message), "scenario") {
		t.Errorf("message does not explain the missing scenario: %q", res.Message)
	}
	if _, err := scenario.Load(scenario.Path(dir, "o/r", 36)); err == nil {
		t.Fatalf("finish created a scenario file on its own")
	}
}

func TestStoredScenarioWithPassingReviewMerges(t *testing.T) {
	dir := t.TempDir()
	store := seed(t, dir)
	fake := fakeGh("# x\n\n## シナリオ\n\n1. `lead ready 36` → ready が付く\n")
	fake.PRThread = []ports.Comment{{Body: "<!-- lead-review sha=" + headSHA + " verdict=pass scenarios=1/1 -->"}}
	if err := scenario.Save(scenario.Path(dir, "o/r", 36), "## Steps\n1. `lead ready 36` → ready が付く\n"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	res := run(t, store, fake)
	if !res.Merged || len(fake.MergeCalls) != 1 {
		t.Fatalf("stored scenario with a passing review did not merge: res=%+v merges=%v", res, fake.MergeCalls)
	}
}
