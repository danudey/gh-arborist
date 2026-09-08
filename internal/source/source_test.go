package source

import (
	"context"
	"errors"
	"testing"

	"github.com/danudey/gh-arborist/internal/cache"
	"github.com/danudey/gh-arborist/internal/gh"
)

// fakeAPI counts what it is asked, so a test can prove that a local repository
// or the cache stopped a request being made.
type fakeAPI struct {
	info        gh.Info
	branches    []gh.Branch
	undeletable map[string]bool
	identities  map[string]gh.CommitIdentity
	prsByBranch map[string][]gh.PullRequest
	containment map[string]gh.Comparison // keyed by head name
	collabs     map[string]gh.Actor
	prs         []gh.PullRequest
	issues      []gh.Issue

	calls        map[string]int
	askedHeads   []string
	askedOIDs    []string
	askedBranchP []string
}

func (f *fakeAPI) note(name string) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
}

func (f *fakeAPI) Info(context.Context) (gh.Info, error) {
	f.note("Info")
	return f.info, nil
}

func (f *fakeAPI) Branches(context.Context) ([]gh.Branch, error) {
	f.note("Branches")
	return f.branches, nil
}

func (f *fakeAPI) UndeletableBranches(context.Context) (map[string]bool, error) {
	f.note("UndeletableBranches")
	return f.undeletable, nil
}

func (f *fakeAPI) CommitIdentities(_ context.Context, oids []string) (map[string]gh.CommitIdentity, error) {
	f.note("CommitIdentities")
	f.askedOIDs = append(f.askedOIDs, oids...)
	out := map[string]gh.CommitIdentity{}
	for _, oid := range oids {
		if id, ok := f.identities[oid]; ok {
			out[oid] = id
		}
	}
	return out, nil
}

func (f *fakeAPI) PullRequestsForBranches(_ context.Context, names []string) (map[string][]gh.PullRequest, error) {
	f.note("PullRequestsForBranches")
	f.askedBranchP = append(f.askedBranchP, names...)
	out := map[string][]gh.PullRequest{}
	for _, n := range names {
		if prs, ok := f.prsByBranch[n]; ok {
			out[n] = prs
		}
	}
	return out, nil
}

func (f *fakeAPI) ContainedIn(_ context.Context, _ string, heads []string) (map[string]gh.Comparison, error) {
	f.note("ContainedIn")
	f.askedHeads = append(f.askedHeads, heads...)
	out := map[string]gh.Comparison{}
	for _, h := range heads {
		if cmp, ok := f.containment[h]; ok {
			out[h] = cmp
		}
	}
	return out, nil
}

func (f *fakeAPI) Collaborators(context.Context) (map[string]gh.Actor, error) {
	f.note("Collaborators")
	return f.collabs, nil
}

func (f *fakeAPI) PullRequests(context.Context, []string, int) ([]gh.PullRequest, error) {
	f.note("PullRequests")
	return f.prs, nil
}

func (f *fakeAPI) Issues(context.Context, []string, int) ([]gh.Issue, error) {
	f.note("Issues")
	return f.issues, nil
}

// fakeGit stands in for a local clone.
type fakeGit struct {
	branches    []gh.Branch
	containment map[string]gh.Comparison
	err         error
	calls       int
}

func (g *fakeGit) Branches(context.Context) ([]gh.Branch, error) {
	g.calls++
	if g.err != nil {
		return nil, g.err
	}
	return g.branches, nil
}

func (g *fakeGit) ContainedIn(_ context.Context, _ string, heads []string) (map[string]gh.Comparison, error) {
	out := map[string]gh.Comparison{}
	for _, h := range heads {
		if cmp, ok := g.containment[h]; ok {
			out[h] = cmp
		}
	}
	return out, nil
}

func (g *fakeGit) Describe() string { return "/tmp/clone" }

func branch(name, oid string) gh.Branch {
	return gh.Branch{Name: name, Tip: gh.Commit{
		OID:       oid,
		Author:    gh.Actor{Name: "Ada", Email: "ada@example.com"},
		Committer: gh.Actor{Name: "Ada", Email: "ada@example.com"},
	}}
}

