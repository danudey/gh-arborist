package checks

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danudey/gh-arborist/internal/gh"
	"github.com/danudey/gh-arborist/internal/timeutil"
)

var now = time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

// fakeSource is a Source backed by fixed data, so check logic can be tested
// without the GitHub API.
type fakeSource struct {
	branches  []gh.Branch
	prsByHead map[string][]gh.PullRequest
	// containment maps base -> head -> comparison.
	containment map[string]map[string]gh.Comparison
	collabs     []string
	prs         []gh.PullRequest
	issues      []gh.Issue

	// requestedBases records which bases were compared against.
	requestedBases []string
}

func (f *fakeSource) Branches(context.Context) ([]gh.Branch, error) { return f.branches, nil }

func (f *fakeSource) PullRequestsForBranches(_ context.Context, names []string) (map[string][]gh.PullRequest, error) {
	out := map[string][]gh.PullRequest{}
	for _, n := range names {
		if prs, ok := f.prsByHead[n]; ok {
			out[n] = prs
		}
	}
	return out, nil
}

func (f *fakeSource) ContainedIn(_ context.Context, base string, heads []string) (map[string]gh.Comparison, error) {
	f.requestedBases = append(f.requestedBases, base)
	out := map[string]gh.Comparison{}
	for _, h := range heads {
		if cmp, ok := f.containment[base][h]; ok {
			out[h] = cmp
		} else {
			out[h] = gh.Comparison{Status: "DIVERGED", AheadBy: 3, BehindBy: 4}
		}
	}
	return out, nil
}

func (f *fakeSource) Collaborators(context.Context) (map[string]gh.Actor, error) {
	out := map[string]gh.Actor{}
	for _, login := range f.collabs {
		out[strings.ToLower(login)] = gh.Actor{Login: login, Type: "User"}
	}
	return out, nil
}

func (f *fakeSource) PullRequests(_ context.Context, states []string, _ int) ([]gh.PullRequest, error) {
	wanted := map[string]bool{}
	for _, s := range states {
		wanted[s] = true
	}
	var out []gh.PullRequest
	for _, pr := range f.prs {
		if wanted[pr.State] {
			out = append(out, pr)
		}
	}
	return out, nil
}

func (f *fakeSource) Issues(_ context.Context, states []string, _ int) ([]gh.Issue, error) {
	wanted := map[string]bool{}
	for _, s := range states {
		wanted[s] = true
	}
	var out []gh.Issue
	for _, iss := range f.issues {
		if wanted[iss.State] {
			out = append(out, iss)
		}
	}
	return out, nil
}

func branch(name string, ageDays int, login string) gh.Branch {
	return gh.Branch{
		Name: name,
		Tip: gh.Commit{
			OID:             "oid-" + name,
			CommittedDate:   now.AddDate(0, 0, -ageDays),
			MessageHeadline: "work on " + name,
			Author:          gh.Actor{Login: login, Type: "User", Name: login},
		},
	}
}

func pr(number int, state, head, author string, resolvedDaysAgo int) gh.PullRequest {
	when := now.AddDate(0, 0, -resolvedDaysAgo)
	p := gh.PullRequest{
		Number:      number,
		State:       state,
		Title:       "pr " + head,
		HeadRefName: head,
		HeadOwner:   "acme",
		Author:      gh.Actor{Login: author, Type: "User"},
		UpdatedAt:   when,
	}
	switch state {
	case "MERGED":
		p.MergedAt, p.ClosedAt = &when, &when
	case "CLOSED":
		p.ClosedAt = &when
	}
	return p
}

