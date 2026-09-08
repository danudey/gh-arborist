package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/text"
	"github.com/danudey/gh-arborist/internal/gh"
	"github.com/danudey/gh-arborist/internal/timeutil"
)

// Options configures every check. A subcommand only sets the fields relevant
// to the checks it runs.
type Options struct {
	// Excludes are glob patterns for branch names no check may flag.
	Excludes []string
	// IncludeProtected allows flagging branches whose deletion is blocked by a
	// branch protection rule.
	IncludeProtected bool

	// Age is the threshold for the stale check.
	Age timeutil.Age

	// Bases are the branches the merged check compares against. Empty means
	// the repository's default branch.
	Bases []string
	// AllBases compares every branch against every other branch.
	AllBases bool

	// MergedOnly limits the closed-pull-request check to merged pull requests,
	// ignoring branches whose pull request was closed without merging.
	MergedOnly bool

	// IncludeClosed makes the orphaned-user check look at closed pull requests
	// and issues as well as open ones.
	IncludeClosed bool
	// IncludeBots stops the orphaned-user check from ignoring app accounts.
	IncludeBots bool
	// SkipForks stops the orphaned-user check from flagging pull requests
	// raised from a fork.
	SkipForks bool
	// IgnoreUsers are logins the orphaned-user check treats as still active.
	IgnoreUsers []string
	// Limit caps how many pull requests and issues the orphaned-user check
	// reads. Zero means no cap.
	Limit int

	// Details makes the runner look up every branch's pull requests up front,
	// so that findings carry the full per-item detail however the checks are
	// combined. Without it, a branch only gains pull request details when a
	// check that needed them happened to run first.
	Details bool
}

// Check is one audit that can be run against a repository.
type Check struct {
	ID    string
	Short string
	Run   func(context.Context, *Runner, *Result) error
}

// All lists the checks in report order.
var All = []Check{
	{
		ID:    "closed-prs",
		Short: "Branches whose pull requests are all merged or closed",
		Run:   checkClosedPRs,
	},
	{
		ID:    "merged",
		Short: "Branches already merged into another branch",
		Run:   checkMerged,
	},
	{
		ID:    "stale",
		Short: "Branches with no commits for a long time",
		Run:   checkStale,
	},
	{
		ID:    "orphans",
		Short: "Branches, pull requests and issues belonging to users without repository access",
		Run:   checkOrphans,
	},
}

// Lookup finds a check by ID.
func Lookup(id string) (Check, bool) {
	for _, c := range All {
		if c.ID == id {
			return c, true
		}
	}
	return Check{}, false
}

// Runner executes checks against one repository, fetching each piece of data
// at most once so that running several checks together costs little more than
// running the most expensive one alone.
type Runner struct {
	Client Source
	Info   gh.Info
	Opts   Options
	Now    time.Time

	filter *branchFilter

	branches      []gh.Branch
	branchesDone  bool
	prsByBranch   map[string][]gh.PullRequest
	prsDone       bool
	collaborators map[string]gh.Actor
	collabsDone   bool
}

// NewRunner prepares a runner. It fails if any exclude or base pattern is
// invalid.
func NewRunner(client Source, info gh.Info, opts Options, now time.Time) (*Runner, error) {
	// Merge bases are excluded from every check, not just the merged one: a
	// release branch you compare against is not a branch to prune. With
	// --all-bases every branch is a base, so that cannot apply.
	bases := opts.Bases
	if opts.AllBases {
		bases = nil
	}
	filter, err := newBranchFilter(info.DefaultBranch, opts.Excludes, bases, opts.IncludeProtected)
	if err != nil {
		return nil, err
	}
	return &Runner{Client: client, Info: info, Opts: opts, Now: now, filter: filter}, nil
}

