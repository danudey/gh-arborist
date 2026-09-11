package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/cli/go-gh/v2/pkg/tableprinter"
	"github.com/cli/go-gh/v2/pkg/text"
	"github.com/danudey/gh-arborist/internal/checks"
)

// renderTable writes the terminal report: one table per kind of item, oldest
// first, with the reasons in their compact form so the rows fit.
//
// Piped output becomes tab-separated values with no header, no colour and the
// reasons written out in full, since a pipe has no width limit.
func renderTable(res *result, opts Options) error {
	c := palette{enabled: opts.Color}
	links := newLinker(opts.Hyperlinks, opts.IsTTY, opts.Env)

	if opts.IsTTY {
		fmt.Fprintf(opts.Out, "%s\n", c.bold(links.wrap(res.RepositoryURL, res.Repository)))
		fmt.Fprintf(opts.Out, "%s · %s · scanned %s\n\n",
			strings.Join(res.ChecksRun, ", "),
			describeScanned(res.Scanned),
			res.ScannedAt.UTC().Format("2006-01-02 15:04 MST"))
	}

	if len(res.Findings) == 0 {
		if opts.IsTTY {
			fmt.Fprintf(opts.Out, "%s Nothing to prune.\n", c.green("✓"))
		}
		writeNotes(res, opts, c)
		return nil
	}

	for _, section := range sections {
		findings := res.OfKind(section.kind)
		if len(findings) == 0 {
			continue
		}

		if !opts.IsTTY {
			// Piped output stays one row per item, with the indicators it
			// matched as a trailing column, so a script never sees the same
			// item twice.
			if err := writeRows(res, opts, section.header, findings, c, links); err != nil {
				return err
			}
			continue
		}

		fmt.Fprintf(opts.Out, "%s\n", c.bold(strings.ToUpper(plural(len(findings), section.singular, section.plural))))

		// A table per indicator. An item that matched several appears under
		// each of them.
		for _, group := range res.GroupsOfKind(section.kind) {
			fmt.Fprintf(opts.Out, "%s\n", c.bold(fmt.Sprintf("  %s (%d)", group.Title, len(group.Findings))))
			if err := writeRows(res, opts, section.header, group.Findings, c, links); err != nil {
				return err
			}
			fmt.Fprintln(opts.Out)
		}
	}

	if opts.IsTTY {
		fmt.Fprintf(opts.Out, "%s\n", summary(res, c))
		fmt.Fprintf(opts.Out, "%s\n", c.gray(disclaimer))
	}
	writeNotes(res, opts, c)
	return nil
}

// writeRows renders one table of findings.
func writeRows(res *result, opts Options, header string, findings []checks.Finding, c palette, links linker) error {
	width := opts.Width
	if width <= 0 {
		width = 120
	}
	t := tableprinter.New(opts.Out, opts.IsTTY, width)
	t.AddHeader([]string{header, "AGE", "OWNER", "SUGGESTED", "WHY"})
	for _, f := range findings {
		action := f.Action()
		if opts.IsTTY {
			// Terminals are narrow, so cap the columns that are only context
			// and spend the remaining width on the reason.
			t.AddField(text.Truncate(48, f.Name), tableprinter.WithColor(links.field(f.URL, nil)))
			t.AddField(shortAge(res.ScannedAt, f.LastActivity), tableprinter.WithColor(c.gray))
			t.AddField(text.Truncate(20, ownerLabel(f.Owner)), tableprinter.WithColor(links.field(f.OwnerURL, c.gray)))
			t.AddField(string(action), tableprinter.WithColor(c.forAction(action)))
			t.AddField(f.WhyShort())
		} else {
			// Piped output is not width-limited, so give the full detail.
			t.AddField(f.Name)
			t.AddField(lastActivity(f.LastActivity, res.ScannedAt))
			t.AddField(f.Owner)
			t.AddField(string(action))
			t.AddField(f.Why())
			t.AddField(categoryList(f))
		}
		t.EndRow()
	}
	return t.Render()
}

// categoryList names the indicators an item matched, for the extra column that
// piped output carries in place of the sub-section headings.
func categoryList(f checks.Finding) string {
	cats := f.Categories()
	out := make([]string, 0, len(cats))
	for _, cat := range cats {
		out = append(out, string(cat))
	}
	return strings.Join(out, ",")
}

// WriteSummary writes the tally of what was found, followed by the
// disclaimer. It is how a run says what it saw when the report itself went
// somewhere else: the terminal report already ends with this block, and the
// Markdown, HTML and JSON reports carry it inside the document.
func WriteSummary(w io.Writer, res *checks.Result, color bool) error {
	c := palette{enabled: color}
	if len(res.Findings) == 0 {
		_, err := fmt.Fprintf(w, "%s Nothing to prune.\n", c.green("✓"))
		return err
	}
	if _, err := fmt.Fprintf(w, "%s\n", summary(res, c)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "%s\n", c.gray(disclaimer))
	return err
}

func summary(res *result, c palette) string {
	var parts []string
	for _, ac := range actionCounts(res) {
		parts = append(parts, c.forAction(ac.Action)(fmt.Sprintf("%d to %s", ac.Count, ac.Action)))
	}
	return fmt.Sprintf("%s flagged: %s", plural(len(res.Findings), "item", "items"), strings.Join(parts, ", "))
}

func writeNotes(res *result, opts Options, c palette) {
	if len(res.Notes) == 0 || opts.ErrOut == nil {
		return
	}
	fmt.Fprintln(opts.ErrOut)
	for _, note := range res.Notes {
		fmt.Fprintf(opts.ErrOut, "%s %s\n", c.yellow("note:"), note)
	}
}
