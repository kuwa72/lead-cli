package finish

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

// Defaults mirror bin/ci-wait (1800s cap, 10s poll).
const (
	DefaultTimeout      = 30 * time.Minute
	DefaultPollInterval = 10 * time.Second
)

// Options controls one finish run (flags of `lead finish`, RFC §6.3).
type Options struct {
	PR           int // attach/override PR number (0 = from state record)
	Part         string
	Merge        bool
	Close        bool
	NoClose      bool
	Comment      string
	Outcome      string
	MergePolicy  string // CLI flag override (auto|confirm|never)
	RepoPolicy   string // repository setting (reserved; empty = unset)
	Timeout      time.Duration
	PollInterval time.Duration
	// Sleep waits between polls; defaults to time.Sleep (tests inject no-op).
	Sleep func(time.Duration)
}

// Result reports what happened. Pauses (confirm stop, never policy,
// auto-merge fallback) are NOT errors: Message tells the next step.
type Result struct {
	ChecksPassed bool
	Merged       bool
	Closed       bool
	Message      string
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

// WaitChecks polls `gh pr checks` until all checks pass (pass/skipping),
// a check fails/cancels, the context ends, or the timeout hits. Empty
// check lists keep waiting (checks not registered yet, like bin/ci-wait).
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
		var failed, pending []string
		for _, c := range checks {
			switch c.Bucket {
			case "pass", "skipping":
			case "fail", "cancel":
				failed = append(failed, c.Name)
			default:
				pending = append(pending, c.Name)
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("PR #%d: failing checks: %s", pr, strings.Join(failed, ", "))
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

// Run executes finish for one issue: re-verify PR, wait CI, policy-gated
// merge, optional comment/close, and state recording.
func Run(ctx context.Context, gh ports.GhClient, store *state.Store, issue int, opts Options) (Result, error) {
	w, ok, err := store.Get(issue, opts.Part)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("no workflow for #%d; run `lead work %d` first", issue, issue)
	}

	prNumber := opts.PR
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
		// Retry path: merge already done, skip to comment/close.
		return runClosePhase(ctx, gh, store, w, prRef, prNumber, opts, false)
	}
	if info.State != "OPEN" {
		return Result{}, fmt.Errorf("PR #%d: state %q, want OPEN", prNumber, info.State)
	}
	if info.Mergeable == "CONFLICTING" {
		return Result{}, fmt.Errorf("PR #%d: merge conflict; resolve it first (checks will not run)", prNumber)
	}

	if err := WaitChecks(ctx, gh, prNumber, opts.timeout(), opts.pollInterval(), opts.sleep); err != nil {
		return Result{}, err
	}

	// Gate: never stops, confirm pauses without --merge, auto checks the
	// repository auto-merge setting and falls back to a manual pointer.
	switch {
	case policy == PolicyNever:
		return Result{ChecksPassed: true,
			Message: fmt.Sprintf("CI passed for PR #%d; merge policy is never, stopping.", prNumber)}, nil
	case policy == PolicyConfirm && !opts.Merge:
		return Result{ChecksPassed: true,
			Message: fmt.Sprintf("CI passed for PR #%d; run `lead finish %d --merge` to merge.", prNumber, issue)}, nil
	case policy == PolicyAuto && !opts.Merge && repoKnown(w.Repository):
		allowed, err := gh.RepoAllowsAutoMerge(ctx, w.Repository)
		if err != nil {
			return Result{}, fmt.Errorf("PR #%d: auto-merge check: %w", prNumber, err)
		}
		if !allowed {
			msg := fmt.Sprintf("CI passed for PR #%d, but the repository disallows auto-merge; run `lead finish %d --merge` to merge manually.", prNumber, issue)
			return Result{ChecksPassed: true, Message: msg}, nil
		}
	}
	if err := gh.PrMerge(ctx, prNumber); err != nil {
		return Result{ChecksPassed: true}, fmt.Errorf("PR #%d: merge: %w", prNumber, err)
	}
	return runClosePhase(ctx, gh, store, w, prRef, prNumber, opts, true)
}

func repoKnown(repo string) bool {
	return repo != "" && repo != "local"
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