// Run executes the named checks and returns their combined findings.
func (r *Runner) Run(ctx context.Context, ids []string) (*Result, error) {
	res := NewResult(r.Info.NameWithOwner, r.Now)
	res.RepositoryURL = r.Info.URL

	if r.Opts.Details {
		if _, err := r.PullRequestsByBranch(ctx); err != nil {
			return nil, err
		}
	}

	for _, id := range ids {
		check, ok := Lookup(id)
		if !ok {
			return nil, fmt.Errorf("unknown check %q", id)
		}
		if err := check.Run(ctx, r, res); err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		res.ChecksRun = append(res.ChecksRun, id)
	}
	if r.branchesDone {
		res.Scanned["branches"] = len(r.branches)
	}
	res.Sort()
	return res, nil
}

// Branches returns every branch in the repository.
func (r *Runner) Branches(ctx context.Context) ([]gh.Branch, error) {
	if r.branchesDone {
		return r.branches, nil
	}
	branches, err := r.Client.Branches(ctx)
	if err != nil {
		return nil, err
	}
	r.branches, r.branchesDone = branches, true
	return branches, nil
}

// PullRequestsByBranch returns the pull requests opened from each branch.
func (r *Runner) PullRequestsByBranch(ctx context.Context) (map[string][]gh.PullRequest, error) {
	if r.prsDone {
		return r.prsByBranch, nil
	}
	branches, err := r.Branches(ctx)
	if err != nil {
		return nil, err
	}
	// Only branches a check may flag can appear in the report, so asking about
	// the rest — the default branch, the merge bases, anything excluded —
	// would buy nothing.
	prs, err := r.Client.PullRequestsForBranches(ctx, branchNames(r.filter.eligible(branches)))
	if err != nil {
		return nil, err
	}
	r.prsByBranch, r.prsDone = prs, true
	return prs, nil
}

// Collaborators returns every account with access to the repository.
func (r *Runner) Collaborators(ctx context.Context) (map[string]gh.Actor, error) {
	if r.collabsDone {
		return r.collaborators, nil
	}
	if !r.Info.CanListCollaborators() {
		return nil, fmt.Errorf("listing collaborators needs push access to %s, but your permission is %s",
			r.Info.NameWithOwner, strings.ToLower(r.Info.ViewerPermission))
	}
	collabs, err := r.Client.Collaborators(ctx)
	if err != nil {
		return nil, err
	}
	if len(collabs) == 0 {
		return nil, fmt.Errorf("the collaborator list for %s came back empty, so every user would look departed", r.Info.NameWithOwner)
	}
	r.collaborators, r.collabsDone = collabs, true
	return collabs, nil
}

// owner is the repository's owner, taken from "OWNER/REPO".
func (r *Runner) owner() string {
	name, _, _ := strings.Cut(r.Info.NameWithOwner, "/")
	return name
}

// ownPullRequests filters out pull requests raised from a fork that happens to
// use the same branch name, leaving only ones whose head branch is this
// repository's branch.
func (r *Runner) ownPullRequests(prs []gh.PullRequest) []gh.PullRequest {
	owner := r.owner()
	var out []gh.PullRequest
	for _, pr := range prs {
		if pr.IsCrossRepository {
			continue
		}
		if pr.HeadOwner != "" && !strings.EqualFold(pr.HeadOwner, owner) {
			continue
		}
		out = append(out, pr)
	}
	return out
}

// branchURL returns the web URL for a branch.
func (r *Runner) branchURL(name string) string {
	if r.Info.URL == "" {
		return ""
	}
	return r.Info.URL + "/tree/" + name
}

// commitURL returns the web URL for a commit.
func (r *Runner) commitURL(oid string) string {
	if r.Info.URL == "" || oid == "" {
		return ""
	}
	return r.Info.URL + "/commit/" + oid
}

// userURL returns an actor's profile URL, or empty when the actor is not a
// resolvable account.
func (r *Runner) userURL(a gh.Actor) string {
	host := r.Info.HostURL()
	if host == "" || !a.Known() {
		return ""
	}
	return host + "/" + a.Login
}

