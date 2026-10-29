package finish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/ledger"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/review"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/workflow"
)

// Defaults mirror bin/ci-wait (1800s cap, 10s poll).
const (
	DefaultTimeout      = 30 * time.Minute
	DefaultPollInterval = 10 * time.Second
)

// Options controls one finish run (flags of `lead finish`, RFC §6.3).
type Options struct {
	PR          int // attach/override PR number (0 = from state record)
	Part        string
	Merge       bool
	Close       bool
	NoClose     bool
	Comment     string
	Outcome     string
	MergePolicy string // CLI flag override (auto|confirm|never)
	RepoPolicy  string // repository setting (reserved; empty = unset)
	// OverrideGuardrail is the human-only confirmation that lifts the
	// protected-file guardrail (issue #230). Merge alone does not: the
	// headless completion definition merges with --merge.
	OverrideGuardrail bool
	// Approve is the human-only confirmation that merges a risk:b issue
	// (issue #245). Merge alone does not: unlabelled and risk:b issues
	// stop for human approval by default. Unrelated to OverrideGuardrail.
	Approve bool
	// NoReview is the human-only confirmation that lets a risk:a PR merge
	// without the independent reviewer's pass (issue #246). It requires
	// Approve as well (issue #249), so an unattended agent cannot lift the
	// review gate alone. It is never part of the headless dispatch prompt.
	NoReview     bool
	Timeout      time.Duration
	PollInterval time.Duration
	// LedgerPath receives one line per merge attempt (issue #274). Empty
	// disables recording.
	LedgerPath string
	// Git and WorkDir let finish remove the issue's worktree when it
	// defers the merge to a human (issue #245). Nil Git skips cleanup.
	Git     workflow.GitRunner
	WorkDir string
	// Sleep waits between polls; defaults to time.Sleep (tests inject no-op).
	Sleep func(time.Duration)
	// PostMerge runs the post-merge smoke after a merge (issue #275).
	// Nil disables it.
	PostMerge *PostMergeOptions
}

// Result reports what happened. Pauses (confirm stop, never policy,
// auto-merge fallback) are NOT errors: Message tells the next step.
type Result struct {
	ChecksPassed bool
	Merged       bool
	Closed       bool
	Message      string
	// Guarded lists protected files that lowered the policy to confirm.
	Guarded []string
	// AwaitingHuman is true when CI passed but the risk gate deferred the
	// merge to a human (issue #245). Not an error.
	AwaitingHuman bool
	// PostMergeFailed is true when the smoke failed and main was reverted
	// (issue #275). The issue stays open, blocked on the incident.
	PostMergeFailed bool
	// RevertOf is the merge commit the post-merge revert undid.
	RevertOf string
}

func (o *Options) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

func (o *Options) pollInterval() time.Duration {
	if o.PollInterval > 0 {
		return o.PollInterval
	}
	return DefaultPollInterval
}

func (o *Options) sleep(d time.Duration) {
	if o.Sleep != nil {
		o.Sleep(d)
		return
	}
	time.Sleep(d)
}

// WaitChecks polls `gh pr checks` until all checks pass, a check
// fails/cancels/skips, the context ends, or the timeout hits. A skipped
// check is not a pass: GitHub treats skipped and neutral as mergeable, so
// a path-filtered job could otherwise let an untested PR through
// (issue #273). Empty check lists keep waiting (checks not registered yet,
// like bin/ci-wait).
func WaitChecks(ctx context.Context, gh ports.GhClient, pr int, timeout, interval time.Duration, sleep func(time.Duration)) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("PR #%d: waiting interrupted: %w", pr, err)
		}
		checks, err := gh.PrChecks(ctx, pr)
		if err != nil {
			return fmt.Errorf("PR #%d: checks: %w", pr, err)
		}
		var failed, skipped, pending []string
		for _, c := range checks {
			switch c.Bucket {
			case "pass":
			case "skipping":
				skipped = append(skipped, c.Name)
			case "fail", "cancel":
				failed = append(failed, c.Name)
			default:
				pending = append(pending, c.Name)
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("PR #%d: failing checks: %s", pr, strings.Join(failed, ", "))
		}
		if len(skipped) > 0 {
			return fmt.Errorf("PR #%d: skipped checks are not a pass: %s", pr, strings.Join(skipped, ", "))
		}
		if len(checks) > 0 && len(pending) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("PR #%d: timeout waiting for checks (%s)", pr, timeout)
		}
		sleep(interval)
	}
}

