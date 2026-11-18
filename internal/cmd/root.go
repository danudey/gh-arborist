// Package cmd wires the checks up to the command line.
package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/danudey/gh-arborist/internal/cache"
	"github.com/danudey/gh-arborist/internal/checks"
	"github.com/danudey/gh-arborist/internal/gh"
	"github.com/danudey/gh-arborist/internal/gitrepo"
	"github.com/danudey/gh-arborist/internal/report"
	"github.com/danudey/gh-arborist/internal/source"
	"github.com/danudey/gh-arborist/internal/timeutil"
	"github.com/spf13/cobra"
)

// ErrFindings is returned when --exit-code is set and the report is not empty.
// It carries no message, because the report has already been printed.
var ErrFindings = errors.New("findings reported")

// globals are the flags every subcommand shares.
type globals struct {
	repo string
	// outputs holds the destination given for each format, keyed by format so
	// that the flags and the reports they ask for stay in step.
	outputs          map[report.Format]*string
	hyperlinks       string
	excludes         []string
	includeProtected bool
	verbose          bool
	exitCode         bool
	pageSize         int
	batchSize        int
	git              string
	localRepo        string
	noFetch          bool
	noCache          bool
}

// outputFormats is the flag that asks for each report format, in the order
// the reports are written when more than one is asked for.
var outputFormats = []struct {
	format report.Format
	flag   string
	help   string
}{
	{report.FormatTable, "output-table", "Write the terminal report to this file, or to standard output with -"},
	{report.FormatMarkdown, "output-md", "Write the Markdown report to this file, or to standard output with -"},
	{report.FormatHTML, "output-html", "Write the HTML report to this file, or to standard output with -"},
	{report.FormatJSON, "output-json", "Write the JSON report to this file, or to standard output with -"},
}

// output is one report and where it is going. A path of "-" is standard
// output.
type output struct {
	format report.Format
	flag   string
	path   string
}

// stdout reports whether this report goes to standard output rather than a
// file.
func (o output) stdout() bool { return o.path == "-" }

// checkFlags are the per-check flags, held together so that "all" can offer
// the union of them.
type checkFlags struct {
	olderThan     string
	bases         []string
	allBases      bool
	mergedOnly    bool
	includeClosed bool
	includeBots   bool
	skipForks     bool
	ignoreUsers   []string
	limit         int
	skip          []string
}

// New builds the command tree.
func New(version string) *cobra.Command {
	g := &globals{}

	root := &cobra.Command{
		Use:   "arborist <command>",
		Short: "Report on branches, pull requests and issues that can be pruned",
		Long: heredoc(`
			Arborist inspects a GitHub repository and reports what could be pruned:
			branches whose work has landed, branches nobody has touched in a long
			time, and items belonging to people who have left.

			It only ever reports. Nothing is deleted, closed or reassigned.

			Each check is available on its own, or run them together with
			"gh arborist all".

			By default the terminal report is printed, followed by a summary of
			what was found. Ask for a file with one or more of --output-table,
			--output-md, --output-html and --output-json, and only the summary is
			printed. Any of them takes "-" to write that format to standard
			output.
		`),
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := root.PersistentFlags()
	pf.StringVarP(&g.repo, "repo", "R", "", "Repository to scan, as [HOST/]OWNER/REPO (default: the current directory's repository)")
	g.outputs = make(map[report.Format]*string, len(outputFormats))
	for _, o := range outputFormats {
		dest := new(string)
		g.outputs[o.format] = dest
		pf.StringVar(dest, o.flag, "", o.help)
	}
	pf.StringVar(&g.hyperlinks, "hyperlinks", "auto", "Clickable links in terminal output: auto, always, never")
	pf.StringArrayVar(&g.excludes, "exclude", nil, "Glob of branch names to leave alone; repeatable (for example --exclude 'release/*')")
	pf.BoolVar(&g.includeProtected, "include-protected", false, "Also report branches that branch protection rules forbid deleting")
	pf.BoolVarP(&g.verbose, "verbose", "v", false, "Print progress to stderr while scanning")
	pf.BoolVar(&g.exitCode, "exit-code", false, "Exit 1 when anything is reported, for use in CI")
	pf.IntVar(&g.pageSize, "page-size", 100, "Items to request per API page")
	pf.IntVar(&g.batchSize, "batch-size", 20, "Branch comparisons to bundle into one API request")
	pf.StringVar(&g.git, "git", "auto", "Read what Git already knows from a local clone: "+strings.Join(gitrepo.Modes(), ", "))
	pf.StringVar(&g.localRepo, "local-repo", "", "Path to a clone of the repository to read branches and merge status from")
	pf.BoolVar(&g.noFetch, "no-fetch", false, "Read the local clone as it is, without fetching first")
	pf.BoolVar(&g.noCache, "no-cache", false, "Ignore the local cache and ask GitHub everything again")

	root.AddCommand(
		newClosedPRsCmd(g),
		newMergedCmd(g),
		newStaleCmd(g),
		newOrphansCmd(g),
		newAllCmd(g),
		newCacheCmd(),
	)
	return root
}

// newCacheCmd exposes the cache, so that somebody who suspects a stale answer
// can look at where it lives or throw it away.
func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache <command>",
		Short: "Inspect or clear the local cache",
		Long: heredoc(`
			Arborist remembers answers that cannot change — whether one commit is an
			ancestor of another, which account an email address belongs to — under
			XDG_CACHE_HOME, so a second scan of the same repository asks GitHub far
			less. Use --no-cache on any command to bypass it for one run.
		`),
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the cache directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := cache.Dir()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), dir)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Delete every cached answer",
		Long: heredoc(`
			Delete the cached answers for every repository. Clones made by
			"--git clone" are left alone, since a fetch brings one up to date and
			re-creating it is expensive; delete the "clones" directory by hand to
			remove those.
		`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := cache.Clear()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Cleared %s\n", path)
			return nil
		},
	})
	return cmd
}