// branchFinding assembles a branch finding, including the details that the
// HTML and Markdown reports expand. Every check goes through here so that one
// branch looks the same however it was found.
func (r *Runner) branchFinding(b gh.Branch, prs []gh.PullRequest, reasons ...Reason) Finding {
	owner := r.branchOwner(b, prs)
	return Finding{
		Kind:         KindBranch,
		Name:         b.Name,
		Title:        b.Tip.MessageHeadline,
		URL:          r.branchURL(b.Name),
		Owner:        owner.Describe(),
		OwnerURL:     r.userURL(owner),
		LastActivity: b.Tip.CommittedDate,
		Details:      r.branchDetails(b, prs),
		Reasons:      reasons,
	}
}

func (r *Runner) branchDetails(b gh.Branch, prs []gh.PullRequest) []Detail {
	var out []Detail
	if b.Tip.OID != "" {
		out = append(out, Detail{Label: "Last commit", Value: shortOID(b.Tip.OID), URL: r.commitURL(b.Tip.OID), Code: true})
	}
	if b.Tip.MessageHeadline != "" {
		out = append(out, Detail{Label: "Commit subject", Value: b.Tip.MessageHeadline})
	}
	if !b.Tip.CommittedDate.IsZero() {
		out = append(out, Detail{Label: "Committed", Value: fmt.Sprintf("%s (%s ago)",
			b.Tip.CommittedDate.UTC().Format("2006-01-02 15:04 MST"), humanAge(r.Now, b.Tip.CommittedDate))})
	}
	if a := b.Tip.Author; a.Describe() != "(unknown)" {
		out = append(out, Detail{Label: "Commit author", Value: a.Describe(), URL: r.userURL(a)})
	}
	// The committer is only worth showing when it differs, as it does for
	// cherry-picks and web edits.
	if c := b.Tip.Committer; c.Describe() != "(unknown)" && c.Describe() != b.Tip.Author.Describe() {
		out = append(out, Detail{Label: "Committer", Value: c.Describe(), URL: r.userURL(c)})
	}
	if b.Protected {
		out = append(out, Detail{Label: "Protection", Value: "a branch protection rule forbids deleting this branch"})
	}
	for _, pr := range r.ownPullRequests(prs) {
		out = append(out, Detail{
			Label: "Pull request",
			Value: fmt.Sprintf("%s (%s) %s — by %s", pr.Ref(), strings.ToLower(pr.State), pr.Title, pr.Author.Describe()),
			URL:   pr.URL,
		})
	}
	return out
}

// pullRequestFinding assembles a pull request finding with its details.
func (r *Runner) pullRequestFinding(pr gh.PullRequest, reasons []Reason) Finding {
	return Finding{
		Kind:         KindPullRequest,
		Name:         pr.Ref(),
		Title:        pr.Title,
		URL:          pr.URL,
		Owner:        pr.Author.Describe(),
		OwnerURL:     r.userURL(pr.Author),
		LastActivity: pr.UpdatedAt,
		Details:      r.pullRequestDetails(pr),
		Reasons:      reasons,
	}
}

func (r *Runner) pullRequestDetails(pr gh.PullRequest) []Detail {
	out := []Detail{
		{Label: "Title", Value: pr.Title},
		{Label: "State", Value: strings.ToLower(pr.State)},
		{Label: "Author", Value: pr.Author.Describe(), URL: r.userURL(pr.Author)},
	}
	out = append(out, assigneeDetails(r, pr.Assignees)...)

	head := Detail{Label: "Head branch", Value: pr.HeadRefName, Code: true}
	if pr.IsCrossRepository {
		// The branch lives in the fork, so a link into this repository would
		// be wrong.
		head.Value = pr.HeadOwner + ":" + pr.HeadRefName
	} else {
		head.URL = r.branchURL(pr.HeadRefName)
	}
	out = append(out, head)

	if pr.IsCrossRepository {
		out = append(out, Detail{Label: "Origin", Value: "raised from a fork, so the author never needed access to this repository"})
	}
	if pr.MergedAt != nil {
		out = append(out, Detail{Label: "Merged", Value: pr.MergedAt.UTC().Format("2006-01-02 15:04 MST")})
	} else if pr.ClosedAt != nil {
		out = append(out, Detail{Label: "Closed unmerged", Value: pr.ClosedAt.UTC().Format("2006-01-02 15:04 MST")})
	}
	return append(out, r.updatedDetail(pr.UpdatedAt))
}