// mergeOutcome maps a finished merge attempt to a ledger outcome.
func mergeOutcome(res Result, err error, opts Options) string {
	switch {
	case res.PostMergeFailed:
		return ledger.OutcomePostMergeFailed
	case err != nil:
		return ledger.OutcomeError
	case res.Merged, res.Closed:
		// Already-merged retries close the issue without merging again;
		// they are still completed runs, not pauses.
		return ledger.OutcomeCompleted
	case res.AwaitingHuman:
		return ledger.OutcomeAwaitingHuman
	case len(res.Guarded) > 0:
		return ledger.OutcomeGuardrail
	case opts.Outcome == "blocked":
		return ledger.OutcomeBlocked
	case opts.Outcome != "":
		return ledger.OutcomeCompleted
	default:
		return ledger.OutcomePaused
	}
}

// Run executes finish for one issue: re-verify PR, wait CI, policy-gated
// merge, optional comment/close, and state recording.
func Run(ctx context.Context, gh ports.GhClient, store *state.Store, issue int, opts Options) (res Result, err error) {
	if opts.NoReview && !opts.Approve {
		return Result{}, errors.New("finish: --no-review requires --approve (both are human-only confirmations)")
	}
	w, ok, err := store.Get(issue, opts.Part)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("no workflow for #%d; run `lead run %d` first", issue, issue)
	}

	// Record only attempts that reached a real workflow: argument errors
	// and unknown issues must not add noise to the history.
	started := time.Now()
	prNumber := opts.PR
	defer func() {
		if opts.LedgerPath == "" {
			return
		}
		repo := git.RepoSlug(w.Repository)
		if repo == "" {
			repo = w.Repository
		}
		revertOf := res.RevertOf
		if revertOf == "" {
			revertOf = w.PostMergeRevertOf
		}
		if aerr := ledger.Append(opts.LedgerPath, ledger.Record{
			Repo: repo, Issue: issue, Phase: ledger.PhaseMerge, Mode: w.Mode, PR: prNumber,
			StartedAt: started, EndedAt: time.Now(),
			Outcome:  mergeOutcome(res, err, opts),
			RevertOf: revertOf,
		}); aerr != nil {
			fmt.Fprintf(os.Stderr, "finish: ledger: %v\n", aerr)
		}
	}()

	var prRef *state.PRRef
	if prNumber != 0 {
		for i := range w.PullRequests {
			if w.PullRequests[i].Number == prNumber {
				prRef = &w.PullRequests[i]
				break
			}
		}
	} else if len(w.PullRequests) > 0 {
		prRef = &w.PullRequests[0]
		prNumber = prRef.Number
	}

	// Discovery (issue #230): a headless `lead finish <issue> --merge`
	// runs against a dispatch record that never learned the PR number.
	// Look it up from the recorded branch before asking for --pr.
	if prNumber == 0 && w.Branch != "" {
		if n, err := gh.PrNumberForBranch(ctx, w.Branch); err == nil && n > 0 {
			prNumber = n
		}
	}

	// No-PR path (research/docs without PR): require an explicit outcome.
	if prNumber == 0 {
		if opts.Outcome == "" {
			return Result{}, fmt.Errorf("no PR recorded for #%d; pass --outcome <merged|researched|documented|blocked> (research/docs) or attach one via --pr", issue)
		}
		return runNoPR(ctx, gh, store, w, opts)
	}

	policy := Resolve(w.Mode, w.MergePolicy, opts.RepoPolicy, opts.MergePolicy)

	info, err := gh.PrInfo(ctx, prNumber)
	if err != nil {
		return Result{}, fmt.Errorf("PR #%d: %w", prNumber, err)
	}
	if info.State == "MERGED" {
		// Retry path: merge already done, skip to comment/close. A
		// postmerge failure left the record blocked; closing here would
		// report the reverted change as done (issue #275).
		if w.Status == state.StatusBlocked && w.PostMergeFailed {
			msg := fmt.Sprintf("PR #%d is merged, but the post-merge smoke failed and the merge was reverted; resolve the incident before re-running the work.", prNumber)
			if w.PostMergeIncident > 0 {
				msg = fmt.Sprintf("PR #%d is merged, but the post-merge smoke failed and the merge was reverted; resolve incident #%d before re-running the work.", prNumber, w.PostMergeIncident)
			}
			return Result{ChecksPassed: true, Merged: true, PostMergeFailed: true, Message: msg}, nil
		}
		return runClosePhase(ctx, gh, store, w, prRef, prNumber, opts, false)
	}
	if info.State != "OPEN" {
		return Result{}, fmt.Errorf("PR #%d: state %q, want OPEN", prNumber, info.State)
	}
	if info.Mergeable == "CONFLICTING" {
		return Result{}, fmt.Errorf("PR #%d: merge conflict; resolve it first (checks will not run)", prNumber)
	}

	// Risk classification (issue #245): labels come from the issue, not the
	// PR. Fail closed — if the lookup fails, we cannot tell a safe change
	// from a risky one, so stop before merging.
	iss, err := gh.View(ctx, issue)
	if err != nil {
		return Result{}, fmt.Errorf("issue #%d: view labels (risk gate): %w", issue, err)
	}
	risk := RiskClass(iss.Labels)
	hadAwaiting := slices.Contains(iss.Labels, state.AwaitingHumanLabel)

	// Guardrail (RFC inbox §7): a PR touching protected convention files
	// never auto-merges. Lower auto → confirm and persist the reason so the
	// inbox can show why this issue stopped. Checked before the CI wait so a
	// gh failure surfaces early and the record is written even if the wait
	// is interrupted.
	guarded, err := guardrail(ctx, gh, prNumber)
	if err != nil {
		return Result{}, err
	}
	if len(guarded) > 0 && policy == PolicyAuto {
		policy = PolicyConfirm
		w.MergePolicy = string(PolicyConfirm)
		w.PolicyReason = fmt.Sprintf("guardrail: PR #%d touches protected files: %s", prNumber, strings.Join(guarded, ", "))
		if err := store.Upsert(w); err != nil {
			return Result{}, err
		}
	}

	if err := WaitChecks(ctx, gh, prNumber, opts.timeout(), opts.pollInterval(), opts.sleep); err != nil {
		return Result{}, err
	}

	// Gate: never stops, the protected-file guardrail stops unless a human
	// passed --override-guardrail (--merge is not enough: the headless
	// completion definition uses it, issue #230), confirm pauses without
	// --merge, auto checks the repository auto-merge setting and falls back
	// to a manual pointer.
	repo := git.RepoSlug(w.Repository)
	switch {
	case policy == PolicyNever:
		return Result{ChecksPassed: true,
			Message: fmt.Sprintf("CI passed for PR #%d; merge policy is never, stopping.", prNumber)}, nil
	case len(guarded) > 0 && !opts.OverrideGuardrail:
		return Result{ChecksPassed: true, Guarded: guarded,
			Message: fmt.Sprintf("CI passed for PR #%d, but it touches protected files (%s); merge policy lowered to confirm. Review the PR, then run `lead finish %d --merge --override-guardrail` to merge.",
				prNumber, strings.Join(guarded, ", "), issue)}, nil
	case policy == PolicyConfirm && !opts.Merge:
		return Result{ChecksPassed: true,
			Message: fmt.Sprintf("CI passed for PR #%d; run `lead finish %d --merge` to merge.", prNumber, issue)}, nil
	case policy == PolicyAuto && !opts.Merge && repo != "":
		allowed, err := gh.RepoAllowsAutoMerge(ctx, repo)
		if err != nil {
			return Result{}, fmt.Errorf("PR #%d: auto-merge check: %w", prNumber, err)
		}
		if !allowed {
			msg := fmt.Sprintf("CI passed for PR #%d, but the repository disallows auto-merge; run `lead finish %d --merge` to merge manually.", prNumber, issue)
			return Result{ChecksPassed: true, Message: msg}, nil
		}
	}
	// Risk gate (issue #245): only risk:a merges unattended. Unlabelled
	// and risk:b stop here as awaiting-human — a clean pause, not a
	// failure. The human merges with --approve.
	if risk != RiskA && !opts.Approve {
		return runAwaitingHuman(ctx, gh, store, w, prNumber, risk, opts)
	}
	// Independent review gate (issue #246): an unattended merge of a
	// risk:a PR also needs the separate reviewer's verdict. Since issue
	// #249 that verdict is a PR comment marker bound to the head SHA the
	// reviewer examined: the label alone is display only. A missing or
	// stale marker stops before merging; --no-review --approve is the
	// human-only override. Issue #250 pins the merge to the same head
	// (--match-head-commit) so a push between the check and the merge
	// cannot slip in.
	mergeHead := ""
	if risk == RiskA && !opts.NoReview {
		head, err := gh.PrHeadSHA(ctx, prNumber)
		if err != nil {
			return Result{ChecksPassed: true}, fmt.Errorf("PR #%d: head SHA (review gate): %w", prNumber, err)
		}
		mergeHead = head
		comments, err := gh.PrComments(ctx, prNumber)
		if err != nil {
			return Result{ChecksPassed: true}, fmt.Errorf("PR #%d: comments (review gate): %w", prNumber, err)
		}
		// Best effort author check (#250): markers from another gh user
		// are ignored when the login is known. The implementation session
		// usually shares the account, so this is defense in depth, not the
		// primary gate.
		login, _ := gh.ApiUser(ctx)
		marker, found := review.LatestMarkerFor(comments, login)
		switch {
		case !found:
			return Result{ChecksPassed: true,
				Message: fmt.Sprintf("CI passed for PR #%d, but it has no review pass for the current head. Run `lead review %d` and merge after it passes; a human can override with `lead finish %d --merge --approve --no-review`.",
					prNumber, prNumber, issue)}, nil
		case marker.Verdict != review.VerdictPass:
			return Result{ChecksPassed: true,
				Message: fmt.Sprintf("CI passed for PR #%d, but the last review verdict is %s. Address the findings, then run `lead review %d` again.",
					prNumber, marker.Verdict, prNumber)}, nil
		case !strings.EqualFold(marker.SHA, head):
			return Result{ChecksPassed: true,
				Message: fmt.Sprintf("CI passed for PR #%d, but the review passed %s and the head is now %s; re-review required: run `lead review %d`.",
					prNumber, shortSHA(marker.SHA), shortSHA(head), prNumber)}, nil
		}
	}
	if mergeHead == "" {
		head, err := gh.PrHeadSHA(ctx, prNumber)
		if err != nil {
			return Result{ChecksPassed: true}, fmt.Errorf("PR #%d: head SHA (merge): %w", prNumber, err)
		}
		mergeHead = head
	}
	if err := gh.PrMerge(ctx, prNumber, mergeHead); err != nil {
		return Result{ChecksPassed: true}, fmt.Errorf("PR #%d: merge: %w", prNumber, err)
	}
	if hadAwaiting {
		if err := gh.IssueRemoveLabel(ctx, w.Issue, state.AwaitingHumanLabel); err != nil {
			return Result{ChecksPassed: true, Merged: true},
				fmt.Errorf("PR #%d merged, but removing the %s label failed: %w", prNumber, state.AwaitingHumanLabel, err)
		}
	}
	pmNote := ""
	if opts.PostMerge != nil {
		pm := *opts.PostMerge
		pm.Issue, pm.PR = issue, prNumber
		if pm.WorkDir == "" {
			pm.WorkDir = opts.WorkDir
		}
		if pm.Git == nil {
			pm.Git = opts.Git
		}
		if pm.Repo == "" {
			pm.Repo = repo
		}
		pmRes, pmErr := PostMerge(ctx, gh, pm)
		if pmErr != nil {
			// The PR is merged on GitHub even when main was reverted, so
			// keep that in the record. Only a smoke failure blocks the
			// issue: a setup error (worktree, merge commit) left main
			// untouched and must not claim a revert.
			if pmRes.SmokeFailed {
				w.Status = state.StatusBlocked
				w.PostMergeFailed = true
				w.PostMergeIncident = pmRes.Incident
				w.PostMergeRevertOf = pmRes.RevertOf
			}
			if prRef != nil {
				prRef.Status = "merged"
			} else {
				w.PullRequests = append(w.PullRequests, state.PRRef{Number: prNumber, Part: opts.Part, Status: "merged"})
			}
			if saveErr := store.Upsert(w); saveErr != nil {
				fmt.Fprintf(os.Stderr, "finish: state save: %v\n", saveErr)
			}
			return Result{ChecksPassed: true, Merged: true,
				PostMergeFailed: pmRes.SmokeFailed, RevertOf: pmRes.RevertOf,
				Message: pmRes.Message}, pmErr
		}
		if pmRes.Passed && pmRes.Message != "" {
			pmNote = pmRes.Message
		}
	}
	res, err = runClosePhase(ctx, gh, store, w, prRef, prNumber, opts, true)
	if err == nil && pmNote != "" {
		res.Message += " (" + pmNote + ")"
	}
	return res, err
}

