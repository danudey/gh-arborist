package report

import (
	"fmt"
	"html"
	"io"
	"strings"
)

// renderMarkdown writes a Markdown report: a scannable table per kind, then a
// collapsible block per item holding the full reasons and the item's details.
//
// The collapsible blocks use <details>, which GitHub, VS Code and most other
// Markdown renderers support, so the document stays readable when pasted into
// an issue.
func renderMarkdown(res *result, opts Options) error {
	doc := buildDocument(res)
	var b strings.Builder

	fmt.Fprintf(&b, "# Pruning report: %s\n\n", mdLink(doc.Repository, doc.RepositoryURL, true))
	fmt.Fprintf(&b, "Scanned %s · %s\n\n", doc.ScannedAt, doc.Scanned)
	if len(doc.Checks) > 0 {
		fmt.Fprintf(&b, "Checks run: %s\n\n", mdCodeList(doc.Checks))
	}

	if doc.Total == 0 {
		b.WriteString("**Nothing to prune.**\n\n")
	} else {
		var counts []string
		for _, c := range doc.Counts {
			counts = append(counts, fmt.Sprintf("%d to %s", c.Count, c.Action))
		}
		fmt.Fprintf(&b, "**%s flagged** — %s\n\n", plural(doc.Total, "item", "items"), strings.Join(counts, ", "))
	}

	fmt.Fprintf(&b, "> %s\n", doc.Disclaimer)

	if len(doc.Notes) > 0 {
		b.WriteString("\n## Notes\n\n")
		for _, note := range doc.Notes {
			fmt.Fprintf(&b, "- %s\n", mdEscape(note))
		}
	}

	for _, section := range doc.Sections {
		fmt.Fprintf(&b, "\n## %s (%d)\n\n", section.Title, section.Count)

		// A table per indicator. An item that matched several indicators is
		// listed in each of their tables.
		for _, group := range section.Groups {
			fmt.Fprintf(&b, "### %s (%d)\n\n", mdEscape(group.Title), group.Count)
			fmt.Fprintf(&b, "| %s | Age | Owner | Suggested | Why |\n", section.NameHeader)
			b.WriteString("| --- | --- | --- | --- | --- |\n")
			for _, row := range group.Rows {
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
					mdLink(row.Name, row.URL, row.Monospace),
					mdEscape(row.Age),
					mdLink(row.Owner, row.OwnerURL, false),
					row.Action,
					mdEscape(row.Why()))
			}
			b.WriteString("\n")
		}

		// The detail is written once per item, however many indicators listed
		// it above.
		fmt.Fprintf(&b, "### %s in detail\n\n", section.Title)
		for _, row := range section.Rows {
			writeMarkdownDetail(&b, row)
		}
	}

	_, err := io.WriteString(opts.Out, b.String())
	return err
}

func writeMarkdownDetail(b *strings.Builder, row docRow) {
	name := row.Name
	if row.Monospace {
		name = "<code>" + html.EscapeString(name) + "</code>"
	} else {
		name = "<b>" + html.EscapeString(name) + "</b>"
	}

	summary := fmt.Sprintf("%s — %s · %s", name, row.Action, html.EscapeString(row.Age))
	if row.Owner != "" {
		summary += " · " + html.EscapeString(row.Owner)
	}

	// The blank lines around the body are what let a Markdown renderer treat
	// the contents as Markdown rather than raw HTML.
	fmt.Fprintf(b, "<details id=%q>\n<summary>%s</summary>\n\n", row.ID, summary)

	if row.URL != "" {
		fmt.Fprintf(b, "[Open on GitHub](%s)\n\n", row.URL)
	}
	b.WriteString("**Why**\n\n")
	for _, reason := range row.Reasons {
		fmt.Fprintf(b, "- `%s` — %s _(suggests %s)_\n", reason.Check, mdEscape(reason.Summary), reason.Action)
	}

	if len(row.Details) > 0 {
		b.WriteString("\n**Details**\n\n")
		for _, d := range row.Details {
			value := d.Value
			if d.Code {
				value = mdCode(value)
			} else {
				value = mdEscape(value)
			}
			if d.URL != "" {
				value = fmt.Sprintf("[%s](%s)", value, d.URL)
			}
			fmt.Fprintf(b, "- **%s**: %s\n", mdEscape(d.Label), value)
		}
	}

	b.WriteString("\n</details>\n\n")
}

// mdLink renders text as a Markdown link, or as plain text when there is no
// URL to point at.
func mdLink(text, url string, code bool) string {
	if text == "" {
		return ""
	}
	label := mdEscape(text)
	if code {
		label = mdCode(text)
	}
	if url == "" {
		return label
	}
	return fmt.Sprintf("[%s](%s)", label, url)
}

// mdCode renders text as a code span. Backticks stop the content being read as
// Markdown, but a pipe still splits a table cell even inside a span, so it has
// to be escaped. Text that itself contains a backtick cannot be a span at all,
// so it falls back to escaped prose.
func mdCode(s string) string {
	if strings.Contains(s, "`") {
		return mdEscape(s)
	}
	return "`" + strings.ReplaceAll(s, "|", `\|`) + "`"
}

func mdCodeList(items []string) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, "`"+item+"`")
	}
	return strings.Join(out, ", ")
}

// mdEscape neutralises the characters that would break a table cell or turn
// into unintended Markdown. Branch names and commit subjects are arbitrary
// text, so this matters.
func mdEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"|", `\|`,
		"*", `\*`,
		"_", `\_`,
		"`", "\\`",
		"[", `\[`,
		"]", `\]`,
		"<", "&lt;",
		">", "&gt;",
		"\r\n", " ",
		"\n", " ",
		"\r", " ",
	)
	return replacer.Replace(s)
}
