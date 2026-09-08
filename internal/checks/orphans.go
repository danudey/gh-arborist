package checks

import (
	"context"
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/text"
	"github.com/danudey/gh-arborist/internal/gh"
)

// checkOrphans flags branches, pull requests and issues that belong to accounts
// which no longer have access to the repository, or whose account is gone
// entirely. These are the items that need reassigning to somebody still on the
// team, or closing.
func checkOrphans(ctx context.Context, r *Runner, res *Result) error {
	collabs, err := r.Collaborators(ctx)
	if err != nil {
		return err
	}
	res.Scanned["collaborators"] = len(collabs)

	if !r.Info.IsPrivate {
		res.Note("This repository is public, so people who opened pull requests or issues without ever having repository access are expected. Treat these results as a list to triage, not a list of departures.")
	}

	ignored := map[string]bool{}
	for _, u := range r.Opts.IgnoreUsers {
		ignored[strings.ToLower(strings.TrimPrefix(u, "@"))] = true
	}

	// departed classifies an actor. An absent problem means the actor still has
	// access, or was deliberately ignored.
	departed := func(a gh.Actor) problem {
		if a.Login == "" || ignored[strings.ToLower(a.Login)] {
			return problem{}
		}
		if a.IsBot() && !r.Opts.IncludeBots {
			return problem{}
		}
		if a.Login == "ghost" {
			return problem{found: true, deleted: true, long: "the GitHub account was deleted", short: "account deleted"}
		}
		if _, ok := collabs[strings.ToLower(a.Login)]; ok {
			return problem{}
		}
		return problem{
			found: true,
			long:  fmt.Sprintf("%s no longer has access to this repository", a.Login),
			short: fmt.Sprintf("%s has no access", a.Login),
		}
	}

	// present reports whether an account can still be handed work. Unlike
	// departed it makes no exception for app accounts, since assigning a pull
	// request to a bot does not give it an owner.
	present := func(a gh.Actor) bool {
		if a.Login == "" || a.Login == "ghost" || a.IsBot() {
			return false
		}
		if ignored[strings.ToLower(a.Login)] {
			return true
		}
		_, ok := collabs[strings.ToLower(a.Login)]
		return ok
	}

	if err := orphanedBranches(ctx, r, res, departed); err != nil {
		return err
	}
	if err := orphanedPullRequests(ctx, r, res, departed, present); err != nil {
		return err
	}
	return orphanedIssues(ctx, r, res, departed)
}

// problem describes why an account no longer counts as present.
type problem struct {
	found bool
	// deleted separates a vanished account from one that merely lost access,
	// which are reported as different indicators.
	deleted bool
	long    string
	short   string
}

type classifier func(gh.Actor) problem

// roleCategories pairs the two indicators one role can produce: the account
// lost access, or the account is gone entirely.
type roleCategories struct{ noAccess, deleted Category }

var (
	ownerRole    = roleCategories{CategoryOwnerNoAccess, CategoryOwnerDeleted}
	authorRole   = roleCategories{CategoryAuthorNoAccess, CategoryAuthorDeleted}
	assigneeRole = roleCategories{CategoryAssigneeNoAccess, CategoryAssigneeDeleted}
)

func (rc roleCategories) of(p problem) Category {
	if p.deleted {
		return rc.deleted
	}
	return rc.noAccess
}

func orphanedBranches(ctx context.Context, r *Runner, res *Result, departed classifier) error {
	branches, err := r.Branches(ctx)
	if err != nil {
		return err
	}
	prsByBranch, err := r.PullRequestsByBranch(ctx)
	if err != nil {
		return err
	}

	unidentified := 0
	for _, b := range r.filter.eligible(branches) {
		owner := r.branchOwner(b, prsByBranch[b.Name])
		if owner.Login == "" {
			// The commit's email address is not linked to any GitHub account,
			// which is too weak a signal to flag on its own.
			unidentified++
			continue
		}
		p := departed(owner)
		if !p.found {
			continue
		}
		res.Add(r.branchFinding(b, prsByBranch[b.Name], Reason{
			Check:    "orphans",
			Category: ownerRole.of(p),
			Summary:  fmt.Sprintf("owned by %s, and %s", owner.Describe(), p.long),
			Short:    "owner " + p.short,
			Action:   ActionReview,
		}))
	}

	if unidentified > 0 {
		res.Note(fmt.Sprintf("%d branches have no identifiable owner, because the tip commit's email address is not linked to a GitHub account and the branch has no pull request. They were not flagged.", unidentified))
	}
	return nil
}