// runAwaitingHuman records that CI passed but the risk class needs a
// human's approval: label the issue, move the workflow to awaiting_review,
// clean the worktree (branch and PR stay), and report a clean pause.
func runAwaitingHuman(ctx context.Context, gh ports.GhClient, store *state.Store, w state.Workflow, prNumber int, risk Risk, opts Options) (Result, error) {
	if err := gh.IssueAddLabel(ctx, w.Issue, state.AwaitingHumanLabel); err != nil {
		return Result{ChecksPassed: true}, fmt.Errorf("issue #%d: add label %s: %w", w.Issue, state.AwaitingHumanLabel, err)
	}
	w.Status = state.StatusAwaitingReview
	w.PID = 0
	msg := fmt.Sprintf("CI passed for PR #%d; issue #%d is risk:%s (unlabelled means risk:b), so it is awaiting human approval. Review the PR, then run `lead finish %d --merge --approve`.",
		prNumber, w.Issue, risk, w.Issue)
	if cleanup := cleanupWorktree(opts, &w); cleanup != "" {
		msg += " (" + cleanup + ")"
	}
	if err := store.Upsert(w); err != nil {
		return Result{ChecksPassed: true}, err
	}
	return Result{ChecksPassed: true, AwaitingHuman: true, Message: msg}, nil
}

