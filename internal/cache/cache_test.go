package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openIn points the cache at a temporary directory, so a test never touches
// the real one.
func openIn(t *testing.T, dir string) *Cache {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", dir)
	c, err := Open("github.com", "owner", "repo")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return c
}

func TestRoundTripSurvivesReopening(t *testing.T) {
	dir := t.TempDir()

	c := openIn(t, dir)
	c.Put(Key("contained", "base", "head"), map[string]int{"behind": 7}, Never)
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var got map[string]int
	if !openIn(t, dir).Get(Key("contained", "base", "head"), &got) {
		t.Fatal("the entry was not there after reopening")
	}
	if got["behind"] != 7 {
		t.Errorf("got %v, want behind=7", got)
	}
}

func TestExpiredEntriesAreGone(t *testing.T) {
	dir := t.TempDir()

	c := openIn(t, dir)
	c.Put("fresh", "yes", time.Hour)
	c.Put("stale", "no", -time.Second)
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened := openIn(t, dir)
	var s string
	if reopened.Get("stale", &s) {
		t.Errorf("an expired entry came back as %q", s)
	}
	if !reopened.Get("fresh", &s) || s != "yes" {
		t.Errorf("the unexpired entry is %q, ok=%v", s, reopened.Get("fresh", &s))
	}
}

// A cache is an optimisation, so a damaged one must read as empty rather than
// stopping a scan.
func TestCorruptFileReadsAsEmpty(t *testing.T) {
	dir := t.TempDir()
	c := openIn(t, dir)
	c.Put("k", "v", Never)
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(c.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	var s string
	if openIn(t, dir).Get("k", &s) {
		t.Errorf("a corrupt file yielded %q", s)
	}
}

// A value that no longer decodes into what the caller expects — because the
// type changed between versions — must not be mistaken for a hit.
func TestMismatchedTypeIsAMiss(t *testing.T) {
	c := openIn(t, t.TempDir())
	c.Put("k", "a string", Never)

	var n int
	if c.Get("k", &n) {
		t.Errorf("decoding a string into an int reported a hit, giving %d", n)
	}
	if c.Misses != 1 {
		t.Errorf("Misses = %d, want 1", c.Misses)
	}
}

// Nothing is written unless something changed, so a scan that only reads does
// not rewrite the file.
func TestSaveOnlyWritesWhenSomethingChanged(t *testing.T) {
	dir := t.TempDir()
	c := openIn(t, dir)
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(c.Path()); !os.IsNotExist(err) {
		t.Errorf("Save wrote %s despite nothing having changed", c.Path())
	}
}

// A nil cache is how --no-cache is implemented, so every method must tolerate
// it.
func TestNilCacheIsInert(t *testing.T) {
	var c *Cache
	var s string
	if c.Get("k", &s) {
		t.Error("a nil cache reported a hit")
	}
	c.Put("k", "v", Never)
	if err := c.Save(); err != nil {
		t.Errorf("Save on a nil cache: %v", err)
	}
	if c.Path() != "" {
		t.Errorf("Path on a nil cache = %q", c.Path())
	}
}

// Keys are built from branch names and email addresses, so two different sets
// of parts must never collapse into one key.
func TestKeyPartsCannotRunTogether(t *testing.T) {
	if Key("ns", "ab", "c") == Key("ns", "a", "bc") {
		t.Error("differently split parts produced the same key")
	}
	if Key("ns", "a") == Key("nsa") {
		t.Error("a part ran into the namespace")
	}
}

// Repository and host names come from the command line, so they must not be
// able to point the cache outside its own directory.
func TestSegmentCannotEscape(t *testing.T) {
	for _, in := range []string{"..", "../../etc", "a/b", ".", ".hidden", ""} {
		got := Segment(in)
		if got == "." || got == ".." || got == "" {
			t.Errorf("Segment(%q) = %q, which is not a usable name", in, got)
		}
		if filepath.Base(got) != got {
			t.Errorf("Segment(%q) = %q, which spans directories", in, got)
		}
	}
}
