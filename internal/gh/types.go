package gh

import (
	"fmt"
	"strings"
	"time"
)

// Info describes the repository being scanned.
type Info struct {
	NameWithOwner    string
	URL              string
	IsPrivate        bool
	IsArchived       bool
	ViewerPermission string // READ, TRIAGE, WRITE, MAINTAIN, ADMIN
	DefaultBranch    string
}

// HostURL returns the web root of the GitHub instance, for building links to
// things outside this repository such as user profiles.
func (i Info) HostURL() string {
	suffix := "/" + i.NameWithOwner
	if i.NameWithOwner == "" || !strings.HasSuffix(i.URL, suffix) {
		return ""
	}
	return strings.TrimSuffix(i.URL, suffix)
}

// CanListCollaborators reports whether the current viewer's permission level is
// high enough for the collaborator list, which the GitHub API restricts to
// users with push access.
func (i Info) CanListCollaborators() bool {
	switch i.ViewerPermission {
	case "WRITE", "MAINTAIN", "ADMIN":
		return true
	}
	return false
}

// Actor is a GitHub account referenced by a branch, pull request or issue. A
// zero Login means the account was deleted or the commit's email address is not
// linked to any account.
type Actor struct {
	Login string
	Type  string // User, Bot, Organization, Mannequin, EnterpriseUserAccount
	Name  string // commit author name, when the login is unknown
	Email string // commit author email, when the login is unknown
}

// IsBot reports whether the actor is an app rather than a person.
func (a Actor) IsBot() bool {
	return a.Type == "Bot" || strings.HasSuffix(a.Login, "[bot]")
}

// Known reports whether the actor resolves to a live GitHub account.
func (a Actor) Known() bool {
	return a.Login != "" && a.Login != "ghost"
}

// Describe renders the actor for display, falling back to the raw commit
// identity when no account could be resolved.
func (a Actor) Describe() string {
	if a.Login != "" {
		return a.Login
	}
	switch {
	case a.Name != "" && a.Email != "":
		return fmt.Sprintf("%s <%s>", a.Name, a.Email)
	case a.Name != "":
		return a.Name
	case a.Email != "":
		return a.Email
	}
	return "(unknown)"
}

// Commit is the tip commit of a branch.
type Commit struct {
	OID             string
	CommittedDate   time.Time
	MessageHeadline string
	Author          Actor
	Committer       Actor
}

// CommitIdentity is the pairing of a commit's Git identities with the GitHub
// accounts they belong to. A login is empty when the address is not linked to
// any account, which is the same thing the API reports for such a commit.
type CommitIdentity struct {
	AuthorEmail    string
	AuthorLogin    string
	CommitterEmail string
	CommitterLogin string
}

// Branch is a single ref under refs/heads/.
type Branch struct {
	Name string
	Tip  Commit
	// Protected is true when a ref update rule forbids deleting the branch.
	Protected bool
}

// PullRequest is a pull request, either open or already resolved.
type PullRequest struct {
	Number            int
	State             string // OPEN, MERGED, CLOSED
	Title             string
	URL               string
	HeadRefName       string
	HeadOwner         string
	IsCrossRepository bool
	Author            Actor
	Assignees         []Actor
	MergedAt          *time.Time
	ClosedAt          *time.Time
	UpdatedAt         time.Time
}

// Ref renders the pull request as "#123".
func (p PullRequest) Ref() string { return fmt.Sprintf("#%d", p.Number) }

// ResolvedAt returns when the pull request was merged or closed, whichever
// applies, and the zero time for open pull requests.
func (p PullRequest) ResolvedAt() time.Time {
	if p.MergedAt != nil {
		return *p.MergedAt
	}
	if p.ClosedAt != nil {
		return *p.ClosedAt
	}
	return time.Time{}
}

// Issue is a repository issue.
type Issue struct {
	Number    int
	State     string // OPEN, CLOSED
	Title     string
	URL       string
	Author    Actor
	Assignees []Actor
	UpdatedAt time.Time
}

// Ref renders the issue as "#123".
func (i Issue) Ref() string { return fmt.Sprintf("#%d", i.Number) }

// Comparison is the relationship between a base branch and a head branch.
type Comparison struct {
	Status   string // AHEAD, BEHIND, DIVERGED, IDENTICAL
	AheadBy  int    // commits the head has that the base does not
	BehindBy int    // commits the base has that the head does not
}

// Contained reports whether every commit on the head branch is already
// reachable from the base branch, which makes the head branch redundant.
func (c Comparison) Contained() bool {
	return c.AheadBy == 0
}