// cleanupWorktree removes the issue's worktree for an awaiting-human pause.
// Best effort: a failure is reported in the message but never turns the
// pause into a failed dispatch attempt. The branch and PR are untouched.
func cleanupWorktree(opts Options, w *state.Workflow) string {
	if w.Worktree == "" || opts.Git == nil {
		return ""
	}
	root, err := opts.Git.RepoRoot(opts.WorkDir)
	if err != nil {
		return "worktree cleanup failed: " + err.Error()
	}
	if err := opts.Git.WorktreeRemove(root, w.Worktree, true); err != nil {
		return "worktree cleanup failed: " + err.Error()
	}
	w.Worktree = ""
	return ""
}

// shortSHA abbreviates a commit SHA for pause messages (7 chars).
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// guardrail lists the PR's protected files (see ProtectedPaths).
func guardrail(ctx context.Context, gh ports.GhClient, pr int) ([]string, error) {
	files, err := gh.PrFiles(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("PR #%d: list files (guardrail): %w", pr, err)
	}
	return GuardedFiles(files), nil
}

// runNoPR records a non-PR outcome (research/docs) and optionally closes.
func runNoPR(ctx context.Context, gh ports.GhClient, store *state.Store, w state.Workflow, opts Options) (Result, error) {
	w.Artifacts = append(w.Artifacts, "outcome:"+opts.Outcome)
	if opts.Comment != "" {
		if err := gh.IssueComment(ctx, w.Issue, opts.Comment); err != nil {
			return Result{}, fmt.Errorf("issue #%d: comment: %w", w.Issue, err)
		}
	}
	if opts.Close && !opts.NoClose {
		if err := gh.IssueClose(ctx, w.Issue); err != nil {
			return Result{}, fmt.Errorf("issue #%d: close: %w", w.Issue, err)
		}
		w.Status = state.StatusClosed
	} else {
		w.Status = state.StatusCompleted
	}
	if err := store.Upsert(w); err != nil {
		return Result{}, err
	}
	return Result{ChecksPassed: true, Closed: w.Status == state.StatusClosed,
		Message: fmt.Sprintf("recorded outcome %q for #%d", opts.Outcome, w.Issue)}, nil
}

