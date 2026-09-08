// Package source assembles the repository data the checks read from the
// cheapest place that knows it.
//
// Three things feed a scan, in order of preference:
//
//   - a local Git repository, which answers branch and ancestry questions with
//     no network access at all;
//   - the on-disk cache, which remembers answers that cannot change, such as
//     whether one commit is an ancestor of another;
//   - the GitHub API, for everything only GitHub knows.
//
// Every API call made from here is a bulk one. Nothing is fetched per item.
package source

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/danudey/gh-arborist/internal/cache"
	"github.com/danudey/gh-arborist/internal/checks"
	"github.com/danudey/gh-arborist/internal/gh"
)

var (
	_ checks.Source = (*Source)(nil)
	_ API           = (*gh.Client)(nil)
)

// How long a cached answer stays good. Anything keyed by a commit ID uses
// cache.Never, because the commits fix the answer: a branch that is an ancestor
// of a given commit is an ancestor of it forever.
const (
	ttlInfo           = time.Hour
	ttlProtection     = time.Hour
	ttlCollabs        = time.Hour
	ttlCommitIdentity = 7 * 24 * time.Hour
	ttlBranchPRs      = 30 * 24 * time.Hour
)

// API is the GitHub half of a source. gh.Client implements it.
type API interface {
	Info(ctx context.Context) (gh.Info, error)
	Branches(ctx context.Context) ([]gh.Branch, error)
	UndeletableBranches(ctx context.Context) (map[string]bool, error)
	CommitIdentities(ctx context.Context, oids []string) (map[string]gh.CommitIdentity, error)
	PullRequestsForBranches(ctx context.Context, names []string) (map[string][]gh.PullRequest, error)
	ContainedIn(ctx context.Context, base string, heads []string) (map[string]gh.Comparison, error)
	Collaborators(ctx context.Context) (map[string]gh.Actor, error)
	PullRequests(ctx context.Context, states []string, limit int) ([]gh.PullRequest, error)
	Issues(ctx context.Context, states []string, limit int) ([]gh.Issue, error)
}

// Git is the local half of a source. gitrepo.Repo implements it, and a nil Git
// means no local repository was available.
type Git interface {
	Branches(ctx context.Context) ([]gh.Branch, error)
	ContainedIn(ctx context.Context, base string, heads []string) (map[string]gh.Comparison, error)
	Describe() string
}

// Source implements checks.Source over an API, an optional local Git
// repository and an optional cache.
type Source struct {
	api   API
	git   Git
	cache *cache.Cache

	// batch is how many branches the API bundles into one pull request
	// lookup, used to judge whether consulting the cache is worth its cost.
	batch    int
	progress func(format string, args ...any)

	branches     []gh.Branch
	branchesDone bool
	tips         map[string]string

	// prsByQuery and issuesByQuery share a paged listing between the checks
	// that want it and the cache validation that needs it.
	prsByQuery    map[string][]gh.PullRequest
	issuesByQuery map[string][]gh.Issue

	openHeads     map[string]bool
	openHeadsDone bool
}

// Options configures New.
type Options struct {
	// Git is the local repository to prefer, or nil to use the API for
	// everything.
	Git Git
	// Cache is the store for answers that survive between runs, or nil to
	// fetch everything afresh.
	Cache *cache.Cache
	// Batch mirrors the API client's pull request batch size.
	Batch int
	// Progress, when set, receives one-line status updates.
	Progress func(format string, args ...any)
}

// New builds a source. api must not be nil; everything else is optional.
func New(api API, opts Options) *Source {
	progress := opts.Progress
	if progress == nil {
		progress = func(string, ...any) {}
	}
	batch := opts.Batch
	if batch < 1 {
		batch = 50
	}
	return &Source{
		api:           api,
		git:           opts.Git,
		cache:         opts.Cache,
		batch:         batch,
		progress:      progress,
		prsByQuery:    map[string][]gh.PullRequest{},
		issuesByQuery: map[string][]gh.Issue{},
	}
}

