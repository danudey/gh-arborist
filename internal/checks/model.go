// Package checks holds the individual audits and the shared finding model
// they report into.
package checks

import (
	"sort"
	"strings"
	"time"
)

// Kind is the type of thing a finding is about.
type Kind string

const (
	KindBranch      Kind = "branch"
	KindPullRequest Kind = "pull-request"
	KindIssue       Kind = "issue"
)

// Action is the suggested response to a finding. This tool only ever suggests;
// it does not act.
type Action string

const (
	// ActionDelete means nothing would be lost by removing the branch.
	ActionDelete Action = "delete"
	// ActionReassign means the item is still live but has nobody to own it.
	ActionReassign Action = "reassign"
	// ActionReview means a human needs to judge whether the work still matters.
	ActionReview Action = "review"
)

// rank orders actions from most to least decisive, so that a branch which is
// both fully merged and owned by a departed user is reported as deletable
// rather than needing reassignment.
func (a Action) rank() int {
	switch a {
	case ActionDelete:
		return 3
	case ActionReassign:
		return 2
	default:
		return 1
	}
}

// Category is the type of indicator behind a reason. The report breaks each
// section into a sub-section per category, so a reader can look at one kind of
// problem at a time.
type Category string

const (
	CategoryMerged           Category = "merged-into-base"
	CategoryDuplicate        Category = "duplicate-branch"
	CategoryPRMerged         Category = "pull-request-merged"
	CategoryPRClosed         Category = "pull-request-closed-unmerged"
	CategoryStale            Category = "older-than-threshold"
	CategoryOwnerNoAccess    Category = "owner-without-access"
	CategoryOwnerDeleted     Category = "owner-account-deleted"
	CategoryAuthorNoAccess   Category = "author-without-access"
	CategoryAuthorDeleted    Category = "author-account-deleted"
	CategoryAssigneeNoAccess Category = "assignee-without-access"
	CategoryAssigneeDeleted  Category = "assignee-account-deleted"
	// CategoryOther catches a reason that names no category, so that nothing
	// can fall out of the report by being uncategorised.
	CategoryOther Category = "other"
)

// categoryOrder is the order sub-sections appear in, most decisive first.
var categoryOrder = []Category{
	CategoryMerged,
	CategoryDuplicate,
	CategoryPRMerged,
	CategoryPRClosed,
	CategoryStale,
	CategoryOwnerNoAccess,
	CategoryOwnerDeleted,
	CategoryAuthorNoAccess,
	CategoryAuthorDeleted,
	CategoryAssigneeNoAccess,
	CategoryAssigneeDeleted,
	CategoryOther,
}

var categoryTitles = map[Category]string{
	CategoryMerged:           "Fully merged into another branch",
	CategoryDuplicate:        "Duplicate of another branch",
	CategoryPRMerged:         "Pull request merged",
	CategoryPRClosed:         "Pull request closed without merging",
	CategoryStale:            "Older than the age threshold",
	CategoryOwnerNoAccess:    "Owner no longer has access",
	CategoryOwnerDeleted:     "Owner's account was deleted",
	CategoryAuthorNoAccess:   "Author no longer has access",
	CategoryAuthorDeleted:    "Author's account was deleted",
	CategoryAssigneeNoAccess: "Assignee no longer has access",
	CategoryAssigneeDeleted:  "Assignee's account was deleted",
	CategoryOther:            "Other",
}

// Title is the sub-section heading for a category.
func (c Category) Title() string {
	if title, ok := categoryTitles[c]; ok {
		return title
	}
	return string(c)
}

// Reason records why one check flagged an item.
type Reason struct {
	Check string `json:"check"`
	// Category is the type of indicator, and so which sub-section of the
	// report this reason files the item under.
	Category Category `json:"category"`
	// Summary is the full explanation, used everywhere except the terminal
	// table, where it would be too wide to fit.
	Summary string `json:"summary"`
	// Short is the same reason compressed to a few words for the table.
	Short  string `json:"short,omitempty"`
	Action Action `json:"suggestedAction"`
}

// category returns the reason's category, falling back to CategoryOther.
func (r Reason) category() Category {
	if r.Category == "" {
		return CategoryOther
	}
	return r.Category
}

// short returns the compact form, falling back to the full summary.
func (r Reason) short() string {
	if r.Short != "" {
		return r.Short
	}
	return r.Summary
}

// Detail is one extra fact about a finding. The terminal report leaves these
// out to stay scannable; the HTML and Markdown reports show them under an
// expandable heading.
type Detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// URL, when set, makes the value a link.
	URL string `json:"url,omitempty"`
	// Code marks a value that should be rendered in a monospaced font, such as
	// a commit ID or a branch name.
	Code bool `json:"code,omitempty"`
}

// Finding is one flagged branch, pull request or issue, with every reason it
// was flagged.
type Finding struct {
	Kind Kind   `json:"kind"`
	Name string `json:"name"`
	// Title is the pull request or issue title, or the branch's tip commit
	// subject.
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
	// Owner is whoever the item currently belongs to: the pull request or issue
	// author, or the branch's last committer.
	Owner string `json:"owner,omitempty"`
	// OwnerURL is the owner's GitHub profile, empty when the commit identity
	// could not be resolved to an account.
	OwnerURL     string    `json:"ownerUrl,omitempty"`
	LastActivity time.Time `json:"lastActivity,omitempty"`
	Reasons      []Reason  `json:"reasons"`
	// Details are the expandable facts about the item.
	Details []Detail `json:"details,omitempty"`
}

