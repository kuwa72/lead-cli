package finish

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ledger"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

// fakePostMergeGit records the worktree/revert/push calls postmerge makes.
type fakePostMergeGit struct {
	root      string
	added     []string
	removed   []string
	reverted  []string
	pushed    []string
	addErr    error
	revertErr error
	pushErr   error
}

func (f *fakePostMergeGit) MainWorktree(dir string) (string, error) { return f.root, nil }
func (f *fakePostMergeGit) EnsureCommit(repoDir, sha string) error {
	return nil
}
func (f *fakePostMergeGit) WorktreeAddDetached(repoDir, path, commit string) error {
	f.added = append(f.added, path+"@"+commit)
	return f.addErr
}
func (f *fakePostMergeGit) WorktreeRemove(repoDir, path string, force bool) error {
	f.removed = append(f.removed, path)
	return nil
}
func (f *fakePostMergeGit) Revert(dir, sha string) error {
	f.reverted = append(f.reverted, sha)
	return f.revertErr
}
func (f *fakePostMergeGit) Push(dir, remote, refspec string) error {
	f.pushed = append(f.pushed, remote+" "+refspec)
	return f.pushErr
}

func TestPostMerge_SuccessSkipsRevertAndIncident(t *testing.T) {
	fake := baseFake()
	fake.PRMergeCommit = testSHA
	g := &fakePostMergeGit{root: t.TempDir()}
	smokeDir := ""

	res, err := PostMerge(context.Background(), fake, PostMergeOptions{
		Issue: 36, PR: 7, WorkDir: "/repo", Git: g,
		Smoke: func(ctx context.Context, dir string) error { smokeDir = dir; return nil },
	})
	if err != nil {
		t.Fatalf("PostMerge: %v", err)
	}
	if !res.Passed || res.SmokeFailed || res.Reverted {
		t.Fatalf("Result = %+v, want passed without revert", res)
	}
	if !strings.HasSuffix(smokeDir, ".worktrees/postmerge-pr7") {
		t.Errorf("smoke dir = %q, want the postmerge worktree", smokeDir)
	}
	if len(g.reverted) != 0 || len(g.pushed) != 0 {
		t.Errorf("revert=%v push=%v, want none on success", g.reverted, g.pushed)
	}
	if len(fake.Created) != 0 {
		t.Errorf("incident created on success: %+v", fake.Created)
	}
}