func newTestRunner(t *testing.T, src *fakeSource, opts Options) *Runner {
	t.Helper()
	if opts.Age.IsZero() {
		opts.Age, _ = timeutil.ParseAge("1y")
	}
	info := gh.Info{
		NameWithOwner:    "acme/widgets",
		URL:              "https://github.com/acme/widgets",
		IsPrivate:        true,
		ViewerPermission: "ADMIN",
		DefaultBranch:    "main",
	}
	r, err := NewRunner(src, info, opts, now)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

// run executes checks and returns findings keyed by name for easy assertions.
func run(t *testing.T, src *fakeSource, opts Options, ids ...string) map[string]Finding {
	t.Helper()
	r := newTestRunner(t, src, opts)
	res, err := r.Run(context.Background(), ids)
	if err != nil {
		t.Fatalf("Run(%v): %v", ids, err)
	}
	out := map[string]Finding{}
	for _, f := range res.Findings {
		out[f.Name] = f
	}
	return out
}

func TestClosedPRsCheck(t *testing.T) {
	src := &fakeSource{
		branches: []gh.Branch{
			branch("main", 1, "alice"),
			branch("feature/merged", 40, "alice"),
			branch("feature/rejected", 50, "bob"),
			branch("feature/still-open", 5, "carol"),
			branch("feature/no-pr", 10, "dave"),
			branch("feature/fork-only", 10, "erin"),
		},
		prsByHead: map[string][]gh.PullRequest{
			"feature/merged":     {pr(10, "MERGED", "feature/merged", "alice", 30)},
			"feature/rejected":   {pr(11, "CLOSED", "feature/rejected", "bob", 20)},
			"feature/still-open": {pr(12, "CLOSED", "feature/still-open", "carol", 9), pr(13, "OPEN", "feature/still-open", "carol", 1)},
			"feature/fork-only":  {forkPR(14, "MERGED", "feature/fork-only", "mallory")},
		},
	}

	got := run(t, src, Options{}, "closed-prs")

	if f, ok := got["feature/merged"]; !ok {
		t.Error("a branch whose pull request was merged should be flagged")
	} else if f.Action() != ActionDelete {
		t.Errorf("merged pull request suggested %q, want %q", f.Action(), ActionDelete)
	}

	if f, ok := got["feature/rejected"]; !ok {
		t.Error("a branch whose pull request was closed unmerged should be flagged")
	} else if f.Action() != ActionReview {
		t.Errorf("closed-unmerged pull request suggested %q, want %q", f.Action(), ActionReview)
	}

	for _, name := range []string{"main", "feature/still-open", "feature/no-pr", "feature/fork-only"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s should not be flagged, but was: %s", name, got[name].Why())
		}
	}
}

func forkPR(number int, state, head, author string) gh.PullRequest {
	p := pr(number, state, head, author, 5)
	p.IsCrossRepository = true
	p.HeadOwner = author
	return p
}

func TestClosedPRsMergedOnly(t *testing.T) {
	src := &fakeSource{
		branches: []gh.Branch{
			branch("main", 1, "alice"),
			branch("feature/rejected", 50, "bob"),
		},
		prsByHead: map[string][]gh.PullRequest{
			"feature/rejected": {pr(11, "CLOSED", "feature/rejected", "bob", 20)},
		},
	}
	got := run(t, src, Options{MergedOnly: true}, "closed-prs")
	if _, ok := got["feature/rejected"]; ok {
		t.Error("--merged-only should ignore pull requests closed without merging")
	}
}

func TestMergedCheck(t *testing.T) {
	src := &fakeSource{
		branches: []gh.Branch{
			branch("main", 1, "alice"),
			branch("feature/landed", 30, "alice"),
			branch("feature/diverged", 30, "bob"),
		},
		containment: map[string]map[string]gh.Comparison{
			"main": {
				"feature/landed": {Status: "BEHIND", AheadBy: 0, BehindBy: 12},
			},
		},
	}
	got := run(t, src, Options{}, "merged")

	if f, ok := got["feature/landed"]; !ok {
		t.Error("a fully merged branch should be flagged")
	} else {
		if f.Action() != ActionDelete {
			t.Errorf("merged branch suggested %q, want %q", f.Action(), ActionDelete)
		}
		if !strings.Contains(f.Why(), "12 commits behind") {
			t.Errorf("reason should say how far behind the branch is, got %q", f.Why())
		}
	}
	if _, ok := got["feature/diverged"]; ok {
		t.Error("a branch with unique commits should not be flagged")
	}
	if _, ok := got["main"]; ok {
		t.Error("the default branch should never be flagged")
	}
}

