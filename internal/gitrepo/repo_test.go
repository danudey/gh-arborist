package gitrepo

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/repository"
)

var target = repository.Repository{Host: "github.com", Owner: "owner", Name: "repo"}

// scratch builds a real repository to read, since the whole point of this
// package is what Git says about one.
func scratch(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "--initial-branch=main")
	run(t, dir, "config", "user.name", "Ada")
	run(t, dir, "config", "user.email", "ada@example.com")
	run(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_DATE=2020-03-04T05:06:07Z", "GIT_COMMITTER_DATE=2020-03-04T05:06:07Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, subject string) {
	t.Helper()
	run(t, dir, "commit", "--quiet", "--allow-empty", "-m", subject)
}

// local opens dir as if it were the repository being scanned. Branches are read
// from refs/heads, as they are in a bare clone we made ourselves.
func local(t *testing.T, dir string) *Repo {
	t.Helper()
	return &Repo{dir: dir, refPrefix: "refs/heads/", progress: func(string, ...any) {}}
}

func TestBranchesReadsTheTipCommit(t *testing.T) {
	dir := scratch(t)
	commit(t, dir, "first commit")
	run(t, dir, "checkout", "--quiet", "-b", "feature/nested-name")
	// A subject containing a tab would break a tab-separated format.
	commit(t, dir, "second\tcommit")

	branches, err := local(t, dir).Branches(context.Background())
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	byName := map[string]int{}
	for i, b := range branches {
		byName[b.Name] = i
	}
	if len(branches) != 2 {
		t.Fatalf("got %d branches, want 2: %v", len(branches), byName)
	}

	feature := branches[byName["feature/nested-name"]]
	if feature.Tip.MessageHeadline != "second\tcommit" {
		t.Errorf("subject is %q, want the tab preserved", feature.Tip.MessageHeadline)
	}
	if feature.Tip.Author.Email != "ada@example.com" || feature.Tip.Author.Name != "Ada" {
		t.Errorf("author is %+v, want Ada <ada@example.com>", feature.Tip.Author)
	}
	// Git cannot know which GitHub account an address belongs to; the API
	// fills that in.
	if feature.Tip.Author.Login != "" {
		t.Errorf("author login is %q, want it left for the API to resolve", feature.Tip.Author.Login)
	}
	if want := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC); !feature.Tip.CommittedDate.Equal(want) {
		t.Errorf("committed date is %v, want %v", feature.Tip.CommittedDate, want)
	}
	if len(feature.Tip.OID) != 40 && len(feature.Tip.OID) != 64 {
		t.Errorf("tip commit ID is %q, want a full object name", feature.Tip.OID)
	}
}

func TestContainedInFindsMergedBranches(t *testing.T) {
	dir := scratch(t)
	commit(t, dir, "one")
	run(t, dir, "checkout", "--quiet", "-b", "landed")
	commit(t, dir, "two")
	run(t, dir, "checkout", "--quiet", "main")
	run(t, dir, "merge", "--quiet", "--no-ff", "-m", "merge landed", "landed")
	commit(t, dir, "three")
	run(t, dir, "checkout", "--quiet", "-b", "in-flight")
	commit(t, dir, "unique work")
	run(t, dir, "branch", "same-as-main", "main")

	got, err := local(t, dir).ContainedIn(context.Background(), "main", []string{"landed", "in-flight", "same-as-main"})
	if err != nil {
		t.Fatalf("ContainedIn: %v", err)
	}

	landed, ok := got["landed"]
	if !ok {
		t.Fatalf("landed is missing from %v", got)
	}
	if !landed.Contained() || landed.Status != "BEHIND" {
		t.Errorf("landed is %+v, want contained and BEHIND", landed)
	}
	// main has the merge commit and "three" that landed does not.
	if landed.BehindBy != 2 {
		t.Errorf("landed is %d commits behind, want 2", landed.BehindBy)
	}
	if same, ok := got["same-as-main"]; !ok || same.Status != "IDENTICAL" {
		t.Errorf("same-as-main is %+v (present=%v), want IDENTICAL", same, ok)
	}
	// A branch with unique work is left out rather than described, because
	// nothing needs to know how far ahead it is.
	if cmp, ok := got["in-flight"]; ok {
		t.Errorf("in-flight was reported as %+v, want it omitted", cmp)
	}
}