func orphanedPullRequests(ctx context.Context, r *Runner, res *Result, departed classifier, present func(gh.Actor) bool) error {
	states := []string{"OPEN"}
	if r.Opts.IncludeClosed {
		states = append(states, "MERGED", "CLOSED")
	}
	prs, err := r.Client.PullRequests(ctx, states, r.Opts.Limit)
	if err != nil {
		return err
	}
	res.Scanned["pullRequests"] = len(prs)

	adopted := 0
	for _, pr := range prs {
		if r.Opts.SkipForks && pr.IsCrossRepository {
			continue
		}
		var reasons []Reason
		if p := departed(pr.Author); p.found {
			// Somebody who still has access is assigned to it, so the work has
			// an owner and the author's departure costs nothing.
			if anyPresent(pr.Assignees, present) {
				adopted++
			} else {
				summary := fmt.Sprintf("opened by %s, and %s", pr.Author.Describe(), p.long)
				if pr.IsCrossRepository {
					summary += " (raised from a fork)"
				}
				reasons = append(reasons, Reason{
					Check:    "orphans",
					Category: authorRole.of(p),
					Summary:  summary,
					Short:    "author " + p.short,
					Action:   ActionReassign,
				})
			}
		}
		reasons = append(reasons, assigneeReasons(pr.Assignees, departed)...)
		if len(reasons) == 0 {
			continue
		}
		res.Add(r.pullRequestFinding(pr, reasons))
	}

	if adopted > 0 {
		res.Note(fmt.Sprintf("Not flagged: %s opened by somebody without access but assigned to somebody who still has access, so the work already has an owner.",
			text.Pluralize(adopted, "pull request")))
	}
	return nil
}

// anyPresent reports whether at least one of the assignees still has access.
func anyPresent(assignees []gh.Actor, present func(gh.Actor) bool) bool {
	for _, a := range assignees {
		if present(a) {
			return true
		}
	}
	return false
}

func orphanedIssues(ctx context.Context, r *Runner, res *Result, departed classifier) error {
	states := []string{"OPEN"}
	if r.Opts.IncludeClosed {
		states = append(states, "CLOSED")
	}
	issues, err := r.Client.Issues(ctx, states, r.Opts.Limit)
	if err != nil {
		return err
	}
	res.Scanned["issues"] = len(issues)

	for _, iss := range issues {
		var reasons []Reason
		if p := departed(iss.Author); p.found {
			reasons = append(reasons, Reason{
				Check:    "orphans",
				Category: authorRole.of(p),
				// An issue's author cannot be changed, so the useful action is
				// to check whether the report still stands.
				Summary: fmt.Sprintf("opened by %s, and %s", iss.Author.Describe(), p.long),
				Short:   "author " + p.short,
				Action:  ActionReview,
			})
		}
		reasons = append(reasons, assigneeReasons(iss.Assignees, departed)...)
		if len(reasons) == 0 {
			continue
		}
		res.Add(r.issueFinding(iss, reasons))
	}
	return nil
}

func assigneeReasons(assignees []gh.Actor, departed classifier) []Reason {
	var out []Reason
	for _, a := range assignees {
		if p := departed(a); p.found {
			out = append(out, Reason{
				Check:    "orphans",
				Category: assigneeRole.of(p),
				Summary:  fmt.Sprintf("assigned to %s, and %s", a.Describe(), p.long),
				Short:    "assignee " + p.short,
				Action:   ActionReassign,
			})
		}
	}
	return out
}
