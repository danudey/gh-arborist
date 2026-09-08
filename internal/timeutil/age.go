// Package timeutil provides calendar-aware parsing of the age thresholds
// used by the stale-branch check.
package timeutil

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Age is a calendar-aware period such as "1y", "6mo", "90d" or "1y6mo".
//
// Years and months are applied with calendar arithmetic rather than as a fixed
// number of hours, so "1y" before 2024-02-29 is 2023-02-28 rather than some
// point in the middle of a day.
type Age struct {
	Years  int
	Months int
	Days   int
	Extra  time.Duration // remainder from h/m/s units
}

// unitPattern matches one leading "<number><unit>" pair. "mo" precedes "m" in
// the alternation because Go's regexp is leftmost-first, so "6mo" must not be
// read as 6 minutes followed by a stray "o".
var unitPattern = regexp.MustCompile(`^(\d+)(mo|y|w|d|h|m|s)`)

// ParseAge reads a threshold such as "1y", "18mo" or "1y6mo". Note that "m"
// means minutes and "mo" means months, matching Go's time.ParseDuration.
func ParseAge(s string) (Age, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Age{}, fmt.Errorf("empty age threshold")
	}

	var a Age
	rest := strings.ToLower(raw)
	for rest != "" {
		m := unitPattern.FindStringSubmatch(rest)
		if m == nil {
			return Age{}, fmt.Errorf("invalid age %q: expected a number followed by y, mo, w, d, h, m or s (for example 1y, 18mo, 90d)", raw)
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return Age{}, fmt.Errorf("invalid age %q: %w", raw, err)
		}
		switch m[2] {
		case "y":
			a.Years += n
		case "mo":
			a.Months += n
		case "w":
			a.Days += 7 * n
		case "d":
			a.Days += n
		case "h":
			a.Extra += time.Duration(n) * time.Hour
		case "m":
			a.Extra += time.Duration(n) * time.Minute
		case "s":
			a.Extra += time.Duration(n) * time.Second
		}
		rest = rest[len(m[0]):]
	}

	if a.IsZero() {
		return Age{}, fmt.Errorf("invalid age %q: must be greater than zero", raw)
	}
	return a, nil
}

// IsZero reports whether the age covers no time at all.
func (a Age) IsZero() bool {
	return a.Years == 0 && a.Months == 0 && a.Days == 0 && a.Extra == 0
}

// CutoffFrom returns the instant that is exactly this age before t. Anything
// older than the result has exceeded the threshold.
func (a Age) CutoffFrom(t time.Time) time.Time {
	return t.AddDate(-a.Years, -a.Months, -a.Days).Add(-a.Extra)
}

// String renders the age back into its canonical input form.
func (a Age) String() string {
	var b strings.Builder
	write := func(n int, unit string) {
		if n != 0 {
			fmt.Fprintf(&b, "%d%s", n, unit)
		}
	}
	write(a.Years, "y")
	write(a.Months, "mo")
	write(a.Days, "d")
	if a.Extra != 0 {
		b.WriteString(a.Extra.String())
	}
	if b.Len() == 0 {
		return "0"
	}
	return b.String()
}
