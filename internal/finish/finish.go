package finish

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kuwa72/lead-cli/internal/adapters/git"
	"github.com/kuwa72/lead-cli/internal/issuecheck"
	"github.com/kuwa72/lead-cli/internal/ledger"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/review"
	"github.com/kuwa72/lead-cli/internal/scenario"
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
	Timeout           time.Duration
	PollInterval      time.Duration
	// LedgerPath receives one line per merge attempt (issue #274). Empty
	// disables recording.
	LedgerPath string
	// Sleep waits between polls; defaults to time.Sleep (tests inject no-op).
	Sleep func(time.Duration)
	// Git and WorkDir let the post-merge smoke create a worktree and
	// finish's caller supply the checkout (issue #275).
	Git     workflow.GitRunner
	WorkDir string
	// PostMerge runs the post-merge smoke after a merge (issue #275).
	// Nil disables it.
	PostMerge *PostMergeOptions
}

// Result reports what happened. Pauses (confirm stop, never policy,
// auto-merge fallback, failed scenario) are NOT errors: Message tells the
// next step.
type Result struct {
	ChecksPassed bool
	Merged       bool
	Closed       bool
	Message      string
	// Guarded lists protected files that stopped the merge.
	Guarded []string
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
	if sleep == nil {
		sleep = time.Sleep
	}
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

	// Guardrail (RFC inbox §7): a PR touching protected convention files
	// never auto-merges. Kept after the CI wait so the pause never claims a
	// pass that did not happen; --override-guardrail is the only way past it.
	guarded, err := guardrail(ctx, gh, prNumber)
	if err != nil {
		return Result{}, err
	}

	if err := WaitChecks(ctx, gh, prNumber, opts.timeout(), opts.pollInterval(), opts.sleep); err != nil {
		return Result{}, err
	}

	// Gate: never stops, confirm pauses without --merge, auto checks the
	// repository auto-merge setting and falls back to a manual pointer.
	repo := git.RepoSlug(w.Repository)
	switch {
	case policy == PolicyNever:
		res := Result{ChecksPassed: true,
			Message: fmt.Sprintf("CI passed for PR #%d; merge policy is never, stopping.", prNumber)}
		if opts.Merge {
			return pause(ctx, gh, issue, res)
		}
		return res, nil
	case policy == PolicyConfirm && !opts.Merge:
		return Result{ChecksPassed: true,
			Message: fmt.Sprintf("CI passed for PR #%d; run `lead finish %d --merge` to merge.", prNumber, issue)}, nil
	case policy == PolicyAuto && !opts.Merge && repo != "":
		allowed, err := gh.RepoAllowsAutoMerge(ctx, repo)
		if err != nil {
			return Result{}, fmt.Errorf("PR #%d: auto-merge check: %w", prNumber, err)
		}
		if !allowed {
			return Result{ChecksPassed: true,
				Message: fmt.Sprintf("CI passed for PR #%d, but the repository disallows auto-merge; run `lead finish %d --merge` to merge manually.", prNumber, issue)}, nil
		}
	}
	if len(guarded) > 0 && !opts.OverrideGuardrail {
		return pause(ctx, gh, issue, Result{ChecksPassed: true, Guarded: guarded,
			Message: fmt.Sprintf("CI passed for PR #%d, but it touches protected files (%s). A human must run `lead finish %d --merge --override-guardrail` to merge.",
				prNumber, strings.Join(guarded, ", "), issue)})
	}

	head, err := gh.PrHeadSHA(ctx, prNumber)
	if err != nil {
		return Result{ChecksPassed: true}, fmt.Errorf("PR #%d: head SHA (merge): %w", prNumber, err)
	}
	// Scenario gate (issues #277/#278): a configured scenario must have a
	// passing score for this head. A harness failure is not 0/0; it is a
	// pause. A scenario file with no review result is also a pause, so a
	// forgotten `lead review` cannot merge without semantic verification.
	scenarioFile := scenario.Path(filepath.Dir(store.Path), git.RepoSlug(w.Repository), issue)
	_, statErr := os.Stat(scenarioFile)
	if statErr != nil && !os.IsNotExist(statErr) {
		return Result{ChecksPassed: true}, fmt.Errorf("PR #%d: scenario file %s: %w", prNumber, scenarioFile, statErr)
	}
	hasScenario := statErr == nil
	// A body with a scenario section is a promise that verification exists.
	// Without a stored scenario the old 0/0 path would merge it unverified
	// (issue #295); fail closed instead.
	bodyHasScenario := false
	if !hasScenario {
		iss, err := gh.View(ctx, issue)
		if err != nil {
			return Result{ChecksPassed: true}, fmt.Errorf("issue #%d: read body (scenario gate): %w", issue, err)
		}
		_, bodyHasScenario = issuecheck.ScenarioSection(iss.Body)
	}
	comments, err := gh.PrComments(ctx, prNumber)
	if err != nil {
		return Result{ChecksPassed: true}, fmt.Errorf("PR #%d: comments (scenario gate): %w", prNumber, err)
	}
	login, _ := gh.ApiUser(ctx)
	marker, found := review.LatestMarkerFor(comments, login)
	switch {
	case found && marker.Scenarios == "error":
		return pause(ctx, gh, issue, Result{ChecksPassed: true,
			Message: fmt.Sprintf("CI passed for PR #%d, but the scenario harness failed; re-run `lead review %d` after fixing it.", prNumber, prNumber)})
	case found && marker.Scenarios != "" && marker.Scenarios != "0/0":
		if !strings.EqualFold(marker.SHA, head) {
			return pause(ctx, gh, issue, Result{ChecksPassed: true,
				Message: fmt.Sprintf("CI passed for PR #%d, but the scenario result is for %s and the head is now %s; re-review required: run `lead review %d` again.",
					prNumber, shortSHA(marker.SHA), shortSHA(head), prNumber)})
		}
		passed, total, ok := review.ParseScenarioScore([]byte("SCENARIOS: " + marker.Scenarios))
		if !ok {
			return pause(ctx, gh, issue, Result{ChecksPassed: true,
				Message: fmt.Sprintf("CI passed for PR #%d, but the scenario score %q is unreadable; run `lead review %d` again.",
					prNumber, marker.Scenarios, prNumber)})
		}
		if total > 0 && passed < total {
			return pause(ctx, gh, issue, Result{ChecksPassed: true,
				Message: fmt.Sprintf("CI passed for PR #%d, but generated scenarios are %d/%d; fix the failing steps and re-run `lead review %d`.",
					prNumber, passed, total, prNumber)})
		}
	case hasScenario:
		return pause(ctx, gh, issue, Result{ChecksPassed: true,
			Message: fmt.Sprintf("CI passed for PR #%d, but this issue has a scenario with no passing review result; run `lead review %d` first.", prNumber, prNumber)})
	case bodyHasScenario:
		return pause(ctx, gh, issue, Result{ChecksPassed: true,
			Message: fmt.Sprintf("CI passed for PR #%d, but the issue has a `## シナリオ` and no stored scenario; run `lead scenario add %d` first (or `lead ready %d` for a valid body).", prNumber, issue, issue)})
	}
	if err := gh.PrMerge(ctx, prNumber, head); err != nil {
		return Result{ChecksPassed: true}, fmt.Errorf("PR #%d: merge: %w", prNumber, err)
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

// pause reports a clean stop (exit 0) and leaves the reason on the issue
// so the next reader sees why the merge did not happen (issue #278).
func pause(ctx context.Context, gh ports.GhClient, issue int, res Result) (Result, error) {
	if res.Message != "" {
		if err := gh.IssueComment(ctx, issue, res.Message); err != nil {
			return res, fmt.Errorf("issue #%d: pause comment: %w", issue, err)
		}
	}
	return res, nil
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
