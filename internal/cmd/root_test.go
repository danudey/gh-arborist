package cmd

import (
	"strings"
	"testing"

	"github.com/danudey/gh-arborist/internal/checks"
	"github.com/danudey/gh-arborist/internal/report"
	"github.com/spf13/pflag"
)

func TestSelectChecks(t *testing.T) {
	all, err := selectChecks(nil)
	if err != nil {
		t.Fatalf("selectChecks(nil): %v", err)
	}
	if len(all) != len(checks.All) {
		t.Errorf("got %d checks, want all %d", len(all), len(checks.All))
	}

	some, err := selectChecks([]string{"orphans"})
	if err != nil {
		t.Fatalf("selectChecks: %v", err)
	}
	for _, id := range some {
		if id == "orphans" {
			t.Error("--skip orphans should have left it out")
		}
	}
	if len(some) != len(checks.All)-1 {
		t.Errorf("got %d checks, want %d", len(some), len(checks.All)-1)
	}

	if _, err := selectChecks([]string{"nonsense"}); err == nil {
		t.Error("skipping an unknown check should fail")
	} else if !strings.Contains(err.Error(), "closed-prs") {
		t.Errorf("the error should list the valid checks, got %q", err)
	}

	if _, err := selectChecks(checkIDs()); err == nil {
		t.Error("skipping every check should fail rather than doing nothing")
	}
}

func TestBuildOptions(t *testing.T) {
	g := &globals{excludes: []string{"tmp/*"}, includeProtected: true}
	f := &checkFlags{olderThan: "18mo", bases: []string{"release/*"}, ignoreUsers: []string{"bob"}, limit: 50}

	opts, err := buildOptions(g, f)
	if err != nil {
		t.Fatalf("buildOptions: %v", err)
	}
	if opts.Age.Months != 18 {
		t.Errorf("age is %+v, want 18 months", opts.Age)
	}
	if len(opts.Excludes) != 1 || opts.Excludes[0] != "tmp/*" {
		t.Errorf("excludes are %v", opts.Excludes)
	}
	if !opts.IncludeProtected || opts.Limit != 50 {
		t.Errorf("flags did not carry through: %+v", opts)
	}
}

func TestBuildOptionsRejectsBadInput(t *testing.T) {
	if _, err := buildOptions(&globals{}, &checkFlags{olderThan: "6months"}); err == nil {
		t.Error("an unparseable age should fail")
	}
	if _, err := buildOptions(&globals{}, &checkFlags{olderThan: "1y", bases: []string{"main"}, allBases: true}); err == nil {
		t.Error("--base with --all-bases should fail")
	}
}

func TestCommandTree(t *testing.T) {
	root := New("test")

	want := map[string][]string{
		"closed-prs": {"prs", "pr"},
		"merged":     {"contained"},
		"stale":      {"old", "abandoned"},
		"orphans":    {"orphaned", "users"},
		"all":        {"audit"},
	}
	for name, aliases := range want {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Errorf("command %q is missing", name)
			continue
		}
		for _, alias := range aliases {
			found, _, err := root.Find([]string{alias})
			if err != nil || found.Name() != name {
				t.Errorf("alias %q does not resolve to %q", alias, name)
			}
		}
		if cmd.Short == "" || cmd.Long == "" {
			t.Errorf("command %q needs help text", name)
		}
	}

	// "all" must accept the flags of every check it runs.
	all, _, err := root.Find([]string{"all"})
	if err != nil {
		t.Fatal("all command is missing")
	}
	for _, flag := range []string{"older-than", "base", "all-bases", "merged-only", "include-closed", "include-bots", "skip-forks", "ignore-user", "limit", "skip"} {
		if all.Flags().Lookup(flag) == nil {
			t.Errorf("the all command is missing --%s", flag)
		}
	}

	// Where the data comes from is a global choice, so every check must offer
	// it.
	for _, flag := range []string{"git", "local-repo", "no-fetch", "no-cache"} {
		if root.PersistentFlags().Lookup(flag) == nil {
			t.Errorf("--%s is not a global flag", flag)
		}
	}

	// So is where each report goes, and the old single-report flags must not
	// linger and quietly do nothing.
	for _, o := range outputFormats {
		if root.PersistentFlags().Lookup(o.flag) == nil {
			t.Errorf("--%s is not a global flag", o.flag)
		}
	}
	for _, flag := range []string{"format", "json", "output"} {
		if root.PersistentFlags().Lookup(flag) != nil {
			t.Errorf("--%s was replaced by the --output-<format> flags", flag)
		}
	}

	// The cache is not a check, but it has to be reachable.
	for _, sub := range []string{"path", "clear"} {
		cmd, _, err := root.Find([]string{"cache", sub})
		if err != nil || cmd.Name() != sub {
			t.Errorf("cache %s is missing", sub)
		}
	}
}

