// Package gitrepo reads the parts of a repository that Git already knows,
// without asking GitHub.
//
// Branch names, tip commits, commit dates, subjects, authors and — most
// usefully — whether one branch is an ancestor of another are all plain Git
// facts. Answering them locally removes the API's per-branch cost entirely: the
// merged check goes from one comparison per branch per base to one local
// command per base.
//
// What Git cannot answer is which GitHub account an address belongs to, and
// whether a protection rule forbids deleting a branch. Those still come from
// the API.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/danudey/gh-arborist/internal/cache"
	"github.com/danudey/gh-arborist/internal/gh"
)

// Mode says how hard to try to find a local copy of the repository.
type Mode string

const (
	// ModeAuto uses a local clone when one is already available, and otherwise
	// leaves everything to the API.
	ModeAuto Mode = "auto"
	// ModeNever ignores Git entirely.
	ModeNever Mode = "never"
	// ModeClone creates or updates a treeless bare clone under the cache
	// directory when no other clone is available.
	ModeClone Mode = "clone"
)

// Modes lists the accepted values of --git.
func Modes() []string { return []string{string(ModeAuto), string(ModeNever), string(ModeClone)} }

// ParseMode validates a --git value.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeAuto, ModeNever, ModeClone:
		return Mode(s), nil
	}
	return "", fmt.Errorf("unknown --git mode %q; choose from %s", s, strings.Join(Modes(), ", "))
}

// Options configures Open.
type Options struct {
	// Mode says whether cloning is allowed. The zero value is ModeAuto.
	Mode Mode
	// LocalPath is a clone to use instead of looking for one. It is an error if
	// it is not a clone of the repository being scanned.
	LocalPath string
	// NoFetch skips the fetch that brings an existing clone up to date. Reading
	// a stale clone is fast but reports stale branches, so this is opt-in.
	NoFetch bool
	// Progress, when set, receives one-line status updates.
	Progress func(format string, args ...any)
}

// Repo is a local Git repository standing in for the GitHub API.
type Repo struct {
	// dir is the directory git commands run in: a work tree, or a bare
	// repository.
	dir string
	// refPrefix is where this repository keeps the remote's branches:
	// "refs/heads/" in a bare clone we made, "refs/remotes/NAME/" in somebody
	// else's working clone.
	refPrefix string
	// remote is the remote to fetch from.
	remote string

	// owned distinguishes a clone we made from somebody's working copy. Only
	// our own may be fetched with a partial-clone filter, since that setting
	// is written into the repository and is not ours to change.
	owned bool
	// cloned records that we created the clone in this run, so there is
	// nothing to fetch.
	cloned bool

	progress func(format string, args ...any)

	// tips caches branch name to tip commit ID, filled in by Branches.
	tips map[string]string
	// counts caches how many commits are reachable from a commit ID.
	counts map[string]int
	// hosts caches ssh alias resolutions.
	hosts map[string]string
	// hostResolver resolves an ssh host alias. Nil means ask ssh, which is
	// what everything but a test wants.
	hostResolver func(context.Context, string) string
}

// resolveHost turns an ssh host alias into the host it really names, answering
// "" when that cannot be worked out. Each answer is remembered, because a
// clone may have several remotes on one alias.
func (r *Repo) resolveHost(ctx context.Context, host string) string {
	if resolved, ok := r.hosts[host]; ok {
		return resolved
	}
	resolve := r.hostResolver
	if resolve == nil {
		resolve = r.sshHostname
	}
	resolved := resolve(ctx, host)
	if r.hosts == nil {
		r.hosts = map[string]string{}
	}
	r.hosts[host] = resolved
	return resolved
}

// Open finds a local Git repository for repo, following opts.
//
// It returns (nil, nil) when there is nothing usable and cloning was not asked
// for; callers fall back to the API. An unusable clone that the user named
// explicitly is an error, because silently ignoring --local-repo would hide a
// typo.
func Open(ctx context.Context, repo repository.Repository, opts Options) (*Repo, error) {
	if opts.Mode == ModeNever {
		return nil, nil
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(string, ...any) {}
	}

	if opts.LocalPath != "" {
		r, err := openExisting(ctx, opts.LocalPath, repo, progress)
		if err != nil {
			return nil, fmt.Errorf("--local-repo %s: %w", opts.LocalPath, err)
		}
		return r, r.freshen(ctx, opts.NoFetch)
	}

	// The common case: the scan is being run from inside a clone of the
	// repository it is scanning.
	if wd, err := os.Getwd(); err == nil {
		r, openErr := openExisting(ctx, wd, repo, progress)
		if openErr == nil {
			progress("using the Git repository in %s", r.dir)
			return r, r.freshen(ctx, opts.NoFetch)
		}
		progress("not using local Git: %v", openErr)
	}

	if opts.Mode != ModeClone {
		return nil, nil
	}
	r, err := openCache(ctx, repo, progress)
	if err != nil {
		return nil, err
	}
	return r, r.freshen(ctx, opts.NoFetch)
}

