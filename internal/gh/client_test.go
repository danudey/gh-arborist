package gh

import (
	"strings"
	"testing"
)

func TestChunk(t *testing.T) {
	in := []string{"a", "b", "c", "d", "e"}

	got := chunk(in, 2)
	if len(got) != 3 {
		t.Fatalf("chunk into 2s gave %d batches, want 3: %v", len(got), got)
	}
	if len(got[2]) != 1 || got[2][0] != "e" {
		t.Errorf("last batch is %v, want [e]", got[2])
	}

	if got := chunk(in, 10); len(got) != 1 || len(got[0]) != 5 {
		t.Errorf("chunk larger than the input gave %v, want one batch of 5", got)
	}
	if got := chunk([]string{}, 3); len(got) != 0 {
		t.Errorf("chunk of nothing gave %v, want no batches", got)
	}
	// A nonsense size must not divide by zero or loop forever.
	if got := chunk(in, 0); len(got) != 5 {
		t.Errorf("chunk with size 0 gave %d batches, want one per item", len(got))
	}
}

// Branch names are user data, so they must travel as GraphQL variables rather
// than being pasted into the query text.
func TestBuildPRBatchPassesNamesAsVariables(t *testing.T) {
	names := []string{"feature/one", `evil") { x } #`}
	query, vars := buildPRBatch(names)

	for _, name := range names {
		if strings.Contains(query, name) {
			t.Errorf("query text contains the branch name %q; it should only appear in variables:\n%s", name, query)
		}
	}
	if vars["h0"] != names[0] || vars["h1"] != names[1] {
		t.Errorf("variables are %v, want h0 and h1 set to the branch names", vars)
	}
	for _, want := range []string{"$h0: String!", "$h1: String!", "b0: pullRequests(headRefName: $h0", "b1: pullRequests(headRefName: $h1", "fragment prFields"} {
		if !strings.Contains(query, want) {
			t.Errorf("query is missing %q:\n%s", want, query)
		}
	}
}

func TestBuildCompareBatch(t *testing.T) {
	query, vars := buildCompareBatch([]string{"a", "b", "c"})

	if len(vars) != 3 {
		t.Errorf("got %d variables, want 3: %v", len(vars), vars)
	}
	for i, name := range []string{"a", "b", "c"} {
		if vars[alias("h", i)] != name {
			t.Errorf("variable %s is %v, want %q", alias("h", i), vars[alias("h", i)], name)
		}
		if want := alias("c", i) + ": compare(headRef: $" + alias("h", i) + ")"; !strings.Contains(query, want) {
			t.Errorf("query is missing %q:\n%s", want, query)
		}
	}
	if !strings.Contains(query, "ref(qualifiedName: $base)") {
		t.Errorf("query should compare against the base ref:\n%s", query)
	}
}

func TestBuildCommitBatch(t *testing.T) {
	oids := []string{"aaa111", "bbb222"}
	query, vars := buildCommitBatch(oids)

	for _, oid := range oids {
		if strings.Contains(query, oid) {
			t.Errorf("query text contains the commit ID %q; it should only appear in variables:\n%s", oid, query)
		}
	}
	for i, oid := range oids {
		if vars[alias("o", i)] != oid {
			t.Errorf("variable %s is %v, want %q", alias("o", i), vars[alias("o", i)], oid)
		}
		if want := alias("c", i) + ": object(oid: $" + alias("o", i) + ")"; !strings.Contains(query, want) {
			t.Errorf("query is missing %q:\n%s", want, query)
		}
	}
	// Both identities are wanted, since a commit's committer differs from its
	// author for cherry-picks and web edits.
	for _, want := range []string{"author { email user { login } }", "committer { email user { login } }"} {
		if !strings.Contains(query, want) {
			t.Errorf("query is missing %q:\n%s", want, query)
		}
	}
}

func TestComparisonContained(t *testing.T) {
	tests := []struct {
		cmp  Comparison
		want bool
	}{
		{Comparison{Status: "BEHIND", AheadBy: 0, BehindBy: 10}, true},
		{Comparison{Status: "IDENTICAL", AheadBy: 0, BehindBy: 0}, true},
		{Comparison{Status: "DIVERGED", AheadBy: 2, BehindBy: 10}, false},
		{Comparison{Status: "AHEAD", AheadBy: 5, BehindBy: 0}, false},
	}
	for _, tt := range tests {
		if got := tt.cmp.Contained(); got != tt.want {
			t.Errorf("Comparison%+v.Contained() = %v, want %v", tt.cmp, got, tt.want)
		}
	}
}

func TestActorClassification(t *testing.T) {
	tests := []struct {
		actor    Actor
		isBot    bool
		known    bool
		describe string
	}{
		{Actor{Login: "alice", Type: "User"}, false, true, "alice"},
		{Actor{Login: "dependabot[bot]", Type: "User"}, true, true, "dependabot[bot]"},
		{Actor{Login: "renovate", Type: "Bot"}, true, true, "renovate"},
		{Actor{Login: "ghost"}, false, false, "ghost"},
		{Actor{Name: "Nate", Email: "nate@example.com"}, false, false, "Nate <nate@example.com>"},
		{Actor{Name: "Nate"}, false, false, "Nate"},
		{Actor{}, false, false, "(unknown)"},
	}
	for _, tt := range tests {
		if got := tt.actor.IsBot(); got != tt.isBot {
			t.Errorf("Actor%+v.IsBot() = %v, want %v", tt.actor, got, tt.isBot)
		}
		if got := tt.actor.Known(); got != tt.known {
			t.Errorf("Actor%+v.Known() = %v, want %v", tt.actor, got, tt.known)
		}
		if got := tt.actor.Describe(); got != tt.describe {
			t.Errorf("Actor%+v.Describe() = %q, want %q", tt.actor, got, tt.describe)
		}
	}
}

func TestCanListCollaborators(t *testing.T) {
	for perm, want := range map[string]bool{
		"READ": false, "TRIAGE": false, "WRITE": true, "MAINTAIN": true, "ADMIN": true, "": false,
	} {
		if got := (Info{ViewerPermission: perm}).CanListCollaborators(); got != want {
			t.Errorf("permission %q: CanListCollaborators() = %v, want %v", perm, got, want)
		}
	}
}

func TestPageSizeForRespectsLimit(t *testing.T) {
	c := &Client{PageSize: 100}
	tests := []struct {
		limit, have, want int
	}{
		{0, 0, 100},   // no limit
		{0, 500, 100}, // no limit, part way through
		{250, 0, 100}, // limit well above a page
		{250, 200, 50},
		{30, 0, 30}, // limit below a page
	}
	for _, tt := range tests {
		if got := c.pageSizeFor(tt.limit, tt.have); got != tt.want {
			t.Errorf("pageSizeFor(limit=%d, have=%d) = %d, want %d", tt.limit, tt.have, got, tt.want)
		}
	}
}