func newClosedPRsCmd(g *globals) *cobra.Command {
	f := &checkFlags{}
	cmd := &cobra.Command{
		Use:     "closed-prs",
		Aliases: []string{"prs", "pr"},
		Short:   checkShort("closed-prs"),
		Long: heredoc(`
			Report branches whose pull requests have all been merged or closed, and
			which therefore have nothing left to wait for.

			A branch is skipped while any pull request from it is still open.

			Merged pull requests are reported as safe to delete, including squash and
			rebase merges that leave no trace in the branch's commits. Pull requests
			that were closed without merging are reported for review instead, because
			the branch may hold work somebody still wants.
		`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return execute(cmd, g, f, []string{"closed-prs"})
		},
	}
	registerClosedPRsFlags(cmd, f)
	return cmd
}

func newMergedCmd(g *globals) *cobra.Command {
	f := &checkFlags{}
	cmd := &cobra.Command{
		Use:     "merged",
		Aliases: []string{"contained"},
		Short:   checkShort("merged"),
		Long: heredoc(`
			Report branches whose every commit is already reachable from another
			branch, so deleting them loses no history.

			By default each branch is compared against the repository's default
			branch. Add release branches with --base, or compare every branch against
			every other with --all-bases, which costs a lot more API requests.

			A branch named by --base is excluded from every check, exactly as if it
			had been passed to --exclude.

			Branches pointing at exactly the same commit as another branch are also
			reported as duplicates.

			Squash and rebase merges rewrite commits, so a branch merged that way
			still looks unmerged here. Use "gh arborist closed-prs" to catch those.
		`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return execute(cmd, g, f, []string{"merged"})
		},
	}
	registerMergedFlags(cmd, f)
	return cmd
}

func newStaleCmd(g *globals) *cobra.Command {
	f := &checkFlags{}
	cmd := &cobra.Command{
		Use:     "stale",
		Aliases: []string{"old", "abandoned"},
		Short:   checkShort("stale"),
		Long: heredoc(`
			Report branches whose last commit is older than a threshold, which usually
			means the work was abandoned.

			Age on its own does not make a branch safe to delete, so every result is
			reported for review rather than deletion. Run "gh arborist all" to see
			which of these are also merged, and so safe to remove.

			Thresholds are written as a number and a unit: y, mo, w, d, h, m or s.
			Note that "m" is minutes and "mo" is months. Units can be combined, as in
			"1y6mo".
		`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return execute(cmd, g, f, []string{"stale"})
		},
	}
	registerStaleFlags(cmd, f)
	return cmd
}

