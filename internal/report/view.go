package report

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/danudey/gh-arborist/internal/checks"
)

// document is the shape the Markdown and HTML reports both render from. Unlike
// the terminal report these have room for the full reasons and the per-item
// detail, so nothing is abbreviated away.
type document struct {
	Repository    string
	RepositoryURL string
	ScannedAt     string
	Checks        []string
	Scanned       string
	Total         int
	Counts        []actionCount
	Notes         []string
	Sections      []docSection
	Disclaimer    string
}

type docSection struct {
	// Title names the kind, as in "Branches".
	Title string
	// NameHeader labels the first column, as in "Branch".
	NameHeader string
	// Count is how many distinct items the section holds. A item listed under
	// several indicators still counts once.
	Count int
	// Groups splits the section into one sub-section per indicator.
	Groups []docGroup
	// Rows is one row per item, in report order, for the per-item detail that
	// follows the sub-sections.
	Rows []docRow
}

// docGroup is one sub-section: the items that matched a single indicator.
type docGroup struct {
	// Title names the indicator, as in "Older than the age threshold".
	Title string
	// ID is a stable anchor for the sub-section.
	ID    string
	Count int
	Rows  []docRow
}

type docRow struct {
	// ID is a stable anchor for linking to this row.
	ID string
	// Key identifies the item itself, so that the same item appearing under
	// several indicators can be counted once.
	Key      string
	Name     string
	URL      string
	Title    string
	Age      string
	AgeExact string
	Owner    string
	OwnerURL string
	Action   checks.Action
	Reasons  []checks.Reason
	Details  []checks.Detail
	// Search is the lowercased haystack the HTML filter box matches against.
	Search string
	// Monospace marks names that are code, such as branch names.
	Monospace bool
}

func buildDocument(res *result) *document {
	doc := &document{
		Repository:    res.Repository,
		RepositoryURL: res.RepositoryURL,
		ScannedAt:     res.ScannedAt.UTC().Format("2006-01-02 15:04 MST"),
		Checks:        res.ChecksRun,
		Scanned:       describeScanned(res.Scanned),
		Total:         len(res.Findings),
		Counts:        actionCounts(res),
		Notes:         res.Notes,
		Disclaimer:    disclaimer,
	}

	used := map[string]bool{}
	for _, section := range sections {
		findings := res.OfKind(section.kind)
		if len(findings) == 0 {
			continue
		}
		out := docSection{
			Title:      strings.ToUpper(section.plural[:1]) + section.plural[1:],
			NameHeader: strings.ToUpper(section.singular[:1]) + section.singular[1:],
			Count:      len(findings),
		}

		// One canonical row per item, keyed so the sub-sections can share it.
		byKey := map[string]docRow{}
		for _, f := range findings {
			row := buildRow(res, f, uniqueSlug(string(f.Kind)+"-"+f.Name, used))
			byKey[row.Key] = row
			out.Rows = append(out.Rows, row)
		}

		// An item matching several indicators is listed under each of them.
		// The first listing keeps the item's own anchor; the repeats get one
		// qualified by the indicator, so every anchor stays unique and
		// "#branch-feature-x" still lands on the item.
		placed := map[string]bool{}
		for _, group := range res.GroupsOfKind(section.kind) {
			g := docGroup{
				Title: group.Title,
				ID:    uniqueSlug(string(section.kind)+"-"+string(group.Category), used),
				Count: len(group.Findings),
			}
			for _, f := range group.Findings {
				row := byKey[findingKey(f)]
				if placed[row.Key] {
					row.ID = uniqueSlug(row.ID+"-"+string(group.Category), used)
				}
				placed[row.Key] = true
				g.Rows = append(g.Rows, row)
			}
			out.Groups = append(out.Groups, g)
		}
		doc.Sections = append(doc.Sections, out)
	}
	return doc
}

func buildRow(res *result, f checks.Finding, id string) docRow {
	return docRow{
		ID:        id,
		Key:       findingKey(f),
		Name:      f.Name,
		URL:       f.URL,
		Title:     f.Title,
		Age:       shortAge(res.ScannedAt, f.LastActivity),
		AgeExact:  exactDate(f),
		Owner:     ownerLabel(f.Owner),
		OwnerURL:  f.OwnerURL,
		Action:    f.Action(),
		Reasons:   f.Reasons,
		Details:   f.Details,
		Search:    searchText(f),
		Monospace: f.Kind == checks.KindBranch,
	}
}

// findingKey identifies one item, so the same item listed under several
// indicators is recognised as one thing.
func findingKey(f checks.Finding) string {
	return string(f.Kind) + ":" + f.Name
}

// Why joins the full summary of every reason.
func (r docRow) Why() string {
	parts := make([]string, 0, len(r.Reasons))
	for _, reason := range r.Reasons {
		parts = append(parts, reason.Summary)
	}
	return strings.Join(parts, "; ")
}

func exactDate(f checks.Finding) string {
	if f.LastActivity.IsZero() {
		return "unknown"
	}
	return f.LastActivity.UTC().Format("2006-01-02")
}

// searchText gathers everything about a finding worth matching a filter
// against, so the HTML report's search box finds items by owner, reason or
// check as well as by name.
func searchText(f checks.Finding) string {
	parts := []string{f.Name, f.Title, f.Owner, string(f.Action())}
	parts = append(parts, f.Checks()...)
	for _, r := range f.Reasons {
		parts = append(parts, r.Summary)
	}
	for _, d := range f.Details {
		parts = append(parts, d.Label, d.Value)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// uniqueSlug turns a name into an anchor, adding a counter when two names
// collapse to the same slug.
func uniqueSlug(s string, used map[string]bool) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if slug == "" {
		slug = "item"
	}
	candidate := slug
	for i := 2; used[candidate]; i++ {
		candidate = fmt.Sprintf("%s-%d", slug, i)
	}
	used[candidate] = true
	return candidate
}