func TestPostMerge_FailureRevertsPushesAndFilesIncident(t *testing.T) {
	fake := baseFake()
	fake.PRMergeCommit = testSHA
	g := &fakePostMergeGit{root: t.TempDir()}

	res, err := PostMerge(context.Background(), fake, PostMergeOptions{
		Issue: 36, PR: 7, WorkDir: "/repo", Git: g,
		Smoke: func(ctx context.Context, dir string) error { return errors.New("build broke") },
	})
	if err == nil {
		t.Fatal("PostMerge succeeded, want the smoke failure to surface")
	}
	if !res.SmokeFailed || !res.Reverted || res.RevertOf != testSHA {
		t.Fatalf("Result = %+v, want smoke-failed + revert of %s", res, testSHA)
	}
	if len(g.reverted) != 1 || g.reverted[0] != testSHA {
		t.Errorf("reverted = %v, want [%s]", g.reverted, testSHA)
	}
	if len(g.pushed) != 1 || !strings.HasPrefix(g.pushed[0], "origin HEAD:refs/heads/revert/pr-7-") {
		t.Errorf("pushed = %v, want a revert branch", g.pushed)
	}
	if len(fake.PRCreates) != 1 || fake.PRCreates[0].Base != "main" {
		t.Fatalf("pr creates = %+v, want one revert PR against main", fake.PRCreates)
	}
	if fake.PRCreates[0].Head != strings.TrimPrefix(g.pushed[0], "origin HEAD:refs/heads/") {
		t.Errorf("PR head = %q, want the pushed branch", fake.PRCreates[0].Head)
	}
	if len(fake.AutoMergeCalls) != 1 || fake.AutoMergeCalls[0] != 201 {
		t.Errorf("auto merges = %v, want [201]", fake.AutoMergeCalls)
	}
	if len(fake.Created) != 1 {
		t.Fatalf("created = %+v, want one incident issue", fake.Created)
	}
	inc := fake.Created[0]
	if !strings.Contains(inc.Title, "P0") || !strings.Contains(inc.Title, "7") {
		t.Errorf("incident title = %q, want P0 and PR 7", inc.Title)
	}
	if len(inc.Labels) != 1 || inc.Labels[0] != IncidentLabel {
		t.Errorf("incident labels = %v, want [%s]", inc.Labels, IncidentLabel)
	}
	if !strings.Contains(inc.Body, testSHA) || !strings.Contains(inc.Body, "build broke") {
		t.Errorf("incident body = %q, want merge SHA and the smoke error", inc.Body)
	}
	if strings.Contains(inc.Body, "手動復旧") {
		t.Errorf("incident body claims manual recovery although the revert PR was opened: %q", inc.Body)
	}
	if !strings.Contains(inc.Body, "auto-merge") {
		t.Errorf("incident body = %q, want the auto-merge action", inc.Body)
	}
	// The reverted change must not be re-dispatched, and the issue keeps a
	// pointer to the incident.
	if len(fake.AddedLabels) != 1 || fake.AddedLabels[0].Label != "blocked" {
		t.Errorf("added labels = %+v, want the blocked label", fake.AddedLabels)
	}
	if len(fake.Comments) != 1 || !strings.Contains(fake.Comments[0].Body, "revert") {
		t.Errorf("issue comments = %+v, want a revert note", fake.Comments)
	}
}

func TestPostMerge_RevertFailureFilesManualRecoveryIncident(t *testing.T) {
	fake := baseFake()
	fake.PRMergeCommit = testSHA
	g := &fakePostMergeGit{root: t.TempDir(), revertErr: errors.New("conflict")}

	res, err := PostMerge(context.Background(), fake, PostMergeOptions{
		Issue: 36, PR: 7, WorkDir: "/repo", Git: g,
		Smoke: func(ctx context.Context, dir string) error { return errors.New("build broke") },
	})
	if err == nil {
		t.Fatal("PostMerge succeeded, want revert failure to surface")
	}
	if res.Reverted {
		t.Errorf("Result = %+v, want Reverted=false", res)
	}
	if len(g.pushed) != 0 {
		t.Errorf("pushed = %v, want no push after a failed revert", g.pushed)
	}
	if len(fake.Created) != 1 {
		t.Fatalf("created = %+v, want one incident issue", fake.Created)
	}
	if body := fake.Created[0].Body; !strings.Contains(body, "手動復旧") {
		t.Errorf("incident body = %q, want the manual-recovery warning", body)
	}
}

func TestPostMerge_NoMergeCommitFails(t *testing.T) {
	fake := baseFake()
	g := &fakePostMergeGit{root: t.TempDir()}
	res, err := PostMerge(context.Background(), fake, PostMergeOptions{
		Issue: 36, PR: 7, WorkDir: "/repo", Git: g,
		Smoke: func(ctx context.Context, dir string) error {
			t.Error("smoke ran without a merge commit")
			return nil
		},
	})
	if err == nil {
		t.Fatalf("PostMerge = %+v, want an error when the merge commit is missing", res)
	}
	if len(g.added) != 0 || len(fake.Created) != 0 {
		t.Errorf("worktree=%v created=%+v, want none", g.added, fake.Created)
	}
}

