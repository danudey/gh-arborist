package checks

import (
	"context"

	"github.com/danudey/gh-arborist/internal/gh"
)

// Source supplies the repository data the checks read. Every method is
// read-only. gh.Client is the real implementation; tests substitute a fake.
type Source interface {
	// Branches lists every branch with its tip commit.
	Branches(ctx context.Context) ([]gh.Branch, error)
	// PullRequestsForBranches maps branch names to the pull requests opened
	// from a head branch of that name, in any state.
	PullRequestsForBranches(ctx context.Context, names []string) (map[string][]gh.PullRequest, error)
	// ContainedIn compares each head branch against base. A head whose commits
	// are not all reachable from base may be left out of the result, since
	// nothing needs to know how far ahead it is.
	ContainedIn(ctx context.Context, base string, heads []string) (map[string]gh.Comparison, error)
	// Collaborators lists every account with access to the repository, keyed by
	// lowercased login.
	Collaborators(ctx context.Context) (map[string]gh.Actor, error)
	// PullRequests lists pull requests in the given states.
	PullRequests(ctx context.Context, states []string, limit int) ([]gh.PullRequest, error)
	// Issues lists issues in the given states.
	Issues(ctx context.Context, states []string, limit int) ([]gh.Issue, error)
}

var _ Source = (*gh.Client)(nil)
