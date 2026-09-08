package checks

import (
	"strings"
	"testing"

	"github.com/danudey/gh-arborist/internal/gh"
)

func TestPatternMatching(t *testing.T) {
	tests := []struct {
		glob, name string
		want       bool
	}{
		{"release/*", "release/1.0", true},
		{"release/*", "release/v2/rc1", true}, // "*" spans "/", unlike path.Match
		{"release/*", "releases/1.0", false},
		{"*-wip", "feature/thing-wip", true},
		{"*", "anything/at/all", true},
		{"main", "main", true},
		{"main", "maintenance", false},
		{"fix-?", "fix-1", true},
		{"fix-?", "fix-12", false},
		{"a.b", "a.b", true},
		{"a.b", "axb", false}, // "." is literal, not a regexp wildcard
	}
	for _, tt := range tests {
		p, err := compilePattern(tt.glob)
		if err != nil {
			t.Errorf("compilePattern(%q): %v", tt.glob, err)
			continue
		}
		if got := p.match(tt.name); got != tt.want {
			t.Errorf("pattern %q against %q = %v, want %v", tt.glob, tt.name, got, tt.want)
		}
	}
}

func TestBranchFilterSkipReasons(t *testing.T) {
	f, err := newBranchFilter("main", []string{"dependabot/*"}, []string{"release/*"}, false)
	if err != nil {
		t.Fatalf("newBranchFilter: %v", err)
	}

	tests := []struct {
		branch     gh.Branch
		skip       bool
		reasonPart string
	}{
		{gh.Branch{Name: "main"}, true, "default branch"},
		{gh.Branch{Name: "release/1.0"}, true, "comparison base"},
		{gh.Branch{Name: "release/2.0"}, true, "comparison base"},
		{gh.Branch{Name: "dependabot/npm/lodash"}, true, "excluded"},
		{gh.Branch{Name: "locked", Protected: true}, true, "protection"},
		{gh.Branch{Name: "feature/ok"}, false, ""},
	}
	for _, tt := range tests {
		skip, reason := f.skip(tt.branch)
		if skip != tt.skip {
			t.Errorf("skip(%q) = %v, want %v", tt.branch.Name, skip, tt.skip)
			continue
		}
		if tt.reasonPart != "" && !strings.Contains(reason, tt.reasonPart) {
			t.Errorf("skip(%q) reason = %q, want it to mention %q", tt.branch.Name, reason, tt.reasonPart)
		}
	}
}

func TestBranchFilterIncludeProtected(t *testing.T) {
	f, err := newBranchFilter("main", nil, nil, true)
	if err != nil {
		t.Fatalf("newBranchFilter: %v", err)
	}
	if skip, reason := f.skip(gh.Branch{Name: "locked", Protected: true}); skip {
		t.Errorf("with IncludeProtected the branch should not be skipped, got %q", reason)
	}
	// The default branch stays off limits even then, because deleting it is
	// never the right suggestion.
	if skip, _ := f.skip(gh.Branch{Name: "main"}); !skip {
		t.Error("the default branch must always be skipped")
	}
}

func TestBranchFilterEligible(t *testing.T) {
	f, err := newBranchFilter("main", []string{"tmp/*"}, nil, false)
	if err != nil {
		t.Fatalf("newBranchFilter: %v", err)
	}
	in := []gh.Branch{{Name: "main"}, {Name: "tmp/x"}, {Name: "a"}, {Name: "b"}}
	got := branchNames(f.eligible(in))
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("eligible = %v, want [a b]", got)
	}
}

func TestCompilePatternRejectsNothing(t *testing.T) {
	// Regexp metacharacters in a branch name must be quoted, not rejected, so
	// any legal branch name can be used as a pattern.
	for _, glob := range []string{"feat(ure)", "a+b", "[weird]", "^caret$"} {
		if _, err := compilePattern(glob); err != nil {
			t.Errorf("compilePattern(%q) failed: %v", glob, err)
		}
	}
}
