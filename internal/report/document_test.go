package report

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/danudey/gh-arborist/internal/checks"
)

// detailedResult has one finding of every kind, each with links and details,
// so the Markdown and HTML renderers can be checked end to end.
func detailedResult() *checks.Result {
	res := checks.NewResult("acme/widgets", now)
	res.RepositoryURL = "https://github.com/acme/widgets"
	res.ChecksRun = []string{"closed-prs", "merged", "stale", "orphans"}
	res.Scanned["branches"] = 12

	res.Add(checks.Finding{
		Kind:         checks.KindBranch,
		Name:         "feature/landed",
		Title:        "Add the thing",
		URL:          "https://github.com/acme/widgets/tree/feature/landed",
		Owner:        "alice",
		OwnerURL:     "https://github.com/alice",
		LastActivity: now.AddDate(0, 0, -400),
		Reasons: []checks.Reason{
			{Check: "merged", Category: checks.CategoryMerged, Summary: "fully merged into main (7 commits behind, nothing unique left)", Short: "merged into main", Action: checks.ActionDelete},
			{Check: "stale", Category: checks.CategoryStale, Summary: "no commits for about 1 year (last was 2025-04-27), which is over the 1y threshold", Short: "idle over 1y", Action: checks.ActionReview},
		},
		Details: []checks.Detail{
			{Label: "Last commit", Value: "a1b2c3d", URL: "https://github.com/acme/widgets/commit/a1b2c3d", Code: true},
			{Label: "Commit subject", Value: "Add the thing"},
			{Label: "Commit author", Value: "alice", URL: "https://github.com/alice"},
			{Label: "Pull request", Value: "#12 (merged) Add the thing — by alice", URL: "https://github.com/acme/widgets/pull/12"},
		},
	})
	res.Add(checks.Finding{
		Kind:         checks.KindPullRequest,
		Name:         "#20",
		Title:        "Half-finished migration",
		URL:          "https://github.com/acme/widgets/pull/20",
		Owner:        "bob",
		OwnerURL:     "https://github.com/bob",
		LastActivity: now.AddDate(0, 0, -30),
		Reasons: []checks.Reason{
			{Check: "orphans", Category: checks.CategoryAuthorNoAccess, Summary: "opened by bob, and bob no longer has access to this repository", Short: "author bob has no access", Action: checks.ActionReassign},
		},
		Details: []checks.Detail{
			{Label: "State", Value: "open"},
			{Label: "Author", Value: "bob", URL: "https://github.com/bob"},
			{Label: "Head branch", Value: "feature/migrate", URL: "https://github.com/acme/widgets/tree/feature/migrate", Code: true},
		},
	})
	res.Add(checks.Finding{
		Kind:         checks.KindIssue,
		Name:         "#42",
		Title:        "Login broken",
		URL:          "https://github.com/acme/widgets/issues/42",
		Owner:        "carol",
		LastActivity: now.AddDate(0, 0, -10),
		Reasons: []checks.Reason{
			{Check: "orphans", Category: checks.CategoryAssigneeNoAccess, Summary: "assigned to bob, and bob no longer has access to this repository", Short: "assignee bob has no access", Action: checks.ActionReassign},
		},
		Details: []checks.Detail{{Label: "Assignee", Value: "bob", URL: "https://github.com/bob"}},
	})
	res.Note("a caveat about the scan")
	res.Sort()
	return res
}

func renderTo(t *testing.T, res *checks.Result, format Format) string {
	t.Helper()
	var out bytes.Buffer
	if err := Render(res, Options{Out: &out, Format: format}); err != nil {
		t.Fatalf("Render(%s): %v", format, err)
	}
	return out.String()
}

