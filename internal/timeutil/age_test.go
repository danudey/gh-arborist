package timeutil

import (
	"testing"
	"time"
)

func TestParseAge(t *testing.T) {
	tests := []struct {
		in   string
		want Age
	}{
		{"1y", Age{Years: 1}},
		{"18mo", Age{Months: 18}},
		{"90d", Age{Days: 90}},
		{"12w", Age{Days: 84}},
		{"1y6mo", Age{Years: 1, Months: 6}},
		{"1y6mo15d", Age{Years: 1, Months: 6, Days: 15}},
		{"48h", Age{Extra: 48 * time.Hour}},
		{"30m", Age{Extra: 30 * time.Minute}},
		{"1MO", Age{Months: 1}},
		{"  1y  ", Age{Years: 1}},
	}
	for _, tt := range tests {
		got, err := ParseAge(tt.in)
		if err != nil {
			t.Errorf("ParseAge(%q) returned error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseAge(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParseAgeMonthsNotMinutes(t *testing.T) {
	// "mo" must win over "m", or "6mo" would parse as six minutes.
	got, err := ParseAge("6mo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Months != 6 || got.Extra != 0 {
		t.Errorf("ParseAge(\"6mo\") = %+v, want 6 months", got)
	}
}

func TestParseAgeErrors(t *testing.T) {
	for _, in := range []string{"", "  ", "1", "y", "6months", "-1y", "1y!", "0d", "1.5y", "abc"} {
		if got, err := ParseAge(in); err == nil {
			t.Errorf("ParseAge(%q) = %+v, want an error", in, got)
		}
	}
}

func TestCutoffFromUsesCalendarMonths(t *testing.T) {
	now := time.Date(2026, time.March, 31, 12, 0, 0, 0, time.UTC)

	// One year back from a leap-adjacent date must land on the same calendar
	// day, not 365 fixed days earlier.
	oneYear, _ := ParseAge("1y")
	if got, want := oneYear.CutoffFrom(now), time.Date(2025, time.March, 31, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("1y before %v = %v, want %v", now, got, want)
	}

	threeMonths, _ := ParseAge("3mo")
	if got, want := threeMonths.CutoffFrom(now), time.Date(2025, time.December, 31, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("3mo before %v = %v, want %v", now, got, want)
	}
}

func TestAgeString(t *testing.T) {
	for _, in := range []string{"1y", "18mo", "90d", "1y6mo"} {
		age, err := ParseAge(in)
		if err != nil {
			t.Fatalf("ParseAge(%q): %v", in, err)
		}
		if got := age.String(); got != in {
			t.Errorf("ParseAge(%q).String() = %q, want %q", in, got, in)
		}
	}
}
