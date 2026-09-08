package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cli/go-gh/v2/pkg/text"
	"github.com/danudey/gh-arborist/internal/gh"
)

// checkMerged flags branches whose commits are all reachable from another
// branch, so deleting them loses nothing.
//
// Two cases count. A branch can point at exactly the same commit as another
// branch, which is detected for free by comparing tip commit IDs. Or a branch
// can be an ancestor of a base branch, which needs a comparison per branch per
// base.
//
// Squash and rebase merges are invisible to this check because they rewrite
// commits; the closed-pull-request check catches those.
func checkMerged(ctx context.Context, r *Runner, res *Result) error {
	branches, err := r.Branches(ctx)
	if err != nil {
		return err
	}
	bases, err := r.mergeBases(branches)
	if err != nil {
		return err
	}

	// Branches named as a base are already excluded by the runner's filter, so
	// nothing here can suggest deleting one.
	eligible := r.filter.eligible(branches)

	flagDuplicateTips(r, branches, eligible, res)

	type containment struct {
		base string
		cmp  gh.Comparison
	}
	contained := map[string][]containment{}

	tips := map[string]string{}
	for _, b := range branches {
		tips[b.Name] = b.Tip.OID
	}

	for _, base := range bases {
		baseOID := tips[base]
		heads := make([]string, 0, len(eligible))
		for _, b := range eligible {
			// Comparing a branch with itself is meaningless, and identical
			// tips are already handled by flagDuplicateTips.
			if b.Name != base && b.Tip.OID != baseOID {
				heads = append(heads, b.Name)
			}
		}
		cmps, err := r.Client.ContainedIn(ctx, base, heads)
		if err != nil {
			return err
		}
		for name, cmp := range cmps {
			if cmp.Contained() && cmp.Status == "BEHIND" {
				contained[name] = append(contained[name], containment{base: base, cmp: cmp})
			}
		}
	}

	byName := map[string]gh.Branch{}
	for _, b := range branches {
		byName[b.Name] = b
	}
	prsByBranch := map[string][]gh.PullRequest{}
	if r.prsDone {
		prsByBranch = r.prsByBranch
	}

	for name, hits := range contained {
		// Report against the default branch when it is one of the matches,
		// since "merged into main" is the most meaningful phrasing.
		sort.Slice(hits, func(i, j int) bool {
			if di, dj := hits[i].base == r.Info.DefaultBranch, hits[j].base == r.Info.DefaultBranch; di != dj {
				return di
			}
			return hits[i].base < hits[j].base
		})
		best := hits[0]

		summary := fmt.Sprintf("fully merged into %s (%s behind, nothing unique left)",
			best.base, text.Pluralize(best.cmp.BehindBy, "commit"))
		short := "merged into " + best.base
		if len(hits) > 1 {
			others := make([]string, 0, len(hits)-1)
			for _, h := range hits[1:] {
				others = append(others, h.base)
			}
			summary += fmt.Sprintf(", and into %s", strings.Join(others, ", "))
			short += fmt.Sprintf(" (+%d)", len(hits)-1)
		}

		res.Add(r.branchFinding(byName[name], prsByBranch[name],
			Reason{Check: "merged", Category: CategoryMerged, Summary: summary, Short: short, Action: ActionDelete}))
	}

	if !r.Opts.AllBases && len(bases) == 1 && bases[0] == r.Info.DefaultBranch {
		res.Note(fmt.Sprintf("Only %s was used as a merge base. Pass --base to add release branches, or --all-bases to compare every branch against every other.", r.Info.DefaultBranch))
	}
	return nil
}

// flagDuplicateTips reports branches that point at the same commit as another
// branch.
//
// One branch of each set is kept. The default branch wins if it is in the set.
// Otherwise the shortest name wins, because duplicates are usually created by
// suffixing an existing name, as in "fix-bug" and "fix-bug-1"; ties are broken
// alphabetically so the choice is stable between runs.
func flagDuplicateTips(r *Runner, all, eligible []gh.Branch, res *Result) {
	groups := map[string][]gh.Branch{}
	for _, b := range all {
		if b.Tip.OID != "" {
			groups[b.Tip.OID] = append(groups[b.Tip.OID], b)
		}
	}

	canFlag := map[string]bool{}
	for _, b := range eligible {
		canFlag[b.Name] = true
	}
	prsByBranch := map[string][]gh.PullRequest{}
	if r.prsDone {
		prsByBranch = r.prsByBranch
	}

	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool {
			if li, lj := len(group[i].Name), len(group[j].Name); li != lj {
				return li < lj
			}
			return group[i].Name < group[j].Name
		})
		keeper := group[0]
		for _, b := range group {
			if b.Name == r.Info.DefaultBranch {
				keeper = b
				break
			}
		}
		for _, b := range group {
			if b.Name == keeper.Name || !canFlag[b.Name] {
				continue
			}
			res.Add(r.branchFinding(b, prsByBranch[b.Name], Reason{
				Check:    "merged",
				Category: CategoryDuplicate,
				Summary:  fmt.Sprintf("points at the same commit as %s, so it is a duplicate", keeper.Name),
				Short:    "duplicate of " + keeper.Name,
				Action:   ActionDelete,
			}))
		}
	}
}