func TestMarkdownReport(t *testing.T) {
	got := renderTo(t, detailedResult(), FormatMarkdown)

	for _, want := range []string{
		"# Pruning report: [`acme/widgets`](https://github.com/acme/widgets)",
		"**3 items flagged** — 1 to delete, 2 to reassign",
		"> " + disclaimer,
		"## Notes",
		"a caveat about the scan",
		"## Branches (1)",
		"## Pull requests (1)",
		"## Issues (1)",
		// A sub-section per indicator, with the branch listed under both of
		// the ones it matched.
		"### Fully merged into another branch (1)",
		"### Older than the age threshold (1)",
		"### Author no longer has access (1)",
		"### Assignee no longer has access (1)",
		"### Branches in detail",
		// The table row links the name and the owner.
		"[`feature/landed`](https://github.com/acme/widgets/tree/feature/landed)",
		"[alice](https://github.com/alice)",
		// Expandable detail, with the full reasons rather than the short ones.
		`<details id="branch-feature-landed">`,
		"<summary><code>feature/landed</code> — delete · 1y · alice</summary>",
		"fully merged into main (7 commits behind, nothing unique left)",
		"- **Last commit**: [`a1b2c3d`](https://github.com/acme/widgets/commit/a1b2c3d)",
		"- **Pull request**: [#12 (merged) Add the thing — by alice](https://github.com/acme/widgets/pull/12)",
		"</details>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the Markdown report is missing %q", want)
		}
	}

	// The terminal's compact wording belongs only in the terminal.
	if strings.Contains(got, "idle over 1y") {
		t.Error("the Markdown report should use the full reasons, not the table's short forms")
	}
}

func TestMarkdownEscapesTableBreakingText(t *testing.T) {
	res := checks.NewResult("acme/widgets", now)
	res.Add(checks.Finding{
		Kind: checks.KindBranch,
		Name: "feature/pipe|name",
		Reasons: []checks.Reason{
			{Check: "stale", Summary: "subject was *starred* and had a | pipe", Action: checks.ActionReview},
		},
	})
	res.Sort()

	got := renderTo(t, res, FormatMarkdown)
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| --- ") {
			continue
		}
		// Five columns means six pipes; an unescaped pipe in the data would
		// add more and shift every following column.
		if n := strings.Count(line, "|") - strings.Count(line, `\|`); n != 6 {
			t.Errorf("row has %d structural pipes, want 6: %q", n, line)
		}
	}
	if !strings.Contains(got, `\*starred\*`) {
		t.Errorf("emphasis characters should be escaped:\n%s", got)
	}
}

func TestMdCode(t *testing.T) {
	// A pipe splits a table cell even inside a code span.
	if got, want := mdCode("a|b"), "`a\\|b`"; got != want {
		t.Errorf("mdCode(%q) = %q, want %q", "a|b", got, want)
	}
	// A backtick cannot appear inside a span, so it falls back to prose.
	if got, want := mdCode("has`tick"), "has\\`tick"; got != want {
		t.Errorf("mdCode(%q) = %q, want %q", "has`tick", got, want)
	}
	if got, want := mdCode("plain"), "`plain`"; got != want {
		t.Errorf("mdCode(%q) = %q, want %q", "plain", got, want)
	}
}

func TestMarkdownEmptyResult(t *testing.T) {
	res := checks.NewResult("acme/widgets", now)
	res.ChecksRun = []string{"stale"}
	got := renderTo(t, res, FormatMarkdown)

	if !strings.Contains(got, "**Nothing to prune.**") {
		t.Errorf("an empty result should say so:\n%s", got)
	}
	if strings.Contains(got, "| --- |") {
		t.Error("an empty result should not print a table")
	}
}

func TestHTMLReport(t *testing.T) {
	got := renderTo(t, detailedResult(), FormatHTML)

	for _, want := range []string{
		"<!doctype html>",
		"<title>Pruning report: acme/widgets</title>",
		`<a href="https://github.com/acme/widgets">acme/widgets</a>`,
		`<details class="row" id="branch-feature-landed" data-key="branch:feature/landed" data-action="delete"`,
		`<span class="name mono">feature/landed</span>`,
		`<span class="badge delete">delete</span>`,
		"fully merged into main (7 commits behind, nothing unique left)",
		`<a href="https://github.com/acme/widgets/commit/a1b2c3d" target="_blank" rel="noreferrer noopener"><code>a1b2c3d</code></a>`,
		"a caveat about the scan",
		disclaimer,
		// The filter controls the page needs to be useful at this size.
		`<input type="search" id="filter"`,
		`data-action="reassign"`,
		"</html>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the HTML report is missing %q", want)
		}
	}

	// The branch matched two indicators, so it is listed under both: four rows
	// for three findings. The page must also be self-contained.
	if n := strings.Count(got, `<details class="row"`); n != 4 {
		t.Errorf("got %d rows, want 4", n)
	}
	// The repeat listing must not reuse the first one's anchor.
	if n := strings.Count(got, `id="branch-feature-landed"`); n != 1 {
		t.Errorf("the item anchor appears %d times, want once", n)
	}
	if !strings.Contains(got, `id="branch-feature-landed-older-than-threshold"`) {
		t.Error("the repeat listing should get an anchor qualified by its indicator")
	}
	if m := regexp.MustCompile(`(?i)<(script|link)[^>]+(src|href)="https?://`).FindString(got); m != "" {
		t.Errorf("the page must not load anything remote, found %q", m)
	}
}

