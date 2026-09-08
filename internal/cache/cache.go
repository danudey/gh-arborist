// Package cache stores facts about a repository between runs, so that a second
// scan does not ask GitHub the same questions again.
//
// Everything kept here is either keyed by something immutable — a commit ID,
// which fixes the answer forever — or carries a short expiry. One JSON file per
// repository is loaded on open, mutated in memory and written back once, so a
// scan costs one read and one write however many lookups it makes.
package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Never is the TTL for a fact that cannot change, such as anything keyed by a
// commit ID. It is the only TTL that does not expire: any other value, negative
// included, sets an expiry time.
const Never time.Duration = 0

// format is the on-disk layout version. A file written by a different version
// is discarded rather than migrated, since everything in it can be fetched
// again.
const format = 1

// dirName is the cache subdirectory this program owns.
const dirName = "gh-arborist"

// Cache is a keyed store of JSON values with per-entry expiry. A nil *Cache is
// usable and does nothing, which is how --no-cache is implemented.
type Cache struct {
	path string

	mu      sync.Mutex
	entries map[string]entry
	dirty   bool

	// Hits and Misses count lookups, for --verbose.
	Hits, Misses int
}

type entry struct {
	Value   json.RawMessage `json:"v"`
	Expires *time.Time      `json:"e,omitempty"`
}

type file struct {
	Format  int              `json:"format"`
	Entries map[string]entry `json:"entries"`
}

// Dir returns the directory this program keeps its cache in, honouring
// XDG_CACHE_HOME through os.UserCacheDir.
func Dir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("could not locate a cache directory: %w", err)
	}
	return filepath.Join(base, dirName), nil
}

// Open loads the cache for one repository, creating it if necessary. A cache
// that cannot be read is treated as empty rather than as an error: a scan must
// not fail because of a corrupt cache file.
func Open(host, owner, name string) (*Cache, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "repos", Segment(host), Segment(owner), Segment(name)+".json")
	c := &Cache{path: path, entries: map[string]entry{}}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, nil
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil || f.Format != format {
		return c, nil
	}
	now := time.Now()
	for k, e := range f.Entries {
		if e.Expires != nil && now.After(*e.Expires) {
			// Dropping it here is what prunes the file over time.
			c.dirty = true
			continue
		}
		c.entries[k] = e
	}
	return c, nil
}

// Path returns the file this cache is stored in, for diagnostics.
func (c *Cache) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Get decodes the value stored under key into out. It reports false when the
// key is absent, expired, or holds something out cannot decode.
func (c *Cache) Get(key string, out any) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok || (e.Expires != nil && time.Now().After(*e.Expires)) {
		c.Misses++
		return false
	}
	if err := json.Unmarshal(e.Value, out); err != nil {
		delete(c.entries, key)
		c.dirty = true
		c.Misses++
		return false
	}
	c.Hits++
	return true
}

// Put stores val under key. A ttl of Never means the entry does not expire,
// which is only correct for a key that fixes its own answer, such as one built
// from commit IDs.
func (c *Cache) Put(key string, val any, ttl time.Duration) {
	if c == nil {
		return
	}
	data, err := json.Marshal(val)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	e := entry{Value: data}
	if ttl != Never {
		expires := time.Now().Add(ttl)
		e.Expires = &expires
	}
	c.entries[key] = e
	c.dirty = true
}

// Save writes the cache back, doing nothing if no entry changed. The write is
// atomic, so an interrupted run cannot leave a half-written file behind.
func (c *Cache) Save() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}

	data, err := json.Marshal(file{Format: format, Entries: c.entries})
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("could not create the cache directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(c.path)+".*")
	if err != nil {
		return fmt.Errorf("could not write the cache: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("could not write the cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not write the cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), c.path); err != nil {
		return fmt.Errorf("could not write the cache: %w", err)
	}
	c.dirty = false
	return nil
}

// Clear removes every cached repository file. Clones are left alone, since
// re-creating one is expensive and a fetch brings it up to date anyway.
func Clear() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	repos := filepath.Join(dir, "repos")
	if err := os.RemoveAll(repos); err != nil {
		return "", fmt.Errorf("could not clear the cache: %w", err)
	}
	return repos, nil
}

// Key builds a cache key from a namespace and its parts. Parts are joined with
// a character that cannot appear in a commit ID, branch name or email address
// used as a part, so two different keys cannot collide.
func Key(namespace string, parts ...string) string {
	var b strings.Builder
	b.WriteString(namespace)
	for _, p := range parts {
		b.WriteString("\x00")
		b.WriteString(p)
	}
	return b.String()
}

// Segment makes a path component out of a repository or host name, which
// arrive from the command line and so must not be able to escape the cache
// directory.
func Segment(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, s)
	if s == "" || s == "." || s == ".." || strings.HasPrefix(s, ".") {
		return "_" + s
	}
	return s
}