func store(t *testing.T) *cache.Cache {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c, err := cache.Open("github.com", "owner", "repo")
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	return c
}

// With a local clone, the commit data comes from Git and the API is only asked
// the two things Git cannot know.
func TestBranchesPrefersGitAndFillsInTheRest(t *testing.T) {
	api := &fakeAPI{
		undeletable: map[string]bool{"release": true},
		identities: map[string]gh.CommitIdentity{
			"oid1": {AuthorEmail: "ada@example.com", AuthorLogin: "ada", CommitterEmail: "ada@example.com", CommitterLogin: "ada"},
		},
	}
	git := &fakeGit{branches: []gh.Branch{branch("feature", "oid1"), branch("release", "oid2")}}

	got, err := New(api, Options{Git: git}).Branches(context.Background())
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}

	if api.calls["Branches"] != 0 {
		t.Error("the API was asked for branches even though a clone was available")
	}
	if git.calls != 1 {
		t.Errorf("the clone was read %d times, want 1", git.calls)
	}
	byName := map[string]gh.Branch{}
	for _, b := range got {
		byName[b.Name] = b
	}
	if login := byName["feature"].Tip.Author.Login; login != "ada" {
		t.Errorf("author login is %q, want ada resolved from the commit", login)
	}
	if byName["feature"].Tip.Author.Name != "Ada" {
		t.Error("resolving the login lost the Git identity")
	}
	if !byName["release"].Protected {
		t.Error("release should be marked protected")
	}
	if byName["feature"].Protected {
		t.Error("feature is not protected")
	}
	// A commit the API said nothing about keeps its Git identity and no login.
	if login := byName["release"].Tip.Author.Login; login != "" {
		t.Errorf("unresolved author login is %q, want empty", login)
	}
}

// Two branches at the same commit are one question, not two.
func TestBranchesAsksAboutEachCommitOnce(t *testing.T) {
	api := &fakeAPI{}
	git := &fakeGit{branches: []gh.Branch{
		branch("a", "shared"), branch("b", "shared"), branch("c", "other"),
	}}
	if _, err := New(api, Options{Git: git}).Branches(context.Background()); err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(api.askedOIDs) != 2 {
		t.Errorf("asked about %v, want each distinct commit once", api.askedOIDs)
	}
}

// A clone that cannot be read is not a reason to fail: the API knows all of it.
func TestBranchesFallsBackToTheAPI(t *testing.T) {
	api := &fakeAPI{branches: []gh.Branch{branch("feature", "oid1")}}
	git := &fakeGit{err: errors.New("object file is empty")}

	got, err := New(api, Options{Git: git}).Branches(context.Background())
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(got) != 1 || api.calls["Branches"] != 1 {
		t.Errorf("got %d branches and %d API calls, want the API to have answered", len(got), api.calls["Branches"])
	}
}

