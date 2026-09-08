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

func TestResolveFormat(t *testing.T) {
	tests := []struct {
		name    string
		globals globals
		want    report.Format
		wantErr bool
	}{
		{"default", globals{format: "table"}, report.FormatTable, false},
		{"markdown", globals{format: "md"}, report.FormatMarkdown, false},
		{"html", globals{format: "html"}, report.FormatHTML, false},
		{"json flag", globals{format: "table", jsonOut: true}, report.FormatJSON, false},
		{"json flag with json format", globals{format: "json", jsonOut: true}, report.FormatJSON, false},
		// Silently ignoring one of two conflicting requests would be worse
		// than refusing.
		{"json flag with html format", globals{format: "html", jsonOut: true}, "", true},
		{"unknown format", globals{format: "pdf"}, "", true},
	}
	for _, tt := range tests {
		got, err := resolveFormat(&tt.globals)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error, got %q", tt.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
		} else if got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
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
		if got := wantsDetails(format); got != want {
			t.Errorf("format %q: wantsDetails() = %v, want %v", format, got, want)
		}
	}
}