func newOrphansCmd(g *globals) *cobra.Command {
	f := &checkFlags{}
	cmd := &cobra.Command{
		Use:     "orphans",
		Aliases: []string{"orphaned", "users"},
		Short:   checkShort("orphans"),
		Long: heredoc(`
			Report branches, pull requests and issues belonging to accounts that no
			longer have access to the repository, or whose account has been deleted.
			This is what is left behind when somebody leaves a company.

			Everyone with any access counts as present: direct collaborators,
			organization members with access through a team, and outside
			collaborators. Reading that list needs push access to the repository.

			This is aimed at private repositories. On a public repository, people who
			opened a pull request or issue without ever having access are normal, so
			expect a long list to triage.

			App accounts are ignored unless --include-bots is given. Only open pull
			requests and issues are examined unless --include-closed is given.
		`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return execute(cmd, g, f, []string{"orphans"})
		},
	}
	registerOrphansFlags(cmd, f)
	return cmd
}

func newAllCmd(g *globals) *cobra.Command {
	f := &checkFlags{}
	cmd := &cobra.Command{
		Use:     "all",
		Aliases: []string{"audit"},
		Short:   "Run every check and report one combined list",
		Long: heredoc(`
			Run every check and report each branch, pull request or issue once, with
			all the reasons it was flagged.

			Where reasons disagree, the most decisive one wins: a branch that is both
			fully merged and owned by a departed colleague is reported as safe to
			delete, because deleting it settles both.

			Data is fetched once and shared between the checks, so this costs little
			more than the most expensive check on its own. Use --skip to leave a check
			out, which is worth doing for "orphans" if you lack push access.
		`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ids, err := selectChecks(f.skip)
			if err != nil {
				return err
			}
			return execute(cmd, g, f, ids)
		},
	}
	registerClosedPRsFlags(cmd, f)
	registerMergedFlags(cmd, f)
	registerStaleFlags(cmd, f)
	registerOrphansFlags(cmd, f)
	cmd.Flags().StringArrayVar(&f.skip, "skip", nil, "Check to leave out; repeatable (closed-prs, merged, stale, orphans)")
	return cmd
}

func registerClosedPRsFlags(cmd *cobra.Command, f *checkFlags) {
	cmd.Flags().BoolVar(&f.mergedOnly, "merged-only", false, "Only report branches whose pull request was merged, ignoring ones closed without merging")
}

func registerMergedFlags(cmd *cobra.Command, f *checkFlags) {
	cmd.Flags().StringArrayVar(&f.bases, "base", nil, "Branch to compare against, as a name or glob; repeatable. Bases are excluded from every check (default: the default branch)")
	cmd.Flags().BoolVar(&f.allBases, "all-bases", false, "Compare every branch against every other branch, which is thorough but slow")
}

func registerStaleFlags(cmd *cobra.Command, f *checkFlags) {
	cmd.Flags().StringVar(&f.olderThan, "older-than", "1y", "Age a branch must exceed to count as stale, for example 1y, 18mo or 90d")
}

func registerOrphansFlags(cmd *cobra.Command, f *checkFlags) {
	cmd.Flags().BoolVar(&f.includeClosed, "include-closed", false, "Also examine closed pull requests and issues")
	cmd.Flags().BoolVar(&f.includeBots, "include-bots", false, "Also report items belonging to app accounts")
	cmd.Flags().BoolVar(&f.skipForks, "skip-forks", false, "Ignore pull requests raised from a fork")
	cmd.Flags().StringArrayVar(&f.ignoreUsers, "ignore-user", nil, "Login to treat as still present; repeatable")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "Most pull requests and issues to examine (0 for no limit)")
}