// The tool must not grow flags that change anything on GitHub while it is
// documented as report-only.
func TestNoMutatingFlags(t *testing.T) {
	root := New("test")
	banned := []string{"delete", "prune", "close", "reassign", "fix", "apply", "yes", "force"}
	for _, cmd := range root.Commands() {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			for _, bad := range banned {
				if f.Name == bad {
					t.Errorf("%s has a --%s flag, but this version only reports", cmd.Name(), f.Name)
				}
			}
		})
	}
}

// testGlobals builds the globals the output flags would have produced, so the
// resolution can be tested without going through cobra.
func testGlobals(paths map[report.Format]string) *globals {
	g := &globals{outputs: map[report.Format]*string{}}
	for _, o := range outputFormats {
		path := paths[o.format]
		g.outputs[o.format] = &path
	}
	return g
}

func TestResolveOutputs(t *testing.T) {
	// Running the tool by hand and asking for nothing gets the terminal
	// report on standard output.
	outs, err := resolveOutputs(testGlobals(nil))
	if err != nil {
		t.Fatalf("resolveOutputs: %v", err)
	}
	if len(outs) != 1 || outs[0].format != report.FormatTable || !outs[0].stdout() {
		t.Errorf("the default should be the table report on standard output, got %+v", outs)
	}

	// Several formats at once, each to its own destination, written in a
	// predictable order.
	outs, err = resolveOutputs(testGlobals(map[report.Format]string{
		report.FormatJSON:     "-",
		report.FormatHTML:     "out.html",
		report.FormatMarkdown: "out.md",
	}))
	if err != nil {
		t.Fatalf("resolveOutputs: %v", err)
	}
	want := []output{
		{format: report.FormatMarkdown, flag: "output-md", path: "out.md"},
		{format: report.FormatHTML, flag: "output-html", path: "out.html"},
		{format: report.FormatJSON, flag: "output-json", path: "-"},
	}
	if len(outs) != len(want) {
		t.Fatalf("got %d outputs, want %d: %+v", len(outs), len(want), outs)
	}
	for i, w := range want {
		if outs[i] != w {
			t.Errorf("output %d is %+v, want %+v", i, outs[i], w)
		}
	}
}

// Two reports sharing a destination would interleave, or leave a file holding
// only one of them, so refuse rather than write something misleading.
func TestResolveOutputsRejectsSharedDestinations(t *testing.T) {
	if _, err := resolveOutputs(testGlobals(map[report.Format]string{
		report.FormatHTML: "-",
		report.FormatJSON: "-",
	})); err == nil {
		t.Error("two formats on standard output should fail")
	} else if !strings.Contains(err.Error(), "standard output") {
		t.Errorf("the error should say where the clash is, got %q", err)
	}

	if _, err := resolveOutputs(testGlobals(map[report.Format]string{
		report.FormatHTML:     "report.out",
		report.FormatMarkdown: "report.out",
	})); err == nil {
		t.Error("two formats writing to one file should fail")
	}
}

// The detail-rich formats have to pay for the pull request lookup; the terminal
// and JSON reports should not.
func TestDetailsOnlyForRichFormats(t *testing.T) {
	for format, want := range map[report.Format]bool{
		report.FormatHTML:     true,
		report.FormatMarkdown: true,
		report.FormatTable:    false,
		report.FormatJSON:     false,
	} {
		if got := wantsDetails([]output{{format: format, path: "-"}}); got != want {
			t.Errorf("format %q: wantsDetails() = %v, want %v", format, got, want)
		}
	}
	// One rich format among several is enough to pay for the detail.
	if !wantsDetails([]output{{format: report.FormatTable}, {format: report.FormatHTML}}) {
		t.Error("wantsDetails() should be true when any output wants the detail")
	}
}
