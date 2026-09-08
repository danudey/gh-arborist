// Package report renders check results as a terminal table, a Markdown
// document, an HTML page or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/text"
	"github.com/danudey/gh-arborist/internal/checks"
)

// result is checks.Result, aliased for brevity inside the renderers.
type result = checks.Result

// Format is an output shape.
type Format string

const (
	// FormatTable is the scannable terminal report, or tab-separated values
	// when the output is not a terminal.
	FormatTable Format = "table"
	// FormatJSON is the full result, for other programs to read.
	FormatJSON Format = "json"
	// FormatMarkdown is a document with expandable per-item detail.
	FormatMarkdown Format = "markdown"
	// FormatHTML is a self-contained page with expandable per-item detail.
	FormatHTML Format = "html"
)

// Formats lists the accepted format names, for help text and errors.
func Formats() []string {
	return []string{string(FormatTable), string(FormatMarkdown), string(FormatHTML), string(FormatJSON)}
}

// ParseFormat resolves a format name, accepting the usual abbreviations.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "table", "text", "terminal", "tty":
		return FormatTable, nil
	case "json":
		return FormatJSON, nil
	case "markdown", "md":
		return FormatMarkdown, nil
	case "html", "htm":
		return FormatHTML, nil
	}
	return "", fmt.Errorf("unknown format %q; choose from %s", s, strings.Join(Formats(), ", "))
}

// Options controls how a result is rendered.
type Options struct {
	// Out receives the report itself.
	Out io.Writer
	// ErrOut receives notes and warnings for the terminal report. The Markdown
	// and HTML reports carry their notes inside the document instead.
	ErrOut io.Writer
	// Format selects the output shape.
	Format Format
	// IsTTY switches the table report between the human layout and
	// tab-separated output.
	IsTTY bool
	// Color enables ANSI colour in the table report.
	Color bool
	// Width is the terminal width to fit the table report to.
	Width int
	// Hyperlinks controls OSC 8 terminal hyperlinks in the table report.
	Hyperlinks Hyperlinks
	// Env reads environment variables when detecting terminal support. Nil
	// means the real environment.
	Env func(string) string
}

// Render writes the result in the requested format.
func Render(res *checks.Result, opts Options) error {
	switch opts.Format {
	case FormatJSON:
		enc := json.NewEncoder(opts.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	case FormatMarkdown:
		return renderMarkdown(res, opts)
	case FormatHTML:
		return renderHTML(res, opts)
	case FormatTable, "":
		return renderTable(res, opts)
	}
	return fmt.Errorf("unknown format %q", opts.Format)
}

// actionCount is how many findings suggest one action.
type actionCount struct {
	Action checks.Action
	Count  int
}

// actionCounts tallies the findings by suggested action, most decisive first,
// leaving out actions that nothing suggests.
func actionCounts(res *checks.Result) []actionCount {
	counts := map[checks.Action]int{}
	for _, f := range res.Findings {
		counts[f.Action()]++
	}
	var out []actionCount
	for _, a := range []checks.Action{checks.ActionDelete, checks.ActionReassign, checks.ActionReview} {
		if counts[a] > 0 {
			out = append(out, actionCount{Action: a, Count: counts[a]})
		}
	}
	return out
}

// sections is the report order, with the labels each kind needs.
var sections = []struct {
	kind     checks.Kind
	header   string
	singular string
	plural   string
}{
	{checks.KindBranch, "BRANCH", "branch", "branches"},
	{checks.KindPullRequest, "PULL REQUEST", "pull request", "pull requests"},
	{checks.KindIssue, "ISSUE", "issue", "issues"},
}

// disclaimer is repeated in every format, because a report that looks like a
// list of completed actions would be dangerous.
const disclaimer = "This report never changes anything. Review each item before acting on it."

func lastActivity(t, now time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return text.RelativeTimeAgo(now, t)
}

// shortAge renders an age in the few characters a table column can spare.
func shortAge(now, t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	days := int(now.Sub(t).Hours() / 24)
	switch {
	case days < 1:
		return "today"
	case days < 30:
		return fmt.Sprintf("%dd", days)
	case days < 365:
		return fmt.Sprintf("%dmo", days/30)
	default:
		return fmt.Sprintf("%dy", days/365)
	}
}

func describeScanned(scanned map[string]int) string {
	order := []struct{ key, singular, plural string }{
		{"branches", "branch", "branches"},
		{"pullRequests", "pull request", "pull requests"},
		{"issues", "issue", "issues"},
		{"collaborators", "collaborator", "collaborators"},
	}
	var parts []string
	for _, o := range order {
		if n, ok := scanned[o.key]; ok {
			parts = append(parts, plural(n, o.singular, o.plural))
		}
	}
	if len(parts) == 0 {
		return "nothing scanned"
	}
	return strings.Join(parts, ", ") + " scanned"
}

// ownerLabel drops the email address from an unlinked commit identity, which
// the owner column has no room for.
func ownerLabel(s string) string {
	if i := strings.Index(s, " <"); i > 0 {
		return s[:i]
	}
	return s
}

// plural picks the right noun form, which text.Pluralize cannot do for words
// like "branch".
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, pluralForm)
}