func selectChecks(skip []string) ([]string, error) {
	skipped := map[string]bool{}
	for _, s := range skip {
		if _, ok := checks.Lookup(s); !ok {
			return nil, fmt.Errorf("unknown check %q; choose from %s", s, strings.Join(checkIDs(), ", "))
		}
		skipped[s] = true
	}
	var ids []string
	for _, c := range checks.All {
		if !skipped[c.ID] {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("every check was skipped, so there is nothing to do")
	}
	return ids, nil
}

func checkIDs() []string {
	var out []string
	for _, c := range checks.All {
		out = append(out, c.ID)
	}
	return out
}

func checkShort(id string) string {
	if c, ok := checks.Lookup(id); ok {
		return c.Short
	}
	return ""
}

// execute resolves the repository, runs the requested checks and prints the
// report.
func execute(cmd *cobra.Command, g *globals, f *checkFlags, ids []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	opts, err := buildOptions(g, f)
	if err != nil {
		return err
	}

	outs, err := resolveOutputs(g)
	if err != nil {
		return err
	}
	hyperlinks, err := report.ParseHyperlinks(g.hyperlinks)
	if err != nil {
		return err
	}
	opts.Details = wantsDetails(outs)

	gitMode, err := gitrepo.ParseMode(g.git)
	if err != nil {
		return err
	}

	repo, err := resolveRepo(g.repo)
	if err != nil {
		return err
	}

	client, err := gh.NewClient(repo)
	if err != nil {
		return err
	}
	if g.pageSize > 0 {
		client.PageSize = g.pageSize
	}
	if g.batchSize > 0 {
		client.CompareBatch = g.batchSize
	}
	var progress func(format string, args ...any)
	if g.verbose {
		progress = func(format string, args ...any) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "· "+format+"\n", args...)
		}
		client.Progress = progress
	}

	src, closeSrc, err := openSource(ctx, repo, client, g, gitMode, progress, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer closeSrc(cmd.ErrOrStderr())

	info, err := src.Info(ctx)
	if err != nil {
		return err
	}

	runner, err := checks.NewRunner(src, info, opts, time.Now())
	if err != nil {
		return err
	}
	res, err := runner.Run(ctx, ids)
	if err != nil {
		return err
	}
	if info.IsArchived {
		res.Note("This repository is archived, so nothing in it can be changed until it is unarchived.")
	}
	if g.noFetch && src.LocalRepo() != "" {
		res.Note(fmt.Sprintf("--no-fetch was given, so branches and merge status are as of the last fetch of %s.", src.LocalRepo()))
	}

	t := term.FromEnv()
	width, _, _ := t.Size()
	// The terminal report ends with the summary itself, so printing the
	// summary again after one would only repeat it.
	summarised := false
	for _, o := range outs {
		// Writing to a file means that report is not going to a terminal,
		// however this process was started.
		isTTY := o.stdout() && t.IsTerminalOutput()
		if err := writeReport(res, o, report.Options{
			ErrOut:     cmd.ErrOrStderr(),
			Format:     o.format,
			IsTTY:      isTTY,
			Color:      o.stdout() && t.IsColorEnabled(),
			Width:      width,
			Hyperlinks: hyperlinks,
		}, cmd.OutOrStdout()); err != nil {
			return err
		}
		if !o.stdout() {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Wrote %s report for %s to %s\n", o.format, res.Repository, o.path)
		}
		summarised = summarised || (o.format == report.FormatTable && isTTY)
	}
	if !summarised {
		sumOut, color := summaryDest(cmd, outs, t)
		if err := report.WriteSummary(sumOut, res, color); err != nil {
			return err
		}
	}

	if g.exitCode && len(res.Findings) > 0 {
		return ErrFindings
	}
	return nil
}

// openSource assembles the data source: the API, plus a local Git repository
// and a cache when they are available. The returned function writes the cache
// back, and reports a failure to do so as a warning rather than an error,
// since a report that has already been produced is not wrong because it could
// not be remembered.
func openSource(ctx context.Context, repo repository.Repository, client *gh.Client, g *globals, mode gitrepo.Mode, progress func(string, ...any), errOut io.Writer) (*source.Source, func(io.Writer), error) {
	var store *cache.Cache
	if !g.noCache {
		// A cache is an optimisation, so not having one is a warning rather
		// than a reason to stop.
		c, err := cache.Open(repo.Host, repo.Owner, repo.Name)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "warning: running without a cache: %v\n", err)
		}
		store = c
	}

	local, err := gitrepo.Open(ctx, repo, gitrepo.Options{
		Mode:      mode,
		LocalPath: g.localRepo,
		NoFetch:   g.noFetch,
		Progress:  progress,
	})
	if err != nil {
		return nil, nil, err
	}
	// A typed nil in an interface is not nil, and a Source must be able to
	// tell that it has no local repository.
	var git source.Git
	if local != nil {
		git = local
	}

	src := source.New(client, source.Options{
		Git:      git,
		Cache:    store,
		Batch:    client.PRBatch,
		Progress: progress,
	})
	return src, func(errOut io.Writer) {
		if err := src.Save(); err != nil {
			_, _ = fmt.Fprintf(errOut, "warning: %v\n", err)
		}
	}, nil
}

