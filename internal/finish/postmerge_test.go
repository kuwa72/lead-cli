package finish

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ledger"
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

func (f *fakePostMergeGit) RepoRoot(dir string) (string, error) { return f.root, nil }
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
	if len(g.pushed) != 1 || g.pushed[0] != "origin HEAD:main" {
		t.Errorf("pushed = %v, want [origin HEAD:main]", g.pushed)
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
		t.Errorf("incident body claims manual recovery although the revert was pushed: %q", inc.Body)
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

func TestPostMerge_NoMergeCommitSkips(t *testing.T) {
	fake := baseFake()
	g := &fakePostMergeGit{root: t.TempDir()}
	res, err := PostMerge(context.Background(), fake, PostMergeOptions{
		Issue: 36, PR: 7, WorkDir: "/repo", Git: g,
		Smoke: func(ctx context.Context, dir string) error {
			t.Error("smoke ran without a merge commit")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("PostMerge: %v", err)
	}
	if !res.Skipped || res.SmokeFailed {
		t.Fatalf("Result = %+v, want skipped", res)
	}
	if len(g.added) != 0 || len(fake.Created) != 0 {
		t.Errorf("worktree=%v created=%+v, want none when skipping", g.added, fake.Created)
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