// issueFinding assembles an issue finding with its details.
func (r *Runner) issueFinding(iss gh.Issue, reasons []Reason) Finding {
	return Finding{
		Kind:         KindIssue,
		Name:         iss.Ref(),
		Title:        iss.Title,
		URL:          iss.URL,
		Owner:        iss.Author.Describe(),
		OwnerURL:     r.userURL(iss.Author),
		LastActivity: iss.UpdatedAt,
		Details:      r.issueDetails(iss),
		Reasons:      reasons,
	}
}

func (r *Runner) issueDetails(iss gh.Issue) []Detail {
	out := []Detail{
		{Label: "Title", Value: iss.Title},
		{Label: "State", Value: strings.ToLower(iss.State)},
		{Label: "Author", Value: iss.Author.Describe(), URL: r.userURL(iss.Author)},
	}
	out = append(out, assigneeDetails(r, iss.Assignees)...)
	return append(out, r.updatedDetail(iss.UpdatedAt))
}

func (r *Runner) updatedDetail(t time.Time) Detail {
	if t.IsZero() {
		return Detail{Label: "Last updated", Value: "unknown"}
	}
	return Detail{Label: "Last updated", Value: fmt.Sprintf("%s (%s ago)",
		t.UTC().Format("2006-01-02 15:04 MST"), humanAge(r.Now, t))}
}

// assigneeDetails lists assignees one per row, so each gets its own link.
func assigneeDetails(r *Runner, assignees []gh.Actor) []Detail {
	if len(assignees) == 0 {
		return []Detail{{Label: "Assignees", Value: "nobody"}}
	}
	out := make([]Detail, 0, len(assignees))
	for _, a := range assignees {
		out = append(out, Detail{Label: "Assignee", Value: a.Describe(), URL: r.userURL(a)})
	}
	return out
}

// shortOID abbreviates a commit ID the way Git does.
func shortOID(oid string) string {
	if len(oid) > 7 {
		return oid[:7]
	}
	return oid
}

// branchOwner works out who a branch belongs to.
//
// Whoever wrote the tip commit is the most reliable answer, so it is tried
// first. A branch can have several pull requests over its life, and the newest
// of them is not necessarily the person still working on the branch. The pull
// request author is only used when the commit's email address is not linked to
// any GitHub account.
func (r *Runner) branchOwner(b gh.Branch, prs []gh.PullRequest) gh.Actor {
	if b.Tip.Author.Known() {
		return b.Tip.Author
	}
	if b.Tip.Committer.Known() {
		return b.Tip.Committer
	}
	for _, pr := range r.ownPullRequests(prs) {
		if pr.Author.Known() {
			return pr.Author
		}
	}
	return b.Tip.Author
}

// mergeBases resolves which branches the merged check compares against.
func (r *Runner) mergeBases(branches []gh.Branch) ([]string, error) {
	if r.Opts.AllBases {
		return branchNames(branches), nil
	}
	if len(r.Opts.Bases) == 0 {
		if r.Info.DefaultBranch == "" {
			return nil, fmt.Errorf("%s has no default branch; pass --base to choose one", r.Info.NameWithOwner)
		}
		return []string{r.Info.DefaultBranch}, nil
	}

	var bases []string
	for _, want := range r.Opts.Bases {
		p, err := compilePattern(want)
		if err != nil {
			return nil, err
		}
		matched := false
		for _, b := range branches {
			if p.match(b.Name) {
				bases = append(bases, b.Name)
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("no branch matches --base %q", want)
		}
	}
	sort.Strings(bases)
	return dedupe(bases), nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// onDate formats a timestamp for use inside a reason.
func onDate(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// humanAge renders how long ago t was as a bare duration, so it reads
// correctly inside a sentence ("no commits for about 2 years").
func humanAge(now, t time.Time) string {
	return strings.TrimSuffix(text.RelativeTimeAgo(now, t), " ago")
}