// A branch that only shares its tip with another branch is a duplicate, and
// must be detectable without any comparison request. The shorter name is kept,
// since duplicates are normally made by suffixing an existing name.
func TestMergedCheckFindsDuplicateTips(t *testing.T) {
	orig := branch("fix-bug", 20, "alice")
	dup := branch("fix-bug-1", 20, "bob")
	dup.Tip.OID = orig.Tip.OID

	src := &fakeSource{branches: []gh.Branch{branch("main", 1, "alice"), orig, dup}}
	got := run(t, src, Options{}, "merged")

	f, ok := got["fix-bug-1"]
	if !ok {
		t.Fatal("the duplicate branch should be flagged")
	}
	if !strings.Contains(f.Why(), "fix-bug") {
		t.Errorf("reason should name the branch that is kept, got %q", f.Why())
	}
	if _, ok := got["fix-bug"]; ok {
		t.Error("only one branch of a duplicate pair should be flagged")
	}
}

// A branch identical to the default branch must keep the default branch, not
// report it as the duplicate.
func TestMergedCheckKeepsDefaultBranch(t *testing.T) {
	main := branch("main", 1, "alice")
	copyOfMain := branch("aaa-copy-of-main", 1, "bob")
	copyOfMain.Tip.OID = main.Tip.OID

	src := &fakeSource{branches: []gh.Branch{main, copyOfMain}}
	got := run(t, src, Options{}, "merged")

	if _, ok := got["main"]; ok {
		t.Error("the default branch must never be reported as the duplicate")
	}
	if f, ok := got["aaa-copy-of-main"]; !ok {
		t.Error("the copy should be flagged")
	} else if !strings.Contains(f.Why(), "main") {
		t.Errorf("reason should name main as the branch kept, got %q", f.Why())
	}
}

func TestMergedCheckBaseSelection(t *testing.T) {
	src := &fakeSource{
		branches: []gh.Branch{
			branch("main", 1, "alice"),
			branch("release/1.0", 5, "alice"),
			branch("feature/x", 30, "bob"),
		},
		containment: map[string]map[string]gh.Comparison{
			"release/1.0": {"feature/x": {Status: "BEHIND", AheadBy: 0, BehindBy: 3}},
		},
	}
	got := run(t, src, Options{Bases: []string{"release/*"}}, "merged")

	if f, ok := got["feature/x"]; !ok {
		t.Error("a branch merged into the named base should be flagged")
	} else if !strings.Contains(f.Why(), "release/1.0") {
		t.Errorf("reason should name the base, got %q", f.Why())
	}
	if _, ok := got["release/1.0"]; ok {
		t.Error("a branch named as a base must not be reported")
	}
	if len(src.requestedBases) != 1 || src.requestedBases[0] != "release/1.0" {
		t.Errorf("compared against %v, want only release/1.0", src.requestedBases)
	}
}

func TestStaleCheck(t *testing.T) {
	src := &fakeSource{
		branches: []gh.Branch{
			branch("main", 900, "alice"),
			branch("feature/ancient", 400, "alice"),
			branch("feature/recent", 200, "bob"),
		},
	}
	got := run(t, src, Options{}, "stale")

	if f, ok := got["feature/ancient"]; !ok {
		t.Error("a branch older than the threshold should be flagged")
	} else if f.Action() != ActionReview {
		t.Errorf("stale branch suggested %q, want %q: age alone is not proof it is safe to delete", f.Action(), ActionReview)
	}
	if _, ok := got["feature/recent"]; ok {
		t.Error("a branch inside the threshold should not be flagged")
	}
	if _, ok := got["main"]; ok {
		t.Error("the default branch should never be flagged, however old it is")
	}
}

func TestStaleCheckThreshold(t *testing.T) {
	src := &fakeSource{branches: []gh.Branch{
		branch("main", 1, "alice"),
		branch("feature/x", 100, "alice"),
	}}

	age, _ := timeutil.ParseAge("90d")
	if _, ok := run(t, src, Options{Age: age}, "stale")["feature/x"]; !ok {
		t.Error("a 100-day-old branch should be stale against a 90d threshold")
	}

	age, _ = timeutil.ParseAge("6mo")
	if _, ok := run(t, src, Options{Age: age}, "stale")["feature/x"]; ok {
		t.Error("a 100-day-old branch should not be stale against a 6mo threshold")
	}
}