// A protected main can refuse auto-merge too: the revert PR still exists,
// so the incident must tell a human to merge it rather than claim success.
func TestPostMerge_AutoMergeFailureNeedsHuman(t *testing.T) {
	fake := baseFake()
	fake.PRMergeCommit = testSHA
	fake.PRAutoMergeErr = errors.New("auto-merge disabled")
	g := &fakePostMergeGit{root: t.TempDir()}

	res, err := PostMerge(context.Background(), fake, PostMergeOptions{
		Issue: 36, PR: 7, WorkDir: "/repo", Git: g,
		Smoke: func(ctx context.Context, dir string) error { return errors.New("build broke") },
	})
	if err == nil {
		t.Fatal("PostMerge succeeded, want the auto-merge failure")
	}
	if res.Reverted || res.RevertPR != 201 {
		t.Fatalf("Result = %+v, want an open revert PR that is not merged", res)
	}
	if len(fake.Created) != 1 || !strings.Contains(fake.Created[0].Body, "手動でマージ") {
		t.Errorf("created = %+v, want the manual-merge incident", fake.Created)
	}
}

func TestRun_BlockedRetryDoesNotClose(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	seedWithPR(t, path, state.Workflow{Status: state.StatusBlocked, PostMergeFailed: true, PostMergeIncident: 77})
	fake := baseFake()
	fake.PR = ports.PRInfo{Number: 7, State: "MERGED"}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep,
		PostMerge: &PostMergeOptions{Smoke: func(context.Context, string) error { return nil }},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Closed || len(fake.Closed) != 0 {
		t.Fatalf("closed a reverted change on retry: result=%+v closed=%v", res, fake.Closed)
	}
	if !res.PostMergeFailed || !strings.Contains(res.Message, "incident #77") {
		t.Errorf("Result = %+v, want the postmerge-failed pause naming incident #77", res)
	}
}

// The real smoke command must run against this repository at least once;
// every other test substitutes it (issue #275 review F5).
func TestDefaultSmoke_OnThisRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI skeleton")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := DefaultSmoke(ctx, "../.."); err != nil {
		t.Fatalf("DefaultSmoke on the repository: %v", err)
	}
}

func TestDefaultSmoke_FailsOutsideAModule(t *testing.T) {
	err := DefaultSmoke(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("DefaultSmoke passed in an empty directory, want a build failure")
	}
	if !strings.Contains(err.Error(), "go build") {
		t.Errorf("error = %v, want the failing command named", err)
	}
}

func TestRun_PostMergeFailureRevertsAndDoesNotClose(t *testing.T) {
	path := t.TempDir() + "/wf.json"
	ledgerPath := t.TempDir() + "/runs.jsonl"
	seedWithPR(t, path, state.Workflow{})
	fake := baseFake()
	fake.PRMergeCommit = testSHA
	g := &fakePostMergeGit{root: t.TempDir()}

	res, err := Run(context.Background(), fake, &state.Store{Path: path}, 36, Options{
		Merge: true, Timeout: time.Minute, Sleep: noSleep, LedgerPath: ledgerPath,
		PostMerge: &PostMergeOptions{
			Git:   g,
			Smoke: func(ctx context.Context, dir string) error { return errors.New("build broke") },
		},
	})
	if err == nil {
		t.Fatal("Run succeeded, want the postmerge failure to surface")
	}
	if !res.Merged || !res.PostMergeFailed || res.Closed {
		t.Fatalf("Result = %+v, want merged + postmerge-failed + open", res)
	}
	if len(fake.Closed) != 0 {
		t.Errorf("closed = %v, want the issue left open after the revert", fake.Closed)
	}
	if len(fake.Created) != 1 {
		t.Fatalf("created = %+v, want one incident issue", fake.Created)
	}
	got := loadOne(t, path)
	if got.Status != state.StatusBlocked {
		t.Errorf("recorded status = %q, want blocked until a human resolves the incident", got.Status)
	}
	recs, err := ledger.Read(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Outcome != ledger.OutcomePostMergeFailed || recs[0].RevertOf != testSHA {
		t.Fatalf("ledger = %+v, want one postmerge-failed record reverting %s", recs, testSHA)
	}
}