func TestOpenRefusesAShallowClone(t *testing.T) {
	origin := scratch(t)
	commit(t, origin, "one")
	commit(t, origin, "two")

	shallow := filepath.Join(t.TempDir(), "shallow")
	run(t, t.TempDir(), "clone", "--quiet", "--depth=1", "--no-local", "file://"+origin, shallow)
	run(t, shallow, "remote", "set-url", "origin", "https://github.com/owner/repo.git")

	_, err := Open(context.Background(), target, Options{LocalPath: shallow, NoFetch: true})
	if err == nil {
		t.Fatal("a shallow clone was accepted; it cannot answer merge questions")
	}
	if !strings.Contains(err.Error(), "shallow") {
		t.Errorf("error is %q, want it to mention the clone being shallow", err)
	}
}

func TestOpenRejectsACloneOfSomewhereElse(t *testing.T) {
	dir := scratch(t)
	commit(t, dir, "one")
	run(t, dir, "remote", "set-url", "origin", "https://github.com/someone/other.git")

	if _, err := Open(context.Background(), target, Options{LocalPath: dir, NoFetch: true}); err == nil {
		t.Fatal("a clone of a different repository was accepted")
	}
}

// An explicitly named clone that turns out to be usable must be read through
// its remote-tracking refs, which are what the remote actually has.
func TestOpenUsesRemoteTrackingRefs(t *testing.T) {
	origin := scratch(t)
	commit(t, origin, "one")
	run(t, origin, "branch", "only-on-the-remote")

	clone := filepath.Join(t.TempDir(), "clone")
	run(t, t.TempDir(), "clone", "--quiet", "--no-local", "file://"+origin, clone)
	run(t, clone, "remote", "set-url", "origin", "https://github.com/owner/repo.git")
	// A branch that exists only locally is not the remote's business.
	run(t, clone, "checkout", "--quiet", "-b", "scratch-work")
	commit(t, clone, "local only")

	r, err := Open(context.Background(), target, Options{LocalPath: clone, NoFetch: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r.refPrefix != "refs/remotes/origin/" {
		t.Errorf("refPrefix is %q, want the remote-tracking refs", r.refPrefix)
	}
	branches, err := r.Branches(context.Background())
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	names := map[string]bool{}
	for _, b := range branches {
		names[b.Name] = true
	}
	if !names["only-on-the-remote"] || !names["main"] {
		t.Errorf("branches are %v, want the remote's branches", names)
	}
	if names["scratch-work"] {
		t.Error("a purely local branch was reported as one of the repository's")
	}
	if names["HEAD"] {
		t.Error("the symbolic HEAD ref was reported as a branch")
	}
}

// Somebody else's clone is read, not reconfigured: fetching with a
// partial-clone filter would write that setting into their repository. The
// remote is whichever one points at the repository, which need not be "origin".
func TestFetchLeavesSomebodyElsesCloneAlone(t *testing.T) {
	origin := scratch(t)
	commit(t, origin, "one")

	clone := filepath.Join(t.TempDir(), "clone")
	run(t, t.TempDir(), "clone", "--quiet", "--no-local", "file://"+origin, clone)
	run(t, clone, "remote", "rename", "origin", "upstream")
	commit(t, origin, "two")

	r := &Repo{
		dir:       clone,
		refPrefix: "refs/remotes/upstream/",
		remote:    "upstream",
		progress:  func(string, ...any) {},
	}
	if err := r.freshen(context.Background(), false); err != nil {
		t.Fatalf("freshen: %v", err)
	}

	branches, err := r.Branches(context.Background())
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(branches) != 1 || branches[0].Tip.MessageHeadline != "two" {
		t.Errorf("branches are %+v, want the remote's new commit, so the named remote was fetched", branches)
	}
	for _, key := range []string{"remote.upstream.promisor", "remote.upstream.partialclonefilter"} {
		if got := tryRun(t, clone, "config", "--get", key); got != "" {
			t.Errorf("the fetch set %s to %q, turning somebody else's clone into a partial one", key, got)
		}
	}
}

// tryRun runs git and returns its output, treating a non-zero exit as an empty
// answer rather than a failure.
func tryRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// A clone of a fork usually has the fork as "origin" and the repository being
// scanned as "upstream". Only the latter's branches are the subject of the
// report, and the fork's must not leak in.
func TestOnlyTheMatchingRemotesBranchesAreRead(t *testing.T) {
	origin := scratch(t)
	commit(t, origin, "one")
	run(t, origin, "branch", "upstream-branch")

	fork := scratch(t)
	commit(t, fork, "one")
	run(t, fork, "branch", "fork-branch")

	clone := filepath.Join(t.TempDir(), "clone")
	run(t, t.TempDir(), "clone", "--quiet", "--no-local", "file://"+origin, clone)
	run(t, clone, "remote", "rename", "origin", "upstream")
	run(t, clone, "remote", "set-url", "upstream", "https://github.com/owner/repo.git")
	// The fork is fetched under its own name, as "gh repo fork" would leave it.
	run(t, clone, "remote", "add", "origin", "file://"+fork)
	run(t, clone, "fetch", "--quiet", "origin")

	r, err := Open(context.Background(), target, Options{LocalPath: clone, NoFetch: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r.remote != "upstream" || r.refPrefix != "refs/remotes/upstream/" {
		t.Fatalf("read remote %q under %q, want upstream's refs", r.remote, r.refPrefix)
	}
	names := branchNames(t, r)
	if !names["upstream-branch"] {
		t.Errorf("branches are %v, want the scanned repository's", names)
	}
	if names["fork-branch"] {
		t.Error("a branch that only exists on the user's fork was reported as the repository's")
	}
}

// The same must hold for a bare repository, where refs/heads can hold
// something else entirely and only the remote's refspec says where its
// branches went.
func TestBareRepositoryFollowsTheRemotesRefspec(t *testing.T) {
	origin := scratch(t)
	commit(t, origin, "one")
	run(t, origin, "branch", "upstream-branch")

	bare := filepath.Join(t.TempDir(), "bare.git")
	run(t, t.TempDir(), "init", "--quiet", "--bare", bare)
	run(t, bare, "config", "remote.upstream.url", "https://github.com/owner/repo.git")
	run(t, bare, "config", "remote.upstream.fetch", "+refs/heads/*:refs/remotes/upstream/*")
	// Something unrelated is sitting in refs/heads.
	run(t, bare, "remote", "add", "elsewhere", "file://"+origin)
	run(t, bare, "fetch", "--quiet", "elsewhere", "+refs/heads/main:refs/heads/not-the-repository")
	run(t, bare, "fetch", "--quiet", "elsewhere", "+refs/heads/*:refs/remotes/upstream/*")

	r, err := Open(context.Background(), target, Options{LocalPath: bare, NoFetch: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r.refPrefix != "refs/remotes/upstream/" {
		t.Fatalf("refPrefix is %q, want the refspec's destination", r.refPrefix)
	}
	names := branchNames(t, r)
	if !names["upstream-branch"] {
		t.Errorf("branches are %v, want the remote's", names)
	}
	if names["not-the-repository"] {
		t.Error("a ref that happened to sit in refs/heads was reported as a branch")
	}
}

func branchNames(t *testing.T, r *Repo) map[string]bool {
	t.Helper()
	branches, err := r.Branches(context.Background())
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	names := map[string]bool{}
	for _, b := range branches {
		names[b.Name] = true
	}
	return names
}

// A remote written against an ssh alias — "git@gh-work:owner/repo" with a
// HostName stanza — is the same repository, and worth using rather than
// falling back to the API.
func TestSSHAliasResolvesToTheRepository(t *testing.T) {
	dir := scratch(t)
	commit(t, dir, "one")
	run(t, dir, "remote", "set-url", "origin", "git@gh-work:owner/repo.git")

	r := &Repo{dir: dir, progress: func(string, ...any) {}}
	// Nothing is asked of ssh until a remote's owner and name already match.
	var asked []string
	r.hostResolver = func(_ context.Context, host string) string {
		asked = append(asked, host)
		if host == "gh-work" {
			return "github.com"
		}
		return host
	}

	remote, prefix, err := r.remoteFor(context.Background(), target)
	if err != nil {
		t.Fatalf("remoteFor: %v", err)
	}
	if remote != "origin" || prefix != "refs/remotes/origin/" {
		t.Errorf("got remote %q under %q, want origin", remote, prefix)
	}
	if len(asked) != 1 || asked[0] != "gh-work" {
		t.Errorf("ssh was asked about %v, want only the aliased host", asked)
	}
}

// An alias belonging to somebody else's host is not this repository, however
// much the owner and name coincide.
func TestSSHAliasForAnotherHostDoesNotMatch(t *testing.T) {
	dir := scratch(t)
	commit(t, dir, "one")
	run(t, dir, "remote", "set-url", "origin", "git@gh-work:owner/repo.git")

	r := &Repo{dir: dir, progress: func(string, ...any) {}}
	r.hostResolver = func(context.Context, string) string { return "gitlab.example.com" }

	if _, _, err := r.remoteFor(context.Background(), target); err == nil {
		t.Error("an alias for a different host was accepted as the repository")
	}
}

// A remote whose host is spelled out must win over an aliased one, whatever
// order the config lists them in.
func TestPlainHostWinsOverAnAlias(t *testing.T) {
	dir := scratch(t)
	commit(t, dir, "one")
	run(t, dir, "remote", "set-url", "origin", "git@gh-work:owner/repo.git")
	run(t, dir, "remote", "add", "canonical", "https://github.com/owner/repo.git")

	r := &Repo{dir: dir, progress: func(string, ...any) {}}
	r.hostResolver = func(context.Context, string) string { return "github.com" }

	remote, _, err := r.remoteFor(context.Background(), target)
	if err != nil {
		t.Fatalf("remoteFor: %v", err)
	}
	if remote != "canonical" {
		t.Errorf("chose %q, want the remote that needed no ssh lookup", remote)
	}
}

// https:// and git:// do not consult the ssh config, so an alias in one of
// those is not an alias at all.
func TestOnlySSHRemotesConsultTheSSHConfig(t *testing.T) {
	dir := scratch(t)
	commit(t, dir, "one")
	run(t, dir, "remote", "set-url", "origin", "https://gh-work/owner/repo.git")

	r := &Repo{dir: dir, progress: func(string, ...any) {}}
	called := false
	r.hostResolver = func(context.Context, string) string {
		called = true
		return "github.com"
	}
	if _, _, err := r.remoteFor(context.Background(), target); err == nil {
		t.Error("an https remote was matched through the ssh config")
	}
	if called {
		t.Error("ssh was consulted for an https remote")
	}
}

func TestIsSSHRemote(t *testing.T) {
	for url, want := range map[string]bool{
		"git@github.com:owner/repo.git":       true,
		"gh-work:owner/repo.git":              true,
		"ssh://git@github.com/owner/repo.git": true,
		"https://github.com/owner/repo.git":   false,
		"git://github.com/owner/repo.git":     false,
		"/srv/git/repo.git":                   false,
		"../sibling.git":                      false,
	} {
		if got := isSSHRemote(url); got != want {
			t.Errorf("isSSHRemote(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestParseSSHHostname(t *testing.T) {
	out := "user git\nhostname github.com\nport 22\nhostkeyalias \n"
	if got := parseSSHHostname([]byte(out)); got != "github.com" {
		t.Errorf("parseSSHHostname = %q, want github.com", got)
	}
	if got := parseSSHHostname([]byte("user git\nport 22\n")); got != "" {
		t.Errorf("parseSSHHostname with no hostname = %q, want empty", got)
	}
}

// A remote URL is repository config, so a host that could be read as an option
// must never reach ssh's command line.
func TestPlausibleHostname(t *testing.T) {
	for host, want := range map[string]bool{
		"github.com":                      true,
		"gh-work":                         true,
		"ssh.github.com":                  true,
		"my_host.example.com":             true,
		"-oProxyCommand=touch /tmp/pwned": false,
		"-G":                              false,
		"host;rm -rf /":                   false,
		"host name":                       false,
		"":                                false,
		strings.Repeat("a", 254):          false,
	} {
		if got := plausibleHostname(host); got != want {
			t.Errorf("plausibleHostname(%q) = %v, want %v", host, got, want)
		}
	}
}

// The real command and its output, so that a change in either is noticed. A
// host nobody configures resolves to itself.
func TestSSHHostnameAsksRealSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	const host = "arborist-test-no-such-alias.invalid"
	r := &Repo{progress: func(string, ...any) {}}
	if got := r.sshHostname(context.Background(), host); got != host {
		t.Errorf("sshHostname(%q) = %q, want it echoed back unchanged", host, got)
	}
	if got := r.sshHostname(context.Background(), "-G"); got != "" {
		t.Errorf("sshHostname refused nothing: got %q", got)
	}
}

func TestBranchDestination(t *testing.T) {
	tests := []struct {
		spec string
		want string
	}{
		{"+refs/heads/*:refs/remotes/origin/*", "refs/remotes/origin/"},
		{"refs/heads/*:refs/remotes/upstream/*", "refs/remotes/upstream/"},
		{"+refs/heads/*:refs/heads/*", "refs/heads/"},
		{"+refs/*:refs/*", "refs/heads/"}, // a mirror
		{"+refs/heads/main:refs/heads/main", ""},
		{"+refs/pull/*/head:refs/remotes/origin/pr/*", ""},
		{"nonsense", ""},
	}
	for _, tt := range tests {
		got, ok := branchDestination(tt.spec)
		if tt.want == "" {
			if ok {
				t.Errorf("branchDestination(%q) = %q, want it rejected", tt.spec, got)
			}
			continue
		}
		if !ok || got != tt.want {
			t.Errorf("branchDestination(%q) = %q, %v; want %q, true", tt.spec, got, ok, tt.want)
		}
	}
}

func TestOpenIgnoresGitWhenAsked(t *testing.T) {
	r, err := Open(context.Background(), target, Options{Mode: ModeNever, LocalPath: scratch(t)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r != nil {
		t.Errorf("--git never returned %v, want nothing", r)
	}
}

func TestParseMode(t *testing.T) {
	for _, in := range Modes() {
		if _, err := ParseMode(in); err != nil {
			t.Errorf("ParseMode(%q): %v", in, err)
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

func TestMatchesRepo(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://github.com/owner/repo.git", true},
		{"https://github.com/owner/repo", true},
		{"https://user@github.com/Owner/Repo.git", true},
		{"git@github.com:owner/repo.git", true},
		{"ssh://git@github.com/owner/repo.git", true},
		{"ssh://git@ssh.github.com:443/owner/repo.git", true},
		{"git://github.com/owner/repo.git", true},
		{"https://github.com/owner/other.git", false},
		{"https://github.com/other/repo.git", false},
		{"https://gitlab.com/owner/repo.git", false},
		{"https://github.com/repo.git", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := matchesRepo(tt.url, target); got != tt.want {
			t.Errorf("matchesRepo(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestRemoteURL(t *testing.T) {
	if got := remoteURL(target); got != "https://github.com/owner/repo.git" {
		t.Errorf("remoteURL = %q", got)
	}
	ghe := repository.Repository{Host: "github.example.com", Owner: "o", Name: "n"}
	if got := remoteURL(ghe); got != "https://github.example.com/o/n.git" {
		t.Errorf("remoteURL for an enterprise host = %q", got)
	}
}