func TestExcludeAndProtection(t *testing.T) {
	protected := branch("keep-me", 900, "alice")
	protected.Protected = true

	src := &fakeSource{branches: []gh.Branch{
		branch("main", 1, "alice"),
		branch("dependabot/npm/lodash", 900, "alice"),
		branch("feature/wip", 900, "bob"),
		protected,
	}}

	got := run(t, src, Options{Excludes: []string{"dependabot/*"}}, "stale")
	if _, ok := got["dependabot/npm/lodash"]; ok {
		t.Error("--exclude 'dependabot/*' should have excluded the branch")
	}
	if _, ok := got["keep-me"]; ok {
		t.Error("a branch that protection forbids deleting should be skipped by default")
	}
	if _, ok := got["feature/wip"]; !ok {
		t.Error("an unexcluded stale branch should still be flagged")
	}

	got = run(t, src, Options{IncludeProtected: true}, "stale")
	if _, ok := got["keep-me"]; !ok {
		t.Error("--include-protected should report protected branches")
	}
}

func TestOrphansCheck(t *testing.T) {
	src := &fakeSource{
		branches: []gh.Branch{
			branch("main", 1, "alice"),
			branch("feature/departed", 30, "bob"),
			branch("feature/present", 30, "alice"),
			branch("feature/bot", 30, "dependabot[bot]"),
		},
		collabs: []string{"alice", "carol"},
		prs: []gh.PullRequest{
			pr(20, "OPEN", "feature/departed", "bob", 1),
			pr(21, "OPEN", "feature/present", "alice", 1),
			pr(22, "MERGED", "old", "bob", 100),
		},
		issues: []gh.Issue{
			{Number: 30, State: "OPEN", Title: "still broken", Author: gh.Actor{Login: "bob", Type: "User"}, UpdatedAt: now},
			{Number: 31, State: "OPEN", Title: "assigned away", Author: gh.Actor{Login: "alice", Type: "User"},
				Assignees: []gh.Actor{{Login: "bob", Type: "User"}}, UpdatedAt: now},
			{Number: 32, State: "OPEN", Title: "fine", Author: gh.Actor{Login: "carol", Type: "User"}, UpdatedAt: now},
			{Number: 33, State: "CLOSED", Title: "done", Author: gh.Actor{Login: "bob", Type: "User"}, UpdatedAt: now},
		},
	}
	src.prsByHead = map[string][]gh.PullRequest{}

	got := run(t, src, Options{}, "orphans")

	if _, ok := got["feature/departed"]; !ok {
		t.Error("a branch owned by somebody without access should be flagged")
	}
	if _, ok := got["feature/present"]; ok {
		t.Error("a branch owned by a collaborator should not be flagged")
	}
	if _, ok := got["feature/bot"]; ok {
		t.Error("app accounts should be ignored unless --include-bots is set")
	}

	if f, ok := got["#20"]; !ok {
		t.Error("an open pull request from a departed author should be flagged")
	} else if f.Action() != ActionReassign {
		t.Errorf("orphaned pull request suggested %q, want %q", f.Action(), ActionReassign)
	}
	if _, ok := got["#21"]; ok {
		t.Error("a pull request from a current collaborator should not be flagged")
	}
	if _, ok := got["#22"]; ok {
		t.Error("closed pull requests should be ignored unless --include-closed is set")
	}

	if _, ok := got["#30"]; !ok {
		t.Error("an issue opened by a departed author should be flagged")
	}
	if f, ok := got["#31"]; !ok {
		t.Error("an issue assigned to a departed user should be flagged")
	} else if f.Action() != ActionReassign {
		t.Errorf("orphaned assignee suggested %q, want %q", f.Action(), ActionReassign)
	}
	if _, ok := got["#32"]; ok {
		t.Error("an issue with a current author and no assignees should not be flagged")
	}
	if _, ok := got["#33"]; ok {
		t.Error("closed issues should be ignored unless --include-closed is set")
	}
}

func TestOrphansCheckIgnoreUser(t *testing.T) {
	src := &fakeSource{
		branches:  []gh.Branch{branch("main", 1, "alice"), branch("feature/bob", 30, "bob")},
		prsByHead: map[string][]gh.PullRequest{},
		collabs:   []string{"alice"},
	}
	if _, ok := run(t, src, Options{IgnoreUsers: []string{"@bob"}}, "orphans")["feature/bob"]; ok {
		t.Error("--ignore-user should treat the login as still present, with or without a leading @")
	}
}