// openExisting adopts a clone that already exists on disk, provided it is a
// clone of the right repository and deep enough to answer ancestry questions.
func openExisting(ctx context.Context, path string, repo repository.Repository, progress func(string, ...any)) (*Repo, error) {
	r := &Repo{dir: path, progress: progress}
	if _, err := r.git(ctx, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("not a Git repository")
	}
	// A shallow clone has no history to compare, so every branch would look
	// unmerged. That is worse than not using Git at all.
	if out, err := r.git(ctx, "rev-parse", "--is-shallow-repository"); err == nil && strings.TrimSpace(string(out)) == "true" {
		return nil, errors.New("the clone is shallow, so it cannot say whether a branch is merged")
	}

	remote, prefix, err := r.remoteFor(ctx, repo)
	if err != nil {
		return nil, err
	}
	r.remote, r.refPrefix = remote, prefix
	return r, nil
}

// openCache creates or reuses a treeless bare clone under the cache directory.
// Blobs are the bulk of a repository and nothing here reads file contents, so
// they are never downloaded.
func openCache(ctx context.Context, repo repository.Repository, progress func(string, ...any)) (*Repo, error) {
	dir, err := cache.Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "clones", cache.Segment(repo.Host), cache.Segment(repo.Owner), cache.Segment(repo.Name)+".git")

	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		return &Repo{dir: path, refPrefix: "refs/heads/", remote: "origin", owned: true, progress: progress}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("could not create the clone directory: %w", err)
	}
	// A failed clone leaves a directory git will refuse to reuse.
	if err := os.RemoveAll(path); err != nil {
		return nil, fmt.Errorf("unable to remove target directory %s: %w", path, err)
	}

	progress("cloning %s/%s into %s (commits only, no file contents)", repo.Owner, repo.Name, path)
	r := &Repo{dir: filepath.Dir(path), refPrefix: "refs/heads/", remote: "origin", owned: true, progress: progress, cloned: true}
	if _, err := r.gitAuthed(ctx, "clone", "--bare", "--filter=blob:none", "--no-tags",
		remoteURL(repo), path); err != nil {
		var removeErr error
		if err := os.RemoveAll(path); err != nil {
			removeErr = fmt.Errorf("unable to remove temporary directory %s: %w", path, err)
		}
		cloneErr := fmt.Errorf("cloning %s/%s: %w", repo.Owner, repo.Name, err)
		return nil, errors.Join(cloneErr, removeErr)
	}
	r.dir = path
	return r, nil
}

// freshen brings the clone's view of the remote up to date. A clone that cannot
// be fetched is still worth reading, so this warns rather than failing: the
// worst case is a report about branches that have since moved.
func (r *Repo) freshen(ctx context.Context, noFetch bool) error {
	if noFetch || r.cloned {
		return nil
	}
	r.progress("fetching %s in %s", r.remote, r.dir)
	args := []string{"fetch", "--quiet", "--prune", "--no-tags"}
	if r.owned {
		// Fetching with a filter writes the partial-clone setting into the
		// repository, so it is only ever passed to a clone we made. The same
		// goes for the refspec: a bare clone of ours has none of its own, but
		// forcing refs/heads in somebody else's bare repository would
		// overwrite refs they may have put there themselves.
		args = append(args, "--filter=blob:none", r.remote, "+refs/heads/*:refs/heads/*")
	} else {
		args = append(args, r.remote)
	}
	if _, err := r.gitAuthed(ctx, args...); err != nil {
		// Retry without the filter, which a server or an older clone may not
		// support.
		if !r.owned {
			r.progress("could not fetch, reading the clone as it is: %v", err)
			return nil
		}
		plain := remove(append([]string{}, args...), "--filter=blob:none")
		if _, err2 := r.gitAuthed(ctx, plain...); err2 != nil {
			r.progress("could not fetch, reading the clone as it is: %v", err)
		}
	}
	return nil
}