// Whether one commit is an ancestor of another cannot change, so the answer is
// asked for once and then remembered between runs.
func TestContainmentIsRememberedBetweenRuns(t *testing.T) {
	c := store(t)
	branches := []gh.Branch{branch("main", "base1"), branch("landed", "head1"), branch("busy", "head2")}
	newAPI := func() *fakeAPI {
		return &fakeAPI{
			branches:    branches,
			containment: map[string]gh.Comparison{"landed": {Status: "BEHIND", BehindBy: 4}},
		}
	}

	first := newAPI()
	got, err := New(first, Options{Cache: c}).ContainedIn(context.Background(), "main", []string{"landed", "busy"})
	if err != nil {
		t.Fatalf("ContainedIn: %v", err)
	}
	if got["landed"].BehindBy != 4 {
		t.Fatalf("first run gave %+v", got)
	}
	if len(first.askedHeads) != 2 {
		t.Errorf("first run asked about %v, want both branches", first.askedHeads)
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A second run over the same commits must not ask again — including about
	// the branch that was not merged, since that answer is just as fixed.
	second := newAPI()
	reopened := store2(t, c)
	got, err = New(second, Options{Cache: reopened}).ContainedIn(context.Background(), "main", []string{"landed", "busy"})
	if err != nil {
		t.Fatalf("ContainedIn: %v", err)
	}
	if second.calls["ContainedIn"] != 0 {
		t.Errorf("the second run asked the API about %v", second.askedHeads)
	}
	if got["landed"].BehindBy != 4 {
		t.Errorf("the cached answer is %+v, want 4 commits behind", got["landed"])
	}
	if _, ok := got["busy"]; ok {
		t.Errorf("busy came back as %+v, want it omitted as it was before", got["busy"])
	}
}

// store2 reopens the cache written by c, to model a second run of the program.
func store2(t *testing.T, c *cache.Cache) *cache.Cache {
	t.Helper()
	reopened, err := cache.Open("github.com", "owner", "repo")
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	if reopened.Path() != c.Path() {
		t.Fatalf("reopened %s, want %s", reopened.Path(), c.Path())
	}
	return reopened
}

// A local clone answers containment outright, so neither the cache nor the API
// is consulted.
func TestContainmentPrefersGit(t *testing.T) {
	api := &fakeAPI{containment: map[string]gh.Comparison{"landed": {Status: "BEHIND", BehindBy: 99}}}
	git := &fakeGit{containment: map[string]gh.Comparison{"landed": {Status: "BEHIND", BehindBy: 4}}}

	got, err := New(api, Options{Git: git, Cache: store(t)}).ContainedIn(context.Background(), "main", []string{"landed"})
	if err != nil {
		t.Fatalf("ContainedIn: %v", err)
	}
	if got["landed"].BehindBy != 4 || api.calls["ContainedIn"] != 0 {
		t.Errorf("got %+v with %d API calls, want the clone's answer and none", got["landed"], api.calls["ContainedIn"])
	}
}

// A branch can gain a pull request without its tip moving, so a cached answer
// is only trusted once the list of open pull requests confirms it.
func TestCachedBranchPullRequestsAreConfirmedAgainstOpenOnes(t *testing.T) {
	c := store(t)
	merged := gh.PullRequest{Number: 1, State: "MERGED", HeadRefName: "landed"}
	branches := []gh.Branch{branch("landed", "head1"), branch("quiet", "head2")}
	names := []string{"landed", "quiet"}

	first := &fakeAPI{branches: branches, prsByBranch: map[string][]gh.PullRequest{"landed": {merged}}}
	// Batch 1 means two cached branches are worth confirming.
	got, err := New(first, Options{Cache: c, Batch: 1}).PullRequestsForBranches(context.Background(), names)
	if err != nil {
		t.Fatalf("PullRequestsForBranches: %v", err)
	}
	if len(got["landed"]) != 1 {
		t.Fatalf("first run gave %v", got)
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Somebody has since opened a pull request from "landed". Its cached entry
	// must be thrown away, while "quiet" is still served from the cache.
	reopened := store2(t, c)
	opened := gh.PullRequest{Number: 2, State: "OPEN", HeadRefName: "landed"}
	second := &fakeAPI{
		branches:    branches,
		prs:         []gh.PullRequest{opened},
		prsByBranch: map[string][]gh.PullRequest{"landed": {merged, opened}},
	}
	got, err = New(second, Options{Cache: reopened, Batch: 1}).PullRequestsForBranches(context.Background(), names)
	if err != nil {
		t.Fatalf("PullRequestsForBranches: %v", err)
	}
	if len(got["landed"]) != 2 {
		t.Errorf("landed came back with %v, want the newly opened pull request too", got["landed"])
	}
	if len(second.askedBranchP) != 1 || second.askedBranchP[0] != "landed" {
		t.Errorf("the API was asked about %v, want only the branch with an open pull request", second.askedBranchP)
	}
}

// Confirming the cache costs a listing of open pull requests, which is not
// worth it when the branches would have fitted into one request anyway.
func TestSmallCacheHitsAreNotWorthConfirming(t *testing.T) {
	c := store(t)
	branches := []gh.Branch{branch("landed", "head1")}
	api := &fakeAPI{branches: branches, prsByBranch: map[string][]gh.PullRequest{"landed": {{Number: 1, State: "MERGED"}}}}
	if _, err := New(api, Options{Cache: c, Batch: 50}).PullRequestsForBranches(context.Background(), []string{"landed"}); err != nil {
		t.Fatalf("PullRequestsForBranches: %v", err)
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	second := &fakeAPI{branches: branches, prsByBranch: api.prsByBranch}
	if _, err := New(second, Options{Cache: store2(t, c), Batch: 50}).PullRequestsForBranches(context.Background(), []string{"landed"}); err != nil {
		t.Fatalf("PullRequestsForBranches: %v", err)
	}
	if second.calls["PullRequests"] != 0 {
		t.Error("open pull requests were listed to confirm a single cached branch")
	}
	if second.calls["PullRequestsForBranches"] != 1 {
		t.Errorf("the branch was fetched %d times, want 1", second.calls["PullRequestsForBranches"])
	}
}

// The checks and the cache confirmation want the same listing, so it is only
// fetched once.
func TestListingsAreSharedBetweenCallers(t *testing.T) {
	api := &fakeAPI{prs: []gh.PullRequest{{Number: 1, State: "OPEN"}}, issues: []gh.Issue{{Number: 2}}}
	s := New(api, Options{})
	ctx := context.Background()
	for range 3 {
		if _, err := s.PullRequests(ctx, []string{"OPEN"}, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Issues(ctx, []string{"OPEN"}, 0); err != nil {
			t.Fatal(err)
		}
	}
	if api.calls["PullRequests"] != 1 || api.calls["Issues"] != 1 {
		t.Errorf("listed pull requests %d times and issues %d times, want 1 each",
			api.calls["PullRequests"], api.calls["Issues"])
	}
	// A different question is a different listing.
	if _, err := s.PullRequests(ctx, []string{"OPEN", "CLOSED"}, 0); err != nil {
		t.Fatal(err)
	}
	if api.calls["PullRequests"] != 2 {
		t.Errorf("asking for other states reused the first listing (%d calls)", api.calls["PullRequests"])
	}
}

// An empty collaborator list would make everybody look departed, so it must
// never be served from the cache as if it were an answer.
func TestEmptyCollaboratorsIsNotACachedAnswer(t *testing.T) {
	c := store(t)
	api := &fakeAPI{collabs: map[string]gh.Actor{}}
	s := New(api, Options{Cache: c})
	ctx := context.Background()
	if _, err := s.Collaborators(ctx); err != nil {
		t.Fatal(err)
	}
	api.collabs = map[string]gh.Actor{"ada": {Login: "ada"}}
	got, err := New(api, Options{Cache: c}).Collaborators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("got %v, want the list to have been fetched again", got)
	}
}

// Without a cache every answer comes from the API, which is what --no-cache
// promises.
func TestNoCacheAsksEveryTime(t *testing.T) {
	api := &fakeAPI{
		branches:    []gh.Branch{branch("main", "base1"), branch("landed", "head1")},
		containment: map[string]gh.Comparison{"landed": {Status: "BEHIND", BehindBy: 4}},
	}
	s := New(api, Options{})
	ctx := context.Background()
	for range 2 {
		if _, err := s.ContainedIn(ctx, "main", []string{"landed"}); err != nil {
			t.Fatal(err)
		}
	}
	if api.calls["ContainedIn"] != 2 {
		t.Errorf("ContainedIn was called %d times, want 2", api.calls["ContainedIn"])
	}
	if err := s.Save(); err != nil {
		t.Errorf("Save without a cache: %v", err)
	}
}

func TestLocalRepoNamesTheClone(t *testing.T) {
	if got := New(&fakeAPI{}, Options{}).LocalRepo(); got != "" {
		t.Errorf("LocalRepo without a clone = %q", got)
	}
	if got := New(&fakeAPI{}, Options{Git: &fakeGit{}}).LocalRepo(); got != "/tmp/clone" {
		t.Errorf("LocalRepo = %q", got)
	}
}
