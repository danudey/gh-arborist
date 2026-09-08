package checks

import (
	"context"
	"fmt"

	"github.com/danudey/gh-arborist/internal/gh"
)

// checkStale flags branches whose tip commit predates the age threshold.
//
// Age alone does not make a branch safe to delete: it may hold work somebody
// still wants. So this check only ever suggests a review.
func checkStale(ctx context.Context, r *Runner, res *Result) error {
	branches, err := r.Branches(ctx)
	if err != nil {
		return err
	}

	cutoff := r.Opts.Age.CutoffFrom(r.Now)
	prsByBranch := map[string][]gh.PullRequest{}
	if r.prsDone {
		prsByBranch = r.prsByBranch
	}

	for _, b := range r.filter.eligible(branches) {
		when := b.Tip.CommittedDate
		if when.IsZero() || !when.Before(cutoff) {
			continue
		}
		res.Add(r.branchFinding(b, prsByBranch[b.Name], Reason{
			Check:    "stale",
			Category: CategoryStale,
			Summary: fmt.Sprintf("no commits for %s (last was %s), which is over the %s threshold",
				humanAge(r.Now, when), onDate(when), r.Opts.Age),
			Short:  fmt.Sprintf("idle over %s", r.Opts.Age),
			Action: ActionReview,
		}))
	}
	return nil
}