// Info fetches repository metadata, from the cache when it is recent enough.
func (s *Source) Info(ctx context.Context) (gh.Info, error) {
	key := cache.Key("info")
	var info gh.Info
	if s.cache.Get(key, &info) {
		return info, nil
	}
	info, err := s.api.Info(ctx)
	if err != nil {
		return info, err
	}
	s.cache.Put(key, info, ttlInfo)
	return info, nil
}

// Branches lists every branch with its tip commit and whether a protection
// rule forbids deleting it.
//
// With a local repository the commit data comes from Git, and the API is asked
// only for the two things Git cannot know: the protection rules, and which
// GitHub account each commit's email address belongs to. The second is
// resolved one request per hundred distinct addresses rather than one node per
// branch, so the cost follows the number of people who have touched the
// repository, not the number of branches.
func (s *Source) Branches(ctx context.Context) ([]gh.Branch, error) {
	if s.branchesDone {
		return s.branches, nil
	}

	branches, err := s.gitBranches(ctx)
	if err != nil {
		return nil, err
	}
	if branches == nil {
		if branches, err = s.api.Branches(ctx); err != nil {
			return nil, err
		}
	}

	s.tips = make(map[string]string, len(branches))
	for _, b := range branches {
		s.tips[b.Name] = b.Tip.OID
	}
	s.branches, s.branchesDone = branches, true
	return branches, nil
}

// gitBranches reads the branches from the local repository and fills in the
// parts Git does not know. It returns nil when there is no local repository, or
// when reading it failed and the API should be used instead.
func (s *Source) gitBranches(ctx context.Context) ([]gh.Branch, error) {
	if s.git == nil {
		return nil, nil
	}
	branches, err := s.git.Branches(ctx)
	if err != nil {
		s.progress("falling back to the API for branches: %v", err)
		return nil, nil
	}

	protected, err := s.undeletableBranches(ctx)
	if err != nil {
		return nil, err
	}
	identities, err := s.identitiesForTips(ctx, branches)
	if err != nil {
		return nil, err
	}
	for i := range branches {
		b := &branches[i]
		b.Protected = protected[b.Name]
		id := identities[b.Tip.OID]
		attachLogin(&b.Tip.Author, id.AuthorLogin)
		attachLogin(&b.Tip.Committer, id.CommitterLogin)
	}
	return branches, nil
}

func attachLogin(a *gh.Actor, login string) {
	if login == "" {
		return
	}
	a.Login = login
	a.Type = "User"
}

func (s *Source) undeletableBranches(ctx context.Context) (map[string]bool, error) {
	key := cache.Key("undeletable")
	var out map[string]bool
	if s.cache.Get(key, &out) {
		return out, nil
	}
	out, err := s.api.UndeletableBranches(ctx)
	if err != nil {
		return nil, err
	}
	s.cache.Put(key, out, ttlProtection)
	return out, nil
}

// identitiesForTips resolves the accounts behind each branch tip's author and
// committer, keyed by commit ID.
//
// It has to be asked one commit at a time rather than one address at a time:
// GitHub links a commit to an account, not an address to an account, and the
// same address can resolve for one commit and not for another. The saving comes
// from the cache instead, since a commit's answer only changes if somebody
// alters which addresses their account claims — and from branches that share a
// tip, which are asked about once.
func (s *Source) identitiesForTips(ctx context.Context, branches []gh.Branch) (map[string]gh.CommitIdentity, error) {
	out := make(map[string]gh.CommitIdentity, len(branches))
	need := map[string]bool{}
	for _, b := range branches {
		oid := b.Tip.OID
		if oid == "" {
			continue
		}
		if _, ok := out[oid]; ok || need[oid] {
			continue
		}
		var id gh.CommitIdentity
		if s.cache.Get(cache.Key("commit-identity", oid), &id) {
			out[oid] = id
			continue
		}
		need[oid] = true
	}
	if len(need) == 0 {
		return out, nil
	}

	oids := sortedKeys(need)
	s.progress("resolving the accounts behind %d commits", len(oids))
	ids, err := s.api.CommitIdentities(ctx, oids)
	if err != nil {
		return nil, err
	}
	for _, oid := range oids {
		// A commit the API said nothing about is recorded as having no
		// accounts behind it, so the next run does not ask again.
		id := ids[oid]
		out[oid] = id
		s.cache.Put(cache.Key("commit-identity", oid), id, ttlCommitIdentity)
	}
	return out, nil
}