// Describe names the repository being read, for a note in the report.
func (r *Repo) Describe() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// commitFields are the tip commit's details, in the order Branches reads them.
// Fields are separated by NUL because a commit subject may contain anything
// else, tabs included.
const commitFields = "%00%(objecttype)%00%(objectname)%00" +
	"%(committerdate:iso-strict)%00%(contents:subject)%00" +
	"%(authorname)%00%(authoremail:trim)%00%(committername)%00%(committeremail:trim)"

// nameField is the for-each-ref field that yields a branch name, with the
// "refs/heads" or "refs/remotes/NAME" this repository keeps them under
// stripped off.
func (r *Repo) nameField() string {
	depth := strings.Count(strings.TrimSuffix(r.refPrefix, "/"), "/") + 1
	return "%(refname:lstrip=" + strconv.Itoa(depth) + ")"
}

// Branches lists every branch with its tip commit, in one local command
// however many branches there are.
//
// The Actors it returns carry a name and an email address but no login, since
// Git has no idea which GitHub account an address belongs to. Resolving that is
// the API's job.
func (r *Repo) Branches(ctx context.Context) ([]gh.Branch, error) {
	out, err := r.git(ctx, "for-each-ref", "--format="+r.nameField()+commitFields, r.refPrefix)
	if err != nil {
		return nil, fmt.Errorf("listing branches from %s: %w", r.dir, err)
	}

	var branches []gh.Branch
	tips := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x00")
		if len(f) != 9 {
			continue
		}
		name := f[0]
		// Remote-tracking refs include a symbolic HEAD, which is not a branch.
		if name == "HEAD" || f[1] != "commit" {
			continue
		}
		b := gh.Branch{Name: name, Tip: gh.Commit{
			OID:             f[2],
			MessageHeadline: f[4],
			Author:          gh.Actor{Name: f[5], Email: f[6]},
			Committer:       gh.Actor{Name: f[7], Email: f[8]},
		}}
		if t, err := time.Parse(time.RFC3339, f[3]); err == nil {
			b.Tip.CommittedDate = t
		}
		tips[name] = b.Tip.OID
		branches = append(branches, b)
	}
	r.tips = tips
	r.progress("read %d branches from %s", len(branches), r.dir)
	return branches, nil
}

// ContainedIn reports which of heads are fully merged into base.
//
// One local command per base finds them all, so this costs the same whether it
// is asked about one branch or a thousand — which is what makes --all-bases
// affordable. Heads that are not contained are left out of the result, since
// nothing needs to know how far ahead they are.
func (r *Repo) ContainedIn(ctx context.Context, base string, heads []string) (map[string]gh.Comparison, error) {
	out := make(map[string]gh.Comparison, len(heads))
	if len(heads) == 0 {
		return out, nil
	}
	baseOID, err := r.resolve(ctx, base)
	if err != nil {
		return nil, err
	}

	merged, err := r.mergedInto(ctx, baseOID)
	if err != nil {
		return nil, err
	}
	baseCount, err := r.reachable(ctx, baseOID)
	if err != nil {
		return nil, err
	}
	for _, head := range heads {
		headOID, ok := merged[head]
		if !ok {
			continue
		}
		if headOID == baseOID {
			out[head] = gh.Comparison{Status: "IDENTICAL"}
			continue
		}
		// The head is an ancestor of the base, so everything the head can
		// reach the base can reach too, and the difference between the two
		// totals is how far behind the head is. Subtracting counts we already
		// have keeps --all-bases to one count per branch instead of one per
		// pair of branches.
		headCount, err := r.reachable(ctx, headOID)
		if err != nil {
			return nil, err
		}
		out[head] = gh.Comparison{Status: "BEHIND", BehindBy: baseCount - headCount}
	}
	r.progress("compared %d branches against %s locally", len(heads), base)
	return out, nil
}

// mergedInto returns the branches whose tip is an ancestor of oid, keyed by
// branch name.
func (r *Repo) mergedInto(ctx context.Context, oid string) (map[string]string, error) {
	format := r.nameField() + "%00%(objectname)"
	out, err := r.git(ctx, "for-each-ref", "--merged="+oid, "--format="+format, r.refPrefix)
	if err != nil {
		return nil, fmt.Errorf("finding branches merged into %s: %w", oid, err)
	}
	merged := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		name, tip, ok := strings.Cut(line, "\x00")
		if ok && name != "HEAD" {
			merged[name] = tip
		}
	}
	return merged, nil
}