// runClosePhase records the merge, posts the comment, and closes unless
// suppressed. A failed close keeps the merge (RFC §7.2) and reports retry.
func runClosePhase(ctx context.Context, gh ports.GhClient, store *state.Store, w state.Workflow, prRef *state.PRRef, prNumber int, opts Options, merged bool) (Result, error) {
	res := Result{ChecksPassed: true, Merged: merged}
	if prRef != nil {
		prRef.Status = "merged"
	} else {
		w.PullRequests = append(w.PullRequests, state.PRRef{Number: prNumber, Part: opts.Part, Status: "merged"})
	}
	w.Status = state.StatusCompleted

	if opts.Comment != "" {
		if err := gh.IssueComment(ctx, w.Issue, opts.Comment); err != nil {
			if err := store.Upsert(w); err != nil {
				return res, fmt.Errorf("issue #%d: comment: %v; state save: %w", w.Issue, err, err)
			}
			return res, fmt.Errorf("issue #%d: comment: %w", w.Issue, err)
		}
	}

	closable := opts.Close || (!opts.NoClose && w.Mode == state.ModeImplement)
	if w.Mode == state.ModeSplit && w.Part == "" {
		closable = false // split parent: never auto-close (children pending)
	}
	if closable && !opts.NoClose {
		if err := gh.IssueClose(ctx, w.Issue); err != nil {
			if saveErr := store.Upsert(w); saveErr != nil {
				return res, fmt.Errorf("issue #%d: close: %v; state save: %w", w.Issue, err, saveErr)
			}
			return res, fmt.Errorf("issue #%d: merged, but close failed (%v); retry with `lead finish %d --close`", w.Issue, err, w.Issue)
		}
		res.Closed = true
		w.Status = state.StatusClosed
	}
	if err := store.Upsert(w); err != nil {
		return res, err
	}
	if res.Closed {
		res.Message = fmt.Sprintf("PR #%d merged, issue #%d closed.", prNumber, w.Issue)
	} else {
		res.Message = fmt.Sprintf("PR #%d merged; issue #%d left open.", prNumber, w.Issue)
	}
	return res, nil
}
