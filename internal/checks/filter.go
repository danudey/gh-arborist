package checks

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/danudey/gh-arborist/internal/gh"
)

// branchFilter decides which branches a check is allowed to flag.
type branchFilter struct {
	defaultBranch string
	excludes      []*pattern
	// bases are the merge bases, which every check treats as excluded: a
	// branch other work is measured against is not a branch to prune.
	bases            []*pattern
	includeProtected bool
}

func newBranchFilter(defaultBranch string, excludes, bases []string, includeProtected bool) (*branchFilter, error) {
	f := &branchFilter{
		defaultBranch:    defaultBranch,
		includeProtected: includeProtected,
	}
	for _, e := range excludes {
		p, err := compilePattern(e)
		if err != nil {
			return nil, err
		}
		f.excludes = append(f.excludes, p)
	}
	for _, b := range bases {
		p, err := compilePattern(b)
		if err != nil {
			return nil, err
		}
		f.bases = append(f.bases, p)
	}
	return f, nil
}

// skip reports whether a branch is off limits, and why.
func (f *branchFilter) skip(b gh.Branch) (bool, string) {
	switch {
	case b.Name == f.defaultBranch:
		return true, "default branch"
	case b.Protected && !f.includeProtected:
		return true, "deletion is blocked by a branch protection rule"
	}
	for _, p := range f.bases {
		if p.match(b.Name) {
			return true, fmt.Sprintf("comparison base matching %q", p.raw)
		}
	}
	for _, p := range f.excludes {
		if p.match(b.Name) {
			return true, fmt.Sprintf("excluded by %q", p.raw)
		}
	}
	return false, ""
}

// eligible returns the branches a check may flag.
func (f *branchFilter) eligible(branches []gh.Branch) []gh.Branch {
	var out []gh.Branch
	for _, b := range branches {
		if skip, _ := f.skip(b); !skip {
			out = append(out, b)
		}
	}
	return out
}

// pattern is a shell-style glob. Unlike path.Match, "*" spans "/" so that a
// pattern like "dependabot/*" and a pattern like "*-wip" both behave the way
// people expect for branch names.
type pattern struct {
	raw string
	re  *regexp.Regexp
}

func compilePattern(glob string) (*pattern, error) {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w", glob, err)
	}
	return &pattern{raw: glob, re: re}, nil
}

func (p *pattern) match(s string) bool { return p.re.MatchString(s) }

func branchNames(branches []gh.Branch) []string {
	out := make([]string, 0, len(branches))
	for _, b := range branches {
		out = append(out, b.Name)
	}
	return out
}