func TestOrphansCheckDeletedAccount(t *testing.T) {
	src := &fakeSource{
		branches:  []gh.Branch{branch("main", 1, "alice")},
		prsByHead: map[string][]gh.PullRequest{},
		collabs:   []string{"alice"},
		issues: []gh.Issue{
			{Number: 40, State: "OPEN", Title: "orphaned", Author: gh.Actor{Login: "ghost"}, UpdatedAt: now},
		},
	}
	f, ok := run(t, src, Options{}, "orphans")["#40"]
	if !ok {
		t.Fatal("an item from a deleted account should be flagged")
	}
	if !strings.Contains(f.Why(), "deleted") {
		t.Errorf("reason should say the account was deleted, got %q", f.Why())
	}
}

// Running every check must report each branch once, with the reasons merged and
// the most decisive action winning.
func TestAllChecksMergeReasons(t *testing.T) {
	src := &fakeSource{
		branches: []gh.Branch{
			branch("main", 1, "alice"),
			branch("feature/everything", 500, "bob"),
		},
		prsByHead: map[string][]gh.PullRequest{
			"feature/everything": {pr(50, "MERGED", "feature/everything", "bob", 480)},
		},
		containment: map[string]map[string]gh.Comparison{
			"main": {"feature/everything": {Status: "BEHIND", AheadBy: 0, BehindBy: 7}},
		},
		collabs: []string{"alice"},
	}

	r := newTestRunner(t, src, Options{})
	res, err := r.Run(context.Background(), []string{"closed-prs", "merged", "stale", "orphans"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var branchFindings []Finding
	for _, f := range res.Findings {
		if f.Kind == KindBranch {
			branchFindings = append(branchFindings, f)
		}
	}
	if len(branchFindings) != 1 {
		t.Fatalf("got %d branch findings, want 1 merged entry: %+v", len(branchFindings), branchFindings)
	}

	f := branchFindings[0]
	gotChecks := f.Checks()
	sort.Strings(gotChecks)
	want := []string{"closed-prs", "merged", "orphans", "stale"}
	if strings.Join(gotChecks, ",") != strings.Join(want, ",") {
		t.Errorf("finding covers checks %v, want %v", gotChecks, want)
	}
	if f.Action() != ActionDelete {
		t.Errorf("action is %q, want %q: deleting settles every other reason", f.Action(), ActionDelete)
	}
}

func TestRunnerFetchesEachThingOnce(t *testing.T) {
	src := &countingSource{fakeSource: fakeSource{
		branches:  []gh.Branch{branch("main", 1, "alice"), branch("feature/x", 500, "bob")},
		prsByHead: map[string][]gh.PullRequest{},
		collabs:   []string{"alice"},
	}}

	r := newTestRunner(t, &src.fakeSource, Options{})
	r.Client = src
	if _, err := r.Run(context.Background(), []string{"closed-prs", "merged", "stale", "orphans"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if src.branchCalls != 1 {
		t.Errorf("Branches called %d times across four checks, want 1", src.branchCalls)
	}
	if src.prCalls != 1 {
		t.Errorf("PullRequestsForBranches called %d times across four checks, want 1", src.prCalls)
	}
}

type countingSource struct {
	fakeSource
	branchCalls int
	prCalls     int
}

func (c *countingSource) Branches(ctx context.Context) ([]gh.Branch, error) {
	c.branchCalls++
	return c.fakeSource.Branches(ctx)
}

func (c *countingSource) PullRequestsForBranches(ctx context.Context, names []string) (map[string][]gh.PullRequest, error) {
	c.prCalls++
	return c.fakeSource.PullRequestsForBranches(ctx, names)
}

func TestUnknownCheck(t *testing.T) {
	src := &fakeSource{branches: []gh.Branch{branch("main", 1, "alice")}}
	r := newTestRunner(t, src, Options{})
	if _, err := r.Run(context.Background(), []string{"nope"}); err == nil {
		t.Error("running an unknown check should fail")
	}
}

// A pull request whose author has gone but whose assignee has not is not
// orphaned: somebody who can still push is on it.
func TestOrphansCheckPullRequestAssignedToPresentUser(t *testing.T) {
	assigned := pr(20, "OPEN", "feature/departed", "bob", 1)
	assigned.Assignees = []gh.Actor{{Login: "alice", Type: "User"}}
	mixed := pr(21, "OPEN", "feature/mixed", "bob", 1)
	mixed.Assignees = []gh.Actor{{Login: "alice", Type: "User"}, {Login: "dave", Type: "User"}}
	unassigned := pr(22, "OPEN", "feature/alone", "bob", 1)
	botAssigned := pr(23, "OPEN", "feature/bot", "bob", 1)
	botAssigned.Assignees = []gh.Actor{{Login: "dependabot[bot]", Type: "Bot"}}

	src := &fakeSource{
		branches:  []gh.Branch{branch("main", 1, "alice")},
		prsByHead: map[string][]gh.PullRequest{},
		collabs:   []string{"alice"},
		prs:       []gh.PullRequest{assigned, mixed, unassigned, botAssigned},
	}
	got := run(t, src, Options{}, "orphans")

	if _, ok := got["#20"]; ok {
		t.Error("a pull request assigned to somebody who still has access should not be a candidate")
	}
	if f, ok := got["#21"]; !ok {
		t.Error("a departed assignee is still worth reporting")
	} else if strings.Contains(f.Why(), "opened by") {
		t.Errorf("the author should not be reported when a live assignee is on it, got %q", f.Why())
	}
	if _, ok := got["#22"]; !ok {
		t.Error("a pull request from a departed author with no assignee should be flagged")
	}
	if _, ok := got["#23"]; !ok {
		t.Error("assigning a bot does not give the work an owner, so it should still be flagged")
	}
}

// Every check, not just "merged", must leave the branches used as comparison
// bases alone.
func TestBasesAreExcludedFromEveryCheck(t *testing.T) {
	src := &fakeSource{
		branches: []gh.Branch{
			branch("main", 1, "alice"),
			branch("release/1.0", 900, "bob"),
			branch("feature/x", 900, "bob"),
		},
		prsByHead: map[string][]gh.PullRequest{},
		collabs:   []string{"alice"},
	}
	got := run(t, src, Options{Bases: []string{"release/*"}}, "stale", "orphans")

	if _, ok := got["release/1.0"]; ok {
		t.Error("a branch matching --base should be excluded from every check, not just merged")
	}
	if _, ok := got["feature/x"]; !ok {
		t.Error("other branches should still be checked")
	}
}

func TestFindingCategoriesAndGrouping(t *testing.T) {
	res := NewResult("acme/widgets", now)
	res.Add(Finding{
		Kind: KindBranch,
		Name: "feature/both",
		Reasons: []Reason{
			{Check: "stale", Category: CategoryStale, Summary: "old", Action: ActionReview},
			{Check: "merged", Category: CategoryMerged, Summary: "merged", Action: ActionDelete},
		},
	})
	res.Add(Finding{
		Kind:    KindBranch,
		Name:    "feature/one",
		Reasons: []Reason{{Check: "stale", Category: CategoryStale, Summary: "old", Action: ActionReview}},
	})
	res.Add(Finding{
		Kind:    KindBranch,
		Name:    "feature/uncategorised",
		Reasons: []Reason{{Check: "stale", Summary: "old", Action: ActionReview}},
	})
	res.Sort()

	// Categories come back in report order, not in the order the reasons were
	// added.
	if got := res.Findings[0].Categories(); len(got) != 2 || got[0] != CategoryMerged || got[1] != CategoryStale {
		t.Errorf("Categories() = %v", got)
	}

	groups := res.GroupsOfKind(KindBranch)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want merged, stale and other: %+v", len(groups), groups)
	}
	if groups[0].Category != CategoryMerged || groups[0].Title != "Fully merged into another branch" {
		t.Errorf("first group = %+v", groups[0])
	}
	if len(groups[0].Findings) != 1 || groups[0].Findings[0].Name != "feature/both" {
		t.Errorf("merged group holds %+v", groups[0].Findings)
	}
	// The branch that matched two indicators is listed under both.
	if len(groups[1].Findings) != 2 {
		t.Errorf("stale group holds %d findings, want 2", len(groups[1].Findings))
	}
	if groups[2].Category != CategoryOther || len(groups[2].Findings) != 1 {
		t.Errorf("an uncategorised reason should fall into %q, got %+v", CategoryOther, groups[2])
	}
	if len(res.GroupsOfKind(KindIssue)) != 0 {
		t.Error("a kind with no findings should produce no groups")
	}
}