// ContainedIn reports which of heads are fully merged into base.
//
// A local repository answers this outright. Otherwise the answer is looked up
// by the pair of commit IDs involved, which fixes it permanently: if this
// commit was an ancestor of that one yesterday, it still is.
func (s *Source) ContainedIn(ctx context.Context, base string, heads []string) (map[string]gh.Comparison, error) {
	if s.git != nil {
		return s.git.ContainedIn(ctx, base, heads)
	}

	baseOID := s.tipOf(ctx, base)
	if s.cache == nil || baseOID == "" {
		return s.api.ContainedIn(ctx, base, heads)
	}

	out := make(map[string]gh.Comparison, len(heads))
	var missing []string
	for _, head := range heads {
		headOID := s.tipOf(ctx, head)
		if headOID == "" {
			missing = append(missing, head)
			continue
		}
		var cached comparison
		if s.cache.Get(containKey(baseOID, headOID), &cached) {
			if cached.Contained {
				out[head] = cached.Comparison
			}
			continue
		}
		missing = append(missing, head)
	}
	if len(missing) < len(heads) {
		s.progress("reused %d cached comparisons against %s", len(heads)-len(missing), base)
	}
	if len(missing) == 0 {
		return out, nil
	}

	fetched, err := s.api.ContainedIn(ctx, base, missing)
	if err != nil {
		return nil, err
	}
	for _, head := range missing {
		cmp, ok := fetched[head]
		if ok {
			out[head] = cmp
		}
		// "Not merged" is worth remembering as much as "merged" is: both are
		// fixed by the pair of commits the answer was about.
		if headOID := s.tipOf(ctx, head); headOID != "" {
			s.cache.Put(containKey(baseOID, headOID), comparison{Contained: ok, Comparison: cmp}, cache.Never)
		}
	}
	return out, nil
}

// comparison is a cached comparison. The flag distinguishes a branch that was
// found not to be merged from one that was never asked about, which a bare
// gh.Comparison cannot express.
type comparison struct {
	Contained  bool          `json:"contained"`
	Comparison gh.Comparison `json:"comparison,omitempty"`
}

func containKey(baseOID, headOID string) string {
	return cache.Key("contained", baseOID, headOID)
}

// PullRequestsForBranches maps each branch name to the pull requests opened
// from it.
//
// Cached results are keyed by the branch's tip commit, but a branch can gain a
// pull request without moving, so a cached answer is only used once the list of
// currently open pull requests confirms the branch has none. That list is a
// single bulk fetch, and the orphans check wants it anyway.
func (s *Source) PullRequestsForBranches(ctx context.Context, names []string) (map[string][]gh.PullRequest, error) {
	if s.cache == nil || len(names) == 0 {
		return s.api.PullRequestsForBranches(ctx, names)
	}

	out := make(map[string][]gh.PullRequest, len(names))
	hits := map[string][]gh.PullRequest{}
	var missing []string
	for _, name := range names {
		oid := s.tipOf(ctx, name)
		var prs []gh.PullRequest
		if oid != "" && s.cache.Get(branchPRKey(name, oid), &prs) {
			hits[name] = prs
			continue
		}
		missing = append(missing, name)
	}

	// Confirming the cache costs a paged listing of open pull requests, which
	// is only worth it when the cache saves more than the single request the
	// branches would otherwise have been bundled into.
	confirmed := false
	if len(hits) > s.batch {
		open, err := s.openPullRequestHeads(ctx)
		if err != nil {
			s.progress("could not confirm cached pull requests, fetching them again: %v", err)
		} else {
			confirmed = true
			for name := range hits {
				if open[name] {
					delete(hits, name)
					missing = append(missing, name)
				}
			}
		}
	}
	if !confirmed {
		for name := range hits {
			missing = append(missing, name)
		}
		hits = nil
	}

	for name, prs := range hits {
		out[name] = prs
	}
	if len(hits) > 0 {
		s.progress("reused cached pull requests for %d branches", len(hits))
	}
	if len(missing) == 0 {
		return out, nil
	}

	sort.Strings(missing)
	fetched, err := s.api.PullRequestsForBranches(ctx, missing)
	if err != nil {
		return nil, err
	}
	for _, name := range missing {
		prs := fetched[name]
		if len(prs) > 0 {
			out[name] = prs
		}
		// A branch with no pull requests is worth remembering too.
		if oid := s.tipOf(ctx, name); oid != "" {
			s.cache.Put(branchPRKey(name, oid), prs, ttlBranchPRs)
		}
	}
	return out, nil
}