// reachable counts the commits reachable from oid, remembering each answer:
// with --all-bases every branch is asked about many times over.
func (r *Repo) reachable(ctx context.Context, oid string) (int, error) {
	if n, ok := r.counts[oid]; ok {
		return n, nil
	}
	out, err := r.git(ctx, "rev-list", "--count", oid)
	if err != nil {
		return 0, fmt.Errorf("counting the commits reachable from %s: %w", oid, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("counting the commits reachable from %s: unexpected output %q", oid, out)
	}
	if r.counts == nil {
		r.counts = map[string]int{}
	}
	r.counts[oid] = n
	return n, nil
}

// resolve turns a branch name into a commit ID, using what Branches already
// read when it can.
func (r *Repo) resolve(ctx context.Context, branch string) (string, error) {
	if oid, ok := r.tips[branch]; ok {
		return oid, nil
	}
	out, err := r.git(ctx, "rev-parse", "--verify", r.refPrefix+branch)
	if err != nil {
		return "", fmt.Errorf("branch %q not found in %s", branch, r.dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// remoteFor finds the remote pointing at the repository being scanned, and
// where that remote's branches are kept locally.
//
// Only that one remote's refs are ever read. A clone commonly has the scanned
// repository as "upstream" and the user's own fork as "origin", and the fork's
// branches are not the ones being reported on — so the prefix comes from the
// matching remote's own fetch refspec rather than from a guess about where
// branches usually live.
func (r *Repo) remoteFor(ctx context.Context, repo repository.Repository) (remote, refPrefix string, err error) {
	out, err := r.git(ctx, "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		return "", "", fmt.Errorf("no remote points at %s/%s", repo.Owner, repo.Name)
	}
	// Aliased remotes are resolved on a second pass, so that a remote whose
	// host is written out in full always wins over one that needs ssh
	// consulted, however the config happens to be ordered.
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for _, viaSSHConfig := range []bool{false, true} {
		for _, line := range lines {
			key, url, ok := strings.Cut(line, " ")
			if !ok {
				continue
			}
			if viaSSHConfig {
				ok = r.matchesRepoViaSSHConfig(ctx, url, repo)
			} else {
				ok = matchesRepo(url, repo)
			}
			if !ok {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
			return name, r.refPrefixFor(ctx, name), nil
		}
	}
	return "", "", fmt.Errorf("no remote points at %s/%s", repo.Owner, repo.Name)
}

// refPrefixFor works out where a remote's branches land, from the refspecs it
// is fetched with.
//
// A remote with no refspec of its own is one of our own bare clones, whose
// branches are its own, or a bare repository set up by hand; refs/heads is the
// only sensible reading of either.
func (r *Repo) refPrefixFor(ctx context.Context, remote string) string {
	out, err := r.git(ctx, "config", "--get-all", "remote."+remote+".fetch")
	if err == nil {
		for _, spec := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if prefix, ok := branchDestination(spec); ok {
				return prefix
			}
		}
	}
	if bare, err := r.git(ctx, "rev-parse", "--is-bare-repository"); err == nil &&
		strings.TrimSpace(string(bare)) == "true" {
		return "refs/heads/"
	}
	return "refs/remotes/" + remote + "/"
}

// branchDestination reads the local side of a fetch refspec, so that
// "+refs/heads/*:refs/remotes/origin/*" gives "refs/remotes/origin/".
func branchDestination(spec string) (string, bool) {
	src, dst, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(spec), "+"), ":")
	if !ok || !strings.HasSuffix(dst, "*") {
		return "", false
	}
	switch src {
	case "refs/heads/*":
		return strings.TrimSuffix(dst, "*"), true
	case "refs/*":
		// A mirror, which keeps every kind of ref where it found it.
		if dst == "refs/*" {
			return "refs/heads/", true
		}
	}
	return "", false
}

// git runs a git command that needs no network access.
func (r *Repo) git(ctx context.Context, args ...string) ([]byte, error) {
	return r.run(ctx, false, args...)
}

// gitAuthed runs a git command that talks to the remote, borrowing gh's
// credentials. Handing the token to git through gh's credential helper keeps it
// out of the process arguments, where anything on the machine could read it.
func (r *Repo) gitAuthed(ctx context.Context, args ...string) ([]byte, error) {
	return r.run(ctx, true, args...)
}

func (r *Repo) run(ctx context.Context, authed bool, args ...string) ([]byte, error) {
	full := []string{"-C", r.dir}
	if authed {
		full = append(full, "-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential")
	}
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(firstLine(msg))
	}
	return stdout.Bytes(), nil
}

// remoteURL is the HTTPS clone URL for repo, which is the only scheme gh's
// credential helper can serve.
func remoteURL(repo repository.Repository) string {
	host := repo.Host
	if host == "" {
		host = "github.com"
	}
	return fmt.Sprintf("https://%s/%s/%s.git", host, repo.Owner, repo.Name)
}

// matchesRepo reports whether a remote URL points at repo, across the several
// shapes a Git remote can take: https://host/owner/name.git,
// git@host:owner/name, ssh://git@host/owner/name and so on.
func matchesRepo(url string, repo repository.Repository) bool {
	host, path, ok := splitRemote(url)
	if !ok {
		return false
	}
	if !hostEqual(host, repo.Host) {
		return false
	}
	return ownerNameEqual(path, repo)
}

// matchesRepoViaSSHConfig is matchesRepo for a remote whose host is an alias
// from the user's ssh config, as in "git@gh-work:owner/name" with a
// "Host gh-work / HostName github.com" stanza. Only the host needs resolving,
// so the owner and name are checked first and ssh is left alone unless they
// match.
func (r *Repo) matchesRepoViaSSHConfig(ctx context.Context, url string, repo repository.Repository) bool {
	if repo.Host == "" || !isSSHRemote(url) {
		return false
	}
	host, path, ok := splitRemote(url)
	if !ok || !ownerNameEqual(path, repo) || hostEqual(host, repo.Host) {
		return false
	}
	resolved := r.resolveHost(ctx, host)
	if resolved == "" || strings.EqualFold(resolved, host) || !hostEqual(resolved, repo.Host) {
		return false
	}
	r.progress("the remote host %q is an ssh alias for %q", host, resolved)
	return true
}

func hostEqual(got, want string) bool {
	// An empty target host means the caller does not care which host it is.
	return want == "" || strings.EqualFold(got, want) || strings.EqualFold(got, "ssh."+want)
}

func ownerNameEqual(path string, repo repository.Repository) bool {
	owner, name, ok := lastTwo(path)
	return ok && strings.EqualFold(owner, repo.Owner) && strings.EqualFold(name, repo.Name)
}

// isSSHRemote reports whether a remote URL is one ssh would be used for, and so
// one whose host may be an alias. Neither https:// nor git:// consults the ssh
// config.
func isSSHRemote(url string) bool {
	url = strings.TrimSpace(url)
	if i := strings.Index(url, "://"); i >= 0 {
		return strings.EqualFold(url[:i], "ssh")
	}
	// scp-like: [user@]host:path. A path with no colon is a local directory.
	_, rest, ok := strings.Cut(url, ":")
	return ok && !strings.HasPrefix(rest, `\`)
}

// resolveHost asks ssh what a host name really resolves to, which is the only
// way to know: "Host" stanzas can rewrite a name arbitrarily, and ~/.ssh/config
// is not a format worth reimplementing.
//
// The answer is remembered, since a clone can have several remotes on the same
// alias. Anything that goes wrong — no ssh, a slow config, a name ssh would
// refuse — reads as "no idea", and the scan falls back to the API.
func (r *Repo) sshHostname(ctx context.Context, host string) string {
	if !plausibleHostname(host) {
		return ""
	}
	// A "Match exec" stanza runs a command while ssh works out the answer, so
	// this is not guaranteed to be quick.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh", "-G", "--", host)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return parseSSHHostname(out)
}

// parseSSHHostname picks the effective host name out of "ssh -G" output, which
// is one lowercased "keyword value" per line.
func parseSSHHostname(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "hostname "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// plausibleHostname reports whether a string is safe to hand to ssh as a host
// name. A remote URL comes from the repository's config, so a name that could
// be read as an option — anything beginning with "-" — must never reach the
// command line.
func plausibleHostname(host string) bool {
	if host == "" || len(host) > 253 || strings.HasPrefix(host, "-") {
		return false
	}
	for _, c := range host {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func splitRemote(url string) (host, path string, ok bool) {
	url = strings.TrimSpace(url)
	if i := strings.Index(url, "://"); i >= 0 {
		rest := url[i+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			if slash := strings.Index(rest, "/"); slash < 0 || at < slash {
				rest = rest[at+1:]
			}
		}
		host, path, ok = strings.Cut(rest, "/")
		host, _, _ = strings.Cut(host, ":") // drop any port
		return host, path, ok
	}
	// scp-like: [user@]host:path
	rest := url
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	host, path, ok = strings.Cut(rest, ":")
	return host, path, ok
}

func lastTwo(path string) (owner, name string, ok bool) {
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[len(parts)-2], parts[len(parts)-1], true
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func remove(in []string, want string) []string {
	out := in[:0]
	for _, s := range in {
		if s != want {
			out = append(out, s)
		}
	}
	return out
}
