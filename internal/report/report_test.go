package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/danudey/gh-arborist/internal/checks"
)

var now = time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

func sampleResult() *checks.Result {
	res := checks.NewResult("acme/widgets", now)
	res.ChecksRun = []string{"merged", "stale"}
	res.Scanned["branches"] = 12
	res.Add(checks.Finding{
		Kind:         checks.KindBranch,
		Name:         "feature/landed",
		Owner:        "alice",
		LastActivity: now.AddDate(0, 0, -400),
		Reasons: []checks.Reason{
			{Check: "merged", Category: checks.CategoryMerged, Summary: "fully merged into main (7 commits behind, nothing unique left)", Short: "merged into main", Action: checks.ActionDelete},
			{Check: "stale", Category: checks.CategoryStale, Summary: "no commits for about 1 year (last was 2025-04-27), which is over the 1y threshold", Short: "idle over 1y", Action: checks.ActionReview},
		},
	})
	res.Add(checks.Finding{
		Kind:         checks.KindIssue,
		Name:         "#42",
		Title:        "login broken",
		Owner:        "bob",
		LastActivity: now.AddDate(0, 0, -10),
		Reasons: []checks.Reason{
			{Check: "orphans", Category: checks.CategoryAssigneeNoAccess, Summary: "assigned to bob, and bob no longer has access to this repository", Short: "assignee bob has no access", Action: checks.ActionReassign},
		},
	})
	res.Note("a caveat")
	res.Sort()
	return res
}

// Piped output must stay tab-separated with no header and no colour, so it can
// be fed to cut or awk.
func TestRenderNonTTYIsTabSeparated(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := Render(sampleResult(), Options{Out: &out, ErrOut: &errOut, Format: FormatTable, IsTTY: false, Color: false, Width: 200}); err != nil {
		t.Fatalf("Render: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want one per finding:\n%s", len(lines), out.String())
	}
	if strings.Contains(out.String(), "BRANCH") {
		t.Error("piped output should have no header row")
	}
	if strings.Contains(out.String(), string(rune(27))) {
		t.Error("piped output should have no colour codes")
	}

	fields := strings.Split(lines[0], "\t")
	if len(fields) != 6 {
		t.Fatalf("got %d columns, want 6: %q", len(fields), lines[0])
	}
	if fields[0] != "feature/landed" || fields[2] != "alice" || fields[3] != "delete" {
		t.Errorf("unexpected columns: %q", fields[0:4])
	}
	// Piped output has room for the full reasons.
	if !strings.Contains(fields[4], "nothing unique left") {
		t.Errorf("piped output should carry the full reason, got %q", fields[4])
	}
	// A pipe gets the sub-categories as a column rather than as headings, so
	// that a script still sees each item exactly once.
	if fields[5] != "merged-into-base,older-than-threshold" {
		t.Errorf("category column = %q", fields[5])
	}
}

func TestRenderTTYUsesShortReasonsAndHeader(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := Render(sampleResult(), Options{Out: &out, ErrOut: &errOut, Format: FormatTable, IsTTY: true, Color: false, Width: 100}); err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"acme/widgets", "BRANCH", "1 BRANCH", "1 ISSUE",
		"merged into main; idle over 1y", "2 items flagged",
		// A sub-heading per indicator, with the branch under both of its own.
		"Fully merged into another branch (1)",
		"Older than the age threshold (1)",
		"Assignee no longer has access (1)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("terminal output is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "nothing unique left") {
		t.Error("terminal output should use the short reasons, which fit the table")
	}
	if !strings.Contains(errOut.String(), "a caveat") {
		t.Errorf("notes belong on stderr, got %q", errOut.String())
	}
}

func TestRenderEmpty(t *testing.T) {
	var out, errOut bytes.Buffer
	res := checks.NewResult("acme/widgets", now)
	res.ChecksRun = []string{"stale"}

	if err := Render(res, Options{Out: &out, ErrOut: &errOut, Format: FormatTable, IsTTY: true, Width: 100}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out.String(), "Nothing to prune") {
		t.Errorf("an empty result should say so, got %q", out.String())
	}

	out.Reset()
	if err := Render(res, Options{Out: &out, ErrOut: &errOut, Format: FormatTable, IsTTY: false, Width: 100}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("an empty result should print nothing when piped, got %q", out.String())
	}
}

func TestRenderJSON(t *testing.T) {
	var out bytes.Buffer
	if err := Render(sampleResult(), Options{Out: &out, Format: FormatJSON}); err != nil {
		t.Fatalf("Render: %v", err)
	}

	var decoded struct {
		Repository string   `json:"repository"`
		ChecksRun  []string `json:"checksRun"`
		Findings   []struct {
			Kind    string `json:"kind"`
			Name    string `json:"name"`
			Reasons []struct {
				Check   string `json:"check"`
				Summary string `json:"summary"`
				Action  string `json:"suggestedAction"`
			} `json:"reasons"`
		} `json:"findings"`
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if decoded.Repository != "acme/widgets" || len(decoded.Findings) != 2 {
		t.Fatalf("unexpected decoded result: %+v", decoded)
	}
	if len(decoded.Findings[0].Reasons) != 2 {
		t.Errorf("both reasons should survive into JSON, got %+v", decoded.Findings[0].Reasons)
	}
	if decoded.Findings[0].Reasons[0].Action != "delete" {
		t.Errorf("suggested action is %q, want delete", decoded.Findings[0].Reasons[0].Action)
	}
	if len(decoded.Notes) != 1 {
		t.Errorf("notes should appear in JSON, got %v", decoded.Notes)
	}
}

func TestShortAge(t *testing.T) {
	tests := []struct {
		days int
		want string
	}{
		{0, "today"},
		{5, "5d"},
		{29, "29d"},
		{60, "2mo"},
		{364, "12mo"},
		{400, "1y"},
		{800, "2y"},
	}
	for _, tt := range tests {
		if got := shortAge(now, now.AddDate(0, 0, -tt.days)); got != tt.want {
			t.Errorf("shortAge(%d days ago) = %q, want %q", tt.days, got, tt.want)
		}
	}
	if got := shortAge(now, time.Time{}); got != "?" {
		t.Errorf("shortAge of an unknown time = %q, want ?", got)
	}
}

func TestPlural(t *testing.T) {
	if got := plural(1, "branch", "branches"); got != "1 branch" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(3, "branch", "branches"); got != "3 branches" {
		t.Errorf("plural(3) = %q", got)
	}
	if got := plural(0, "branch", "branches"); got != "0 branches" {
		t.Errorf("plural(0) = %q", got)
	}
}

func TestOwnerLabel(t *testing.T) {
	if got := ownerLabel("Nate Smith <nate@example.com>"); got != "Nate Smith" {
		t.Errorf("ownerLabel dropped the wrong part: %q", got)
	}
	if got := ownerLabel("alice"); got != "alice" {
		t.Errorf("ownerLabel changed a plain login: %q", got)
	}
	if got := ownerLabel(""); got != "" {
		t.Errorf("ownerLabel of an empty owner = %q", got)
	}
}
