package finish

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

// Approve is `lead approve <n>` (issue #255): the human's morning approval
// for an issue the risk gate parked as awaiting-human. It refuses anything
// else, warns when the PR has no review:pass (the human's judgement wins),
// and otherwise runs finish with --merge --approve --close. DryRun reports
// the target PR and review state without waiting, merging, or closing.
func Approve(ctx context.Context, gh ports.GhClient, store *state.Store, issue int, opts Options) (Result, error) {
	iss, err := gh.View(ctx, issue)
	if err != nil {
		return Result{}, fmt.Errorf("issue #%d: view labels: %w", issue, err)
	}
	if !slices.Contains(iss.Labels, state.AwaitingHumanLabel) {
		return Result{}, fmt.Errorf("issue #%d is not awaiting human approval (no %s label); approve only merges issues the risk gate parked",
			issue, state.AwaitingHumanLabel)
	}

	w, ok, err := store.Get(issue, opts.Part)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("no workflow for #%d; run `lead run %d` first", issue, issue)
	}
	prNumber, _ := resolvePR(ctx, gh, w, opts)
	if prNumber == 0 {
		return Result{}, fmt.Errorf("no PR recorded for #%d; attach one with --pr", issue)
	}

	verdict := ""
	if labels, err := gh.PrLabels(ctx, prNumber); err == nil {
		verdict = reviewVerdict(labels)
	}
	warn := ""
	if verdict != state.ReviewPassLabel {
		shown := verdict
		if shown == "" {
			shown = "none"
		}
		warn = fmt.Sprintf("warning: PR #%d has no %s (review verdict: %s); approving on the human's judgement.",
			prNumber, state.ReviewPassLabel, shown)
	}

	if opts.DryRun {
		msg := fmt.Sprintf("Dry run: issue #%d is awaiting human approval; PR #%d review verdict %s. Would wait for CI, squash-merge PR #%d, and close #%d.",
			issue, prNumber, orNone(verdict), prNumber, issue)
		return Result{DryRun: true, Message: joinMessage(warn, msg)}, nil
	}

	opts.Merge, opts.Approve, opts.Close, opts.NoClose = true, true, true, false
	res, err := Run(ctx, gh, store, issue, opts)
	res.Message = joinMessage(warn, res.Message)
	return res, err
}

func reviewVerdict(labels []string) string {
	for _, l := range labels {
		if l == state.ReviewPassLabel || l == state.ReviewNeedsWorkLabel {
			return l
		}
	}
	return ""
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func joinMessage(first, second string) string {
	switch {
	case first == "":
		return second
	case second == "":
		return first
	default:
		return strings.TrimSpace(first + "\n" + second)
	}
}
