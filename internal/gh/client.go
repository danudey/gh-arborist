// Package gh wraps the GitHub GraphQL API calls that the checks need. Every
// call in here is read-only.
package gh

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
)

// Client reads branches, pull requests, issues and collaborators for one
// repository.
type Client struct {
	gql  *api.GraphQLClient
	repo repository.Repository

	// PageSize is how many nodes to request per page of a paged connection.
	PageSize int
	// PRBatch is how many branches to look up pull requests for per request.
	PRBatch int
	// CompareBatch is how many branches to compare against a base per request.
	CompareBatch int
	// CommitBatch is how many commits to resolve identities for per request.
	CommitBatch int

	// Progress, when set, receives one-line status updates.
	Progress func(format string, args ...any)
}

// NewClient builds a client for repo, authenticating against repo's host.
func NewClient(repo repository.Repository) (*Client, error) {
	gql, err := api.NewGraphQLClient(api.ClientOptions{Host: repo.Host})
	if err != nil {
		return nil, err
	}
	return &Client{
		gql:          gql,
		repo:         repo,
		PageSize:     100,
		PRBatch:      50,
		CompareBatch: 20,
		CommitBatch:  100,
	}, nil
}

// Repo returns the repository this client reads.
func (c *Client) Repo() repository.Repository { return c.repo }

func (c *Client) progress(format string, args ...any) {
	if c.Progress != nil {
		c.Progress(format, args...)
	}
}

func (c *Client) vars(extra map[string]any) map[string]any {
	v := map[string]any{"owner": c.repo.Owner, "name": c.repo.Name}
	for k, val := range extra {
		v[k] = val
	}
	return v
}

// do runs a query, tolerating partial NOT_FOUND errors. Those happen when a
// branch is deleted by somebody else while a scan is in flight; the rest of the
// response is still usable.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	err := c.gql.DoWithContext(ctx, query, vars, out)
	if err == nil {
		return nil
	}
	var gqlErr *api.GraphQLError
	if errors.As(err, &gqlErr) && allNotFound(gqlErr) {
		for _, item := range gqlErr.Errors {
			c.progress("skipping: %s", item.Message)
		}
		return nil
	}
	return err
}

func allNotFound(err *api.GraphQLError) bool {
	if len(err.Errors) == 0 {
		return false
	}
	for _, item := range err.Errors {
		if item.Type != "NOT_FOUND" {
			return false
		}
	}
	return true
}

const actorFields = `
fragment actorFields on Actor { login __typename }
`

const prFields = `
fragment prFields on PullRequest {
  number state title url headRefName isCrossRepository updatedAt mergedAt closedAt
  headRepositoryOwner { login }
  author { ...actorFields }
  assignees(first: 20) { nodes { login __typename } }
}
`

type jsonActor struct {
	Login    string `json:"login"`
	Typename string `json:"__typename"`
}

func (a *jsonActor) actor() Actor {
	if a == nil {
		return Actor{}
	}
	return Actor{Login: a.Login, Type: a.Typename}
}