// Action returns the most decisive action any reason suggests.
func (f Finding) Action() Action {
	best := ActionReview
	for _, r := range f.Reasons {
		if r.Action.rank() > best.rank() {
			best = r.Action
		}
	}
	return best
}

// Why joins every reason into one human-readable phrase.
func (f Finding) Why() string {
	parts := make([]string, 0, len(f.Reasons))
	for _, r := range f.Reasons {
		parts = append(parts, r.Summary)
	}
	return strings.Join(parts, "; ")
}

// WhyShort joins the compact form of every reason, for the terminal table.
func (f Finding) WhyShort() string {
	parts := make([]string, 0, len(f.Reasons))
	for _, r := range f.Reasons {
		parts = append(parts, r.short())
	}
	return strings.Join(parts, "; ")
}

// Categories lists the indicator types the finding matched, in report order.
// A finding that matched several is listed under each of them.
func (f Finding) Categories() []Category {
	seen := map[Category]bool{}
	for _, r := range f.Reasons {
		seen[r.category()] = true
	}
	out := make([]Category, 0, len(seen))
	for _, c := range categoryOrder {
		if seen[c] {
			out = append(out, c)
		}
	}
	return out
}

// Checks lists the checks that contributed to the finding.
func (f Finding) Checks() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range f.Reasons {
		if !seen[r.Check] {
			seen[r.Check] = true
			out = append(out, r.Check)
		}
	}
	return out
}

// Result collects the findings from one or more checks.
type Result struct {
	Repository string `json:"repository"`
	// RepositoryURL is the repository's web URL, for linking the report
	// heading.
	RepositoryURL string         `json:"repositoryUrl,omitempty"`
	ScannedAt     time.Time      `json:"scannedAt"`
	ChecksRun     []string       `json:"checksRun"`
	Scanned       map[string]int `json:"scanned"`
	Findings      []Finding      `json:"findings"`
	// Notes are caveats about the scan itself, such as checks that had to be
	// skipped or results that need extra interpretation.
	Notes []string `json:"notes,omitempty"`

	index map[string]int
}

// NewResult builds an empty result for a repository.
func NewResult(repo string, now time.Time) *Result {
	return &Result{
		Repository: repo,
		ScannedAt:  now,
		Scanned:    map[string]int{},
		// Never nil, so that JSON consumers always get an array.
		Findings: []Finding{},
		index:    map[string]int{},
	}
}

// Add records a finding, merging its reasons into any existing finding for the
// same item so that each branch, pull request or issue is reported once.
func (r *Result) Add(f Finding) {
	key := string(f.Kind) + "\x00" + f.Name
	if i, ok := r.index[key]; ok {
		existing := &r.Findings[i]
		existing.Reasons = append(existing.Reasons, f.Reasons...)
		if existing.Title == "" {
			existing.Title = f.Title
		}
		if existing.URL == "" {
			existing.URL = f.URL
		}
		if existing.Owner == "" {
			existing.Owner = f.Owner
			existing.OwnerURL = f.OwnerURL
		}
		if existing.LastActivity.IsZero() {
			existing.LastActivity = f.LastActivity
		}
		// Details describe the item, not the reason, so whichever check got
		// there first has already filled them in.
		if len(existing.Details) == 0 {
			existing.Details = f.Details
		}
		return
	}
	r.index[key] = len(r.Findings)
	r.Findings = append(r.Findings, f)
}

// Note records a caveat about the scan, ignoring duplicates.
func (r *Result) Note(note string) {
	for _, existing := range r.Notes {
		if existing == note {
			return
		}
	}
	r.Notes = append(r.Notes, note)
}

// Sort orders findings by kind, then oldest activity first, which puts the
// least contentious deletions at the top of the report.
func (r *Result) Sort() {
	kindRank := map[Kind]int{KindBranch: 0, KindPullRequest: 1, KindIssue: 2}
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if ka, kb := kindRank[a.Kind], kindRank[b.Kind]; ka != kb {
			return ka < kb
		}
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.Before(b.LastActivity)
		}
		return a.Name < b.Name
	})
	r.index = map[string]int{}
	for i, f := range r.Findings {
		r.index[string(f.Kind)+"\x00"+f.Name] = i
	}
}

// OfKind returns the findings for one kind of item.
func (r *Result) OfKind(k Kind) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Kind == k {
			out = append(out, f)
		}
	}
	return out
}

// Group is the findings of one kind that share an indicator.
type Group struct {
	Category Category `json:"category"`
	Title    string   `json:"title"`
	Findings []Finding
}

// GroupsOfKind splits the findings of one kind into a group per indicator, in
// report order, leaving out indicators nothing matched. A finding that matched
// several indicators appears in each of their groups.
func (r *Result) GroupsOfKind(k Kind) []Group {
	byCategory := map[Category][]Finding{}
	for _, f := range r.OfKind(k) {
		for _, c := range f.Categories() {
			byCategory[c] = append(byCategory[c], f)
		}
	}
	var out []Group
	for _, c := range categoryOrder {
		if findings := byCategory[c]; len(findings) > 0 {
			out = append(out, Group{Category: c, Title: c.Title(), Findings: findings})
		}
	}
	return out
}