// resolveOutputs turns the --output-<format> flags into the list of reports to
// write. With none of them given, the terminal report goes to standard output,
// which is what running the tool by hand should do.
func resolveOutputs(g *globals) ([]output, error) {
	var outs []output
	for _, o := range outputFormats {
		path := strings.TrimSpace(*g.outputs[o.format])
		if path == "" {
			continue
		}
		outs = append(outs, output{format: o.format, flag: o.flag, path: path})
	}
	if len(outs) == 0 {
		return []output{{format: report.FormatTable, flag: "output-table", path: "-"}}, nil
	}
	// Two reports sharing a destination would interleave into nonsense, or
	// leave a file holding only whichever was written last.
	seen := map[string]output{}
	for _, o := range outs {
		if first, ok := seen[o.path]; ok {
			where := o.path
			if o.stdout() {
				where = "standard output"
			}
			return nil, fmt.Errorf("--%s and --%s cannot both write to %s", first.flag, o.flag, where)
		}
		seen[o.path] = o
	}
	return outs, nil
}

// writeReport renders one report and closes its destination, so that writing
// several does not have to leave every file open until the command returns.
func writeReport(res *checks.Result, o output, opts report.Options, stdout io.Writer) error {
	out, closeOut, err := openOutput(o.path, stdout)
	if err != nil {
		return err
	}
	defer func() { _ = closeOut() }()
	opts.Out = out
	if err := report.Render(res, opts); err != nil {
		return err
	}
	return closeOut()
}

// summaryDest picks where the summary goes. Standard output is the point of
// it, unless a report is already going there, in which case appending the
// summary could corrupt a document or confuse whatever is reading the stream.
func summaryDest(cmd *cobra.Command, outs []output, t term.Term) (io.Writer, bool) {
	for _, o := range outs {
		if o.stdout() {
			return cmd.ErrOrStderr(), false
		}
	}
	return cmd.OutOrStdout(), t.IsTerminalOutput() && t.IsColorEnabled()
}

// openOutput returns the writer a report goes to. A second call to the
// returned function is harmless, so it works as both a defer and an explicit
// close whose error is checked.
func openOutput(path string, fallback io.Writer) (io.Writer, func() error, error) {
	if path == "" || path == "-" {
		return fallback, func() error { return nil }, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, fmt.Errorf("could not write the report: %w", err)
	}
	buf := bufio.NewWriter(f)
	closed := false
	return buf, func() error {
		if closed {
			return nil
		}
		closed = true
		if err := buf.Flush(); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}, nil
}

func buildOptions(g *globals, f *checkFlags) (checks.Options, error) {
	opts := checks.Options{
		Excludes:         g.excludes,
		IncludeProtected: g.includeProtected,
		Bases:            f.bases,
		AllBases:         f.allBases,
		MergedOnly:       f.mergedOnly,
		IncludeClosed:    f.includeClosed,
		IncludeBots:      f.includeBots,
		SkipForks:        f.skipForks,
		IgnoreUsers:      f.ignoreUsers,
		Limit:            f.limit,
	}
	if f.olderThan != "" {
		age, err := timeutil.ParseAge(f.olderThan)
		if err != nil {
			return opts, err
		}
		opts.Age = age
	}
	if f.allBases && len(f.bases) > 0 {
		return opts, errors.New("--base and --all-bases cannot be used together")
	}
	return opts, nil
}

// wantsDetails reports whether any report being written shows the per-item
// detail, and so justifies the extra pull request lookup that gathering it
// costs.
func wantsDetails(outs []output) bool {
	for _, o := range outs {
		if o.format == report.FormatHTML || o.format == report.FormatMarkdown {
			return true
		}
	}
	return false
}

func resolveRepo(spec string) (repository.Repository, error) {
	if spec != "" {
		return repository.Parse(spec)
	}
	repo, err := repository.Current()
	if err != nil {
		return repo, fmt.Errorf("could not work out which repository to scan: %w\nRun this inside a repository, or pass --repo OWNER/REPO", err)
	}
	return repo, nil
}

// heredoc trims the leading tab indentation used to keep long help text
// readable in the source.
func heredoc(s string) string {
	lines := strings.Split(strings.Trim(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, strings.Repeat("\t", 3))
	}
	return strings.Join(lines, "\n")
}