type jsonPR struct {
	Number              int        `json:"number"`
	State               string     `json:"state"`
	Title               string     `json:"title"`
	URL                 string     `json:"url"`
	HeadRefName         string     `json:"headRefName"`
	IsCrossRepository   bool       `json:"isCrossRepository"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	MergedAt            *time.Time `json:"mergedAt"`
	ClosedAt            *time.Time `json:"closedAt"`
	HeadRepositoryOwner *struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
	Author    *jsonActor `json:"author"`
	Assignees struct {
		Nodes []jsonActor `json:"nodes"`
	} `json:"assignees"`
}

func (p jsonPR) pullRequest() PullRequest {
	pr := PullRequest{
		Number:            p.Number,
		State:             p.State,
		Title:             p.Title,
		URL:               p.URL,
		HeadRefName:       p.HeadRefName,
		IsCrossRepository: p.IsCrossRepository,
		Author:            p.Author.actor(),
		MergedAt:          p.MergedAt,
		ClosedAt:          p.ClosedAt,
		UpdatedAt:         p.UpdatedAt,
	}
	if p.HeadRepositoryOwner != nil {
		pr.HeadOwner = p.HeadRepositoryOwner.Login
	}
	for i := range p.Assignees.Nodes {
		pr.Assignees = append(pr.Assignees, p.Assignees.Nodes[i].actor())
	}
	return pr
}

const infoQuery = `
query ArboristRepoInfo($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    nameWithOwner
    url
    isPrivate
    isArchived
    viewerPermission
    defaultBranchRef { name }
  }
}
`

// Info fetches repository metadata, including the default branch name.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var resp struct {
		Repository *struct {
			NameWithOwner    string `json:"nameWithOwner"`
			URL              string `json:"url"`
			IsPrivate        bool   `json:"isPrivate"`
			IsArchived       bool   `json:"isArchived"`
			ViewerPermission string `json:"viewerPermission"`
			DefaultBranchRef *struct {
				Name string `json:"name"`
			} `json:"defaultBranchRef"`
		} `json:"repository"`
	}
	if err := c.do(ctx, infoQuery, c.vars(nil), &resp); err != nil {
		return Info{}, err
	}
	if resp.Repository == nil {
		return Info{}, fmt.Errorf("repository %s/%s not found, or you do not have access to it", c.repo.Owner, c.repo.Name)
	}
	info := Info{
		NameWithOwner:    resp.Repository.NameWithOwner,
		URL:              resp.Repository.URL,
		IsPrivate:        resp.Repository.IsPrivate,
		IsArchived:       resp.Repository.IsArchived,
		ViewerPermission: resp.Repository.ViewerPermission,
	}
	if resp.Repository.DefaultBranchRef != nil {
		info.DefaultBranch = resp.Repository.DefaultBranchRef.Name
	}
	return info, nil
}

const branchesQuery = `
query ArboristBranches($owner: String!, $name: String!, $limit: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    refs(refPrefix: "refs/heads/", first: $limit, after: $cursor,
         orderBy: {field: ALPHABETICAL, direction: ASC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes {
        name
        refUpdateRule { allowsDeletions }
        target {
          __typename
          ... on Commit {
            oid
            committedDate
            messageHeadline
            author { name email user { login } }
            committer { name email user { login } }
          }
        }
      }
    }
  }
}
`

// Branches lists every branch with its tip commit and deletion protection.
func (c *Client) Branches(ctx context.Context) ([]Branch, error) {
	type gitActor struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		User  *struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	toActor := func(g *gitActor) Actor {
		if g == nil {
			return Actor{}
		}
		a := Actor{Name: g.Name, Email: g.Email}
		if g.User != nil {
			a.Login = g.User.Login
			a.Type = "User"
		}
		return a
	}

	var branches []Branch
	var cursor *string
	for {
		var resp struct {
			Repository struct {
				Refs struct {
					TotalCount int `json:"totalCount"`
					PageInfo   struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						Name          string `json:"name"`
						RefUpdateRule *struct {
							AllowsDeletions bool `json:"allowsDeletions"`
						} `json:"refUpdateRule"`
						Target *struct {
							Typename        string    `json:"__typename"`
							OID             string    `json:"oid"`
							CommittedDate   time.Time `json:"committedDate"`
							MessageHeadline string    `json:"messageHeadline"`
							Author          *gitActor `json:"author"`
							Committer       *gitActor `json:"committer"`
						} `json:"target"`
					} `json:"nodes"`
				} `json:"refs"`
			} `json:"repository"`
		}

		v := c.vars(map[string]any{"limit": c.PageSize, "cursor": cursor})
		if err := c.do(ctx, branchesQuery, v, &resp); err != nil {
			return nil, fmt.Errorf("listing branches: %w", err)
		}

		refs := resp.Repository.Refs
		for _, n := range refs.Nodes {
			b := Branch{Name: n.Name}
			if n.RefUpdateRule != nil {
				b.Protected = !n.RefUpdateRule.AllowsDeletions
			}
			if n.Target != nil && n.Target.Typename == "Commit" {
				b.Tip = Commit{
					OID:             n.Target.OID,
					CommittedDate:   n.Target.CommittedDate,
					MessageHeadline: n.Target.MessageHeadline,
					Author:          toActor(n.Target.Author),
					Committer:       toActor(n.Target.Committer),
				}
			}
			branches = append(branches, b)
		}

		c.progress("fetched %d/%d branches", len(branches), refs.TotalCount)
		if !refs.PageInfo.HasNextPage {
			break
		}
		next := refs.PageInfo.EndCursor
		cursor = &next
	}
	return branches, nil
}

const refRulesQuery = `
query ArboristRefRules($owner: String!, $name: String!, $limit: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    refs(refPrefix: "refs/heads/", first: $limit, after: $cursor,
         orderBy: {field: ALPHABETICAL, direction: ASC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes { name refUpdateRule { allowsDeletions } }
    }
  }
}
`

// UndeletableBranches names the branches a ref update rule forbids deleting.
// Branches absent from the result can be deleted.
//
// This asks for nothing but names and rules, so it is the cheap half of the
// branch listing: it is what remains to be fetched when Git has already
// supplied every branch's tip commit.
func (c *Client) UndeletableBranches(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	var cursor *string
	seen := 0
	for {
		var resp struct {
			Repository struct {
				Refs struct {
					TotalCount int `json:"totalCount"`
					PageInfo   struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						Name          string `json:"name"`
						RefUpdateRule *struct {
							AllowsDeletions bool `json:"allowsDeletions"`
						} `json:"refUpdateRule"`
					} `json:"nodes"`
				} `json:"refs"`
			} `json:"repository"`
		}

		v := c.vars(map[string]any{"limit": c.PageSize, "cursor": cursor})
		if err := c.do(ctx, refRulesQuery, v, &resp); err != nil {
			return nil, fmt.Errorf("listing branch protection rules: %w", err)
		}
		refs := resp.Repository.Refs
		for _, n := range refs.Nodes {
			if n.RefUpdateRule != nil && !n.RefUpdateRule.AllowsDeletions {
				out[n.Name] = true
			}
		}
		seen += len(refs.Nodes)
		c.progress("checked protection for %d/%d branches", seen, refs.TotalCount)
		if !refs.PageInfo.HasNextPage {
			break
		}
		next := refs.PageInfo.EndCursor
		cursor = &next
	}
	return out, nil
}

// CommitIdentities resolves the GitHub accounts behind the author and committer
// of each commit, keyed by commit ID.
//
// Git knows a commit's author only as a name and an email address; which
// account that is belongs to GitHub, which links accounts per commit rather
// than per address. A hundred commits are resolved per request, and the answer
// for a commit does not change, so this is the one thing a local clone still
// needs the API for.
func (c *Client) CommitIdentities(ctx context.Context, oids []string) (map[string]CommitIdentity, error) {
	out := make(map[string]CommitIdentity, len(oids))
	if len(oids) == 0 {
		return out, nil
	}

	done := 0
	for _, batch := range chunk(oids, c.CommitBatch) {
		query, vars := buildCommitBatch(batch)
		var resp struct {
			Repository map[string]*struct {
				Author    *jsonGitActor `json:"author"`
				Committer *jsonGitActor `json:"committer"`
			} `json:"repository"`
		}
		if err := c.do(ctx, query, c.vars(vars), &resp); err != nil {
			return nil, fmt.Errorf("resolving commit authors: %w", err)
		}
		for i, oid := range batch {
			node := resp.Repository[alias("c", i)]
			if node == nil {
				continue
			}
			id := CommitIdentity{}
			id.AuthorEmail, id.AuthorLogin = node.Author.identity()
			id.CommitterEmail, id.CommitterLogin = node.Committer.identity()
			out[oid] = id
		}
		done += len(batch)
		c.progress("resolved %d/%d commit authors", done, len(oids))
	}
	return out, nil
}

type jsonGitActor struct {
	Email string `json:"email"`
	User  *struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (g *jsonGitActor) identity() (email, login string) {
	if g == nil {
		return "", ""
	}
	if g.User != nil {
		login = g.User.Login
	}
	return g.Email, login
}

func buildCommitBatch(oids []string) (string, map[string]any) {
	vars := make(map[string]any, len(oids))
	var head, body strings.Builder
	head.WriteString("query ArboristCommitIdentities($owner: String!, $name: String!")
	for i, oid := range oids {
		v := alias("o", i)
		vars[v] = oid
		fmt.Fprintf(&head, ", $%s: GitObjectID!", v)
		fmt.Fprintf(&body, "    %s: object(oid: $%s) { ... on Commit { author { email user { login } } committer { email user { login } } } }\n",
			alias("c", i), v)
	}
	head.WriteString(") {\n  repository(owner: $owner, name: $name) {\n")

	return head.String() + body.String() + "  }\n}\n", vars
}

// PullRequestsForBranches maps each branch name to the pull requests opened
// from a branch of that name, in any state.
//
// Results can include pull requests raised from a fork whose head branch
// happens to share the name; callers should check HeadOwner. This uses aliased
// queries rather than paging every pull request in the repository, so the cost
// scales with the number of branches, not the repository's history.
func (c *Client) PullRequestsForBranches(ctx context.Context, names []string) (map[string][]PullRequest, error) {
	out := make(map[string][]PullRequest, len(names))
	if len(names) == 0 {
		return out, nil
	}

	done := 0
	for _, batch := range chunk(names, c.PRBatch) {
		query, vars := buildPRBatch(batch)
		var resp struct {
			Repository map[string]struct {
				Nodes []jsonPR `json:"nodes"`
			} `json:"repository"`
		}
		if err := c.do(ctx, query, c.vars(vars), &resp); err != nil {
			return nil, fmt.Errorf("looking up pull requests for branches: %w", err)
		}
		for i, name := range batch {
			for _, node := range resp.Repository[alias("b", i)].Nodes {
				out[name] = append(out[name], node.pullRequest())
			}
		}
		done += len(batch)
		c.progress("checked pull requests for %d/%d branches", done, len(names))
	}
	return out, nil
}

func buildPRBatch(names []string) (string, map[string]any) {
	vars := make(map[string]any, len(names))
	var head, body strings.Builder
	head.WriteString("query ArboristPRsByHead($owner: String!, $name: String!")
	for i, name := range names {
		v := alias("h", i)
		vars[v] = name
		fmt.Fprintf(&head, ", $%s: String!", v)
		fmt.Fprintf(&body, "    %s: pullRequests(headRefName: $%s, first: 20, states: [OPEN, MERGED, CLOSED], orderBy: {field: UPDATED_AT, direction: DESC}) { nodes { ...prFields } }\n",
			alias("b", i), v)
	}
	head.WriteString(") {\n  repository(owner: $owner, name: $name) {\n")

	return head.String() + body.String() + "  }\n}\n" + prFields + actorFields, vars
}

// ContainedIn compares every branch in heads against base, reporting how far
// ahead or behind each one is. A head with AheadBy == 0 is fully contained in
// base.
//
// This detects merge commits and fast-forwards. It cannot detect squash or
// rebase merges, which rewrite commits and so leave the head branch ahead of
// base; the closed-pull-request check covers those.
func (c *Client) ContainedIn(ctx context.Context, base string, heads []string) (map[string]Comparison, error) {
	out := make(map[string]Comparison, len(heads))
	if len(heads) == 0 {
		return out, nil
	}

	done := 0
	for _, batch := range chunk(heads, c.CompareBatch) {
		query, vars := buildCompareBatch(batch)
		vars["base"] = "refs/heads/" + base
		var resp struct {
			Repository struct {
				Ref map[string]*Comparison `json:"ref"`
			} `json:"repository"`
		}
		if err := c.do(ctx, query, c.vars(vars), &resp); err != nil {
			return nil, fmt.Errorf("comparing branches against %s: %w", base, err)
		}
		if resp.Repository.Ref == nil {
			return nil, fmt.Errorf("base branch %q not found in %s/%s", base, c.repo.Owner, c.repo.Name)
		}
		for i, name := range batch {
			if cmp := resp.Repository.Ref[alias("c", i)]; cmp != nil {
				out[name] = *cmp
			}
		}
		done += len(batch)
		c.progress("compared %d/%d branches against %s", done, len(heads), base)
	}
	return out, nil
}

func buildCompareBatch(names []string) (string, map[string]any) {
	vars := make(map[string]any, len(names))
	var head, body strings.Builder
	head.WriteString("query ArboristCompare($owner: String!, $name: String!, $base: String!")
	for i, name := range names {
		v := alias("h", i)
		vars[v] = name
		fmt.Fprintf(&head, ", $%s: String!", v)
		fmt.Fprintf(&body, "      %s: compare(headRef: $%s) { status aheadBy behindBy }\n", alias("c", i), v)
	}
	head.WriteString(") {\n  repository(owner: $owner, name: $name) {\n    ref(qualifiedName: $base) {\n")

	return head.String() + body.String() + "    }\n  }\n}\n", vars
}

const collaboratorsQuery = `
query ArboristCollaborators($owner: String!, $name: String!, $limit: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    collaborators(affiliation: ALL, first: $limit, after: $cursor) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes { login __typename }
    }
  }
}
`

// Collaborators returns every account with any access to the repository:
// direct collaborators, organization members with access through a team, and
// outside collaborators. Logins are lowercased for case-insensitive lookup.
//
// The GitHub API restricts this to viewers with push access; see
// Info.CanListCollaborators.
func (c *Client) Collaborators(ctx context.Context) (map[string]Actor, error) {
	out := map[string]Actor{}
	var cursor *string
	for {
		var resp struct {
			Repository struct {
				Collaborators *struct {
					TotalCount int `json:"totalCount"`
					PageInfo   struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []jsonActor `json:"nodes"`
				} `json:"collaborators"`
			} `json:"repository"`
		}

		v := c.vars(map[string]any{"limit": c.PageSize, "cursor": cursor})
		if err := c.do(ctx, collaboratorsQuery, v, &resp); err != nil {
			return nil, fmt.Errorf("listing collaborators: %w", err)
		}
		collabs := resp.Repository.Collaborators
		if collabs == nil {
			return nil, fmt.Errorf("could not read the collaborator list for %s/%s: it needs push access to the repository", c.repo.Owner, c.repo.Name)
		}
		for i := range collabs.Nodes {
			a := collabs.Nodes[i].actor()
			out[strings.ToLower(a.Login)] = a
		}
		c.progress("fetched %d/%d collaborators", len(out), collabs.TotalCount)
		if !collabs.PageInfo.HasNextPage {
			break
		}
		next := collabs.PageInfo.EndCursor
		cursor = &next
	}
	return out, nil
}

const pullRequestsQuery = `
query ArboristPullRequests($owner: String!, $name: String!, $states: [PullRequestState!], $limit: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    pullRequests(states: $states, first: $limit, after: $cursor,
                 orderBy: {field: UPDATED_AT, direction: DESC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes { ...prFields }
    }
  }
}
` + prFields + actorFields

// PullRequests pages through the repository's pull requests in the given
// states, stopping after limit results (limit <= 0 means no cap).
func (c *Client) PullRequests(ctx context.Context, states []string, limit int) ([]PullRequest, error) {
	var out []PullRequest
	var cursor *string
	for {
		var resp struct {
			Repository struct {
				PullRequests struct {
					TotalCount int `json:"totalCount"`
					PageInfo   struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []jsonPR `json:"nodes"`
				} `json:"pullRequests"`
			} `json:"repository"`
		}

		v := c.vars(map[string]any{"states": states, "limit": c.pageSizeFor(limit, len(out)), "cursor": cursor})
		if err := c.do(ctx, pullRequestsQuery, v, &resp); err != nil {
			return nil, fmt.Errorf("listing pull requests: %w", err)
		}
		prs := resp.Repository.PullRequests
		for _, node := range prs.Nodes {
			out = append(out, node.pullRequest())
		}
		c.progress("fetched %d/%d pull requests", len(out), prs.TotalCount)
		if !prs.PageInfo.HasNextPage || (limit > 0 && len(out) >= limit) {
			break
		}
		next := prs.PageInfo.EndCursor
		cursor = &next
	}
	return out, nil
}

const issuesQuery = `
query ArboristIssues($owner: String!, $name: String!, $states: [IssueState!], $limit: Int!, $cursor: String) {
  repository(owner: $owner, name: $name) {
    issues(states: $states, first: $limit, after: $cursor,
           orderBy: {field: UPDATED_AT, direction: DESC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes {
        number state title url updatedAt
        author { ...actorFields }
        assignees(first: 20) { nodes { login __typename } }
      }
    }
  }
}
` + actorFields

// Issues pages through the repository's issues in the given states, stopping
// after limit results (limit <= 0 means no cap).
func (c *Client) Issues(ctx context.Context, states []string, limit int) ([]Issue, error) {
	var out []Issue
	var cursor *string
	for {
		var resp struct {
			Repository struct {
				Issues struct {
					TotalCount int `json:"totalCount"`
					PageInfo   struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						Number    int        `json:"number"`
						State     string     `json:"state"`
						Title     string     `json:"title"`
						URL       string     `json:"url"`
						UpdatedAt time.Time  `json:"updatedAt"`
						Author    *jsonActor `json:"author"`
						Assignees struct {
							Nodes []jsonActor `json:"nodes"`
						} `json:"assignees"`
					} `json:"nodes"`
				} `json:"issues"`
			} `json:"repository"`
		}

		v := c.vars(map[string]any{"states": states, "limit": c.pageSizeFor(limit, len(out)), "cursor": cursor})
		if err := c.do(ctx, issuesQuery, v, &resp); err != nil {
			return nil, fmt.Errorf("listing issues: %w", err)
		}
		issues := resp.Repository.Issues
		for _, n := range issues.Nodes {
			iss := Issue{
				Number:    n.Number,
				State:     n.State,
				Title:     n.Title,
				URL:       n.URL,
				UpdatedAt: n.UpdatedAt,
				Author:    n.Author.actor(),
			}
			for i := range n.Assignees.Nodes {
				iss.Assignees = append(iss.Assignees, n.Assignees.Nodes[i].actor())
			}
			out = append(out, iss)
		}
		c.progress("fetched %d/%d issues", len(out), issues.TotalCount)
		if !issues.PageInfo.HasNextPage || (limit > 0 && len(out) >= limit) {
			break
		}
		next := issues.PageInfo.EndCursor
		cursor = &next
	}
	return out, nil
}

// pageSizeFor shrinks the next page so that a limit is not overshot.
func (c *Client) pageSizeFor(limit, have int) int {
	if limit <= 0 || limit-have > c.PageSize {
		return c.PageSize
	}
	return limit - have
}

func alias(prefix string, i int) string { return fmt.Sprintf("%s%d", prefix, i) }

func chunk[T any](in []T, size int) [][]T {
	if size < 1 {
		size = 1
	}
	var out [][]T
	for start := 0; start < len(in); start += size {
		end := min(start+size, len(in))
		out = append(out, in[start:end])
	}
	return out
}
