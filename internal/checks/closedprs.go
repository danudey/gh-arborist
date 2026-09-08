package checks

import (
	"context"
	"fmt"

	"github.com/danudey/gh-arborist/internal/gh"
)

// checkClosedPRs flags branches whose pull requests have all been resolved.
//
// A branch with a merged pull request is safe to delete even when the merge was
// a squash or rebase, which is why this check exists alongside the ancestry
// check: squashing rewrites the commits, so a squash-merged branch still looks
// unmerged to Git.
func checkClosedPRs(ctx context.Context, r *Runner, res *Result) error {
	branches, err := r.Branches(ctx)
	if err != nil {
		return err
	}
	prsByBranch, err := r.PullRequestsByBranch(ctx)
	if err != nil {
		return err
	}

	for _, b := range r.filter.eligible(branches) {
		prs := r.ownPullRequests(prsByBranch[b.Name])
		if len(prs) == 0 {
			continue
		}

		var merged, closed []gh.PullRequest
		open := false
		for _, pr := range prs {
			switch pr.State {
			case "OPEN":
				open = true
			case "MERGED":
				merged = append(merged, pr)
			case "CLOSED":
				closed = append(closed, pr)
			}
		}
		// An open pull request still needs the branch.
		if open {
			continue
		}

		var reason Reason
		switch {
		case len(merged) > 0:
			latest := newestPR(merged)
			summary := fmt.Sprintf("pull request %s was merged on %s", latest.Ref(), onDate(latest.ResolvedAt()))
			short := latest.Ref() + " merged"
			if len(merged) > 1 {
				summary += fmt.Sprintf(" (%d merged pull requests came from this branch)", len(merged))
				short += fmt.Sprintf(" (+%d)", len(merged)-1)
			}
			reason = Reason{Check: "closed-prs", Category: CategoryPRMerged, Summary: summary, Short: short, Action: ActionDelete}
		case len(closed) > 0 && !r.Opts.MergedOnly:
			latest := newestPR(closed)
			reason = Reason{
				Check:    "closed-prs",
				Category: CategoryPRClosed,
				// Closed without merging means the work was rejected or
				// abandoned, so the branch is a candidate but not a certainty.
				Summary: fmt.Sprintf("pull request %s was closed without merging on %s", latest.Ref(), onDate(latest.ResolvedAt())),
				Short:   latest.Ref() + " closed unmerged",
				Action:  ActionReview,
			}
		default:
			continue
		}

		res.Add(r.branchFinding(b, prsByBranch[b.Name], reason))
	}
	return nil
}

func newestPR(prs []gh.PullRequest) gh.PullRequest {
	newest := prs[0]
	for _, pr := range prs[1:] {
		if pr.ResolvedAt().After(newest.ResolvedAt()) {
			newest = pr
		}
	}
	return newest
}