func branchPRKey(name, oid string) string {
	return cache.Key("branch-prs", name, oid)
}

// openPullRequestHeads names the branches with at least one open pull request.
func (s *Source) openPullRequestHeads(ctx context.Context) (map[string]bool, error) {
	if s.openHeadsDone {
		return s.openHeads, nil
	}
	prs, err := s.PullRequests(ctx, []string{"OPEN"}, 0)
	if err != nil {
		return nil, err
	}
	heads := make(map[string]bool, len(prs))
	for _, pr := range prs {
		heads[pr.HeadRefName] = true
	}
	s.openHeads, s.openHeadsDone = heads, true
	return heads, nil
}

// Collaborators lists every account with access to the repository.
func (s *Source) Collaborators(ctx context.Context) (map[string]gh.Actor, error) {
	key := cache.Key("collaborators")
	var out map[string]gh.Actor
	if s.cache.Get(key, &out) && len(out) > 0 {
		return out, nil
	}
	out, err := s.api.Collaborators(ctx)
	if err != nil {
		return nil, err
	}
	s.cache.Put(key, out, ttlCollabs)
	return out, nil
}

// PullRequests lists pull requests in the given states. Repeated calls with the
// same arguments are served from memory, so the checks and the cache
// confirmation share one listing.
func (s *Source) PullRequests(ctx context.Context, states []string, limit int) ([]gh.PullRequest, error) {
	key := queryKey(states, limit)
	if prs, ok := s.prsByQuery[key]; ok {
		return prs, nil
	}
	prs, err := s.api.PullRequests(ctx, states, limit)
	if err != nil {
		return nil, err
	}
	s.prsByQuery[key] = prs
	return prs, nil
}

// Issues lists issues in the given states, sharing one listing between callers.
func (s *Source) Issues(ctx context.Context, states []string, limit int) ([]gh.Issue, error) {
	key := queryKey(states, limit)
	if issues, ok := s.issuesByQuery[key]; ok {
		return issues, nil
	}
	issues, err := s.api.Issues(ctx, states, limit)
	if err != nil {
		return nil, err
	}
	s.issuesByQuery[key] = issues
	return issues, nil
}

// LocalRepo names the local Git repository being read, or "" when everything
// comes from the API.
func (s *Source) LocalRepo() string {
	if s.git == nil {
		return ""
	}
	return s.git.Describe()
}

// Save writes the cache back. Failing to save is worth reporting but not worth
// failing the scan for, since the report itself is already correct.
func (s *Source) Save() error { return s.cache.Save() }

// tipOf returns a branch's tip commit ID, or "" when the branch is unknown.
func (s *Source) tipOf(ctx context.Context, name string) string {
	if !s.branchesDone {
		if _, err := s.Branches(ctx); err != nil {
			return ""
		}
	}
	return s.tips[name]
}

func queryKey(states []string, limit int) string {
	return fmt.Sprintf("%s/%d", strings.Join(states, ","), limit)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