// A branch name is arbitrary text and must not be able to inject markup.
func TestHTMLEscapesFindingText(t *testing.T) {
	res := checks.NewResult("acme/widgets", now)
	res.Add(checks.Finding{
		Kind:  checks.KindBranch,
		Name:  `evil"><script>alert(1)</script>`,
		Owner: "<b>bob</b>",
		Reasons: []checks.Reason{
			{Check: "stale", Summary: `<img src=x onerror="alert(1)">`, Action: checks.ActionReview},
		},
		Details: []checks.Detail{{Label: "Note", Value: "</dd><script>x</script>"}},
	})
	res.Sort()

	got := renderTo(t, res, FormatHTML)
	for _, bad := range []string{"<script>alert(1)</script>", `<img src=x onerror=`, "<b>bob</b>"} {
		if strings.Contains(got, bad) {
			t.Errorf("unescaped %q reached the page", bad)
		}
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("the markup should appear escaped:\n%s", got)
	}
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{
		"": FormatTable, "table": FormatTable, "text": FormatTable,
		"json": FormatJSON, "JSON": FormatJSON,
		"markdown": FormatMarkdown, "md": FormatMarkdown,
		"html": FormatHTML, "htm": FormatHTML, " HTML ": FormatHTML,
	} {
		got, err := ParseFormat(in)
		if err != nil {
			t.Errorf("ParseFormat(%q): %v", in, err)
		} else if got != want {
			t.Errorf("ParseFormat(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ParseFormat("pdf"); err == nil {
		t.Error("an unknown format should fail")
	} else if !strings.Contains(err.Error(), "markdown") {
		t.Errorf("the error should list the valid formats, got %q", err)
	}
}

func TestUniqueSlug(t *testing.T) {
	used := map[string]bool{}
	tests := []struct{ in, want string }{
		{"branch-feature/one", "branch-feature-one"},
		{"branch-feature_one", "branch-feature-one-2"}, // collides once flattened
		{"branch-feature.one", "branch-feature-one-3"},
		{"pull-request-#20", "pull-request-20"},
		{"...", "item"},
	}
	for _, tt := range tests {
		if got := uniqueSlug(tt.in, used); got != tt.want {
			t.Errorf("uniqueSlug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDocumentCarriesFullDetail(t *testing.T) {
	doc := buildDocument(detailedResult())

	if len(doc.Sections) != 3 {
		t.Fatalf("got %d sections, want one per kind", len(doc.Sections))
	}
	if doc.Sections[0].Title != "Branches" || doc.Sections[0].NameHeader != "Branch" {
		t.Errorf("unexpected section labels: %+v", doc.Sections[0])
	}

	row := doc.Sections[0].Rows[0]
	if len(row.Reasons) != 2 || len(row.Details) != 4 {
		t.Errorf("row lost detail: %d reasons, %d details", len(row.Reasons), len(row.Details))
	}
	if row.AgeExact != now.AddDate(0, 0, -400).UTC().Format("2006-01-02") {
		t.Errorf("AgeExact = %q", row.AgeExact)
	}
	// The search haystack has to cover more than the name for the filter box
	// to be worth having.
	for _, want := range []string{"feature/landed", "alice", "merged", "stale", "commit subject"} {
		if !strings.Contains(row.Search, want) {
			t.Errorf("search text is missing %q: %q", want, row.Search)
		}
	}
	if row.Search != strings.ToLower(row.Search) {
		t.Error("search text should be lowercased, since the filter lowercases the term")
	}
}

func TestDocumentEmptySections(t *testing.T) {
	res := checks.NewResult("acme/widgets", now)
	res.Add(checks.Finding{
		Kind:    checks.KindIssue,
		Name:    "#1",
		Reasons: []checks.Reason{{Check: "orphans", Summary: "x", Action: checks.ActionReassign}},
	})
	res.Sort()

	doc := buildDocument(res)
	if len(doc.Sections) != 1 || doc.Sections[0].Title != "Issues" {
		t.Errorf("kinds with no findings should be left out, got %+v", doc.Sections)
	}
}

func TestExactDateUnknown(t *testing.T) {
	if got := exactDate(checks.Finding{LastActivity: time.Time{}}); got != "unknown" {
		t.Errorf("exactDate of a zero time = %q, want unknown", got)
	}
}
