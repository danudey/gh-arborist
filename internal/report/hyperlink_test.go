package report

import (
	"strings"
	"testing"
)

func envFrom(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func TestSupportsHyperlinks(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"ghostty", map[string]string{"TERM": "xterm-ghostty", "TERM_PROGRAM": "ghostty"}, true},
		{"kitty", map[string]string{"TERM": "xterm-kitty", "KITTY_WINDOW_ID": "1"}, true},
		{"iTerm", map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "iTerm.app"}, true},
		{"vscode", map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "vscode"}, true},
		{"windows terminal", map[string]string{"TERM": "xterm-256color", "WT_SESSION": "abc"}, true},
		{"konsole", map[string]string{"TERM": "xterm-256color", "KONSOLE_VERSION": "220401"}, true},
		{"wezterm", map[string]string{"TERM": "xterm-256color", "WEZTERM_PANE": "0"}, true},
		{"recent vte", map[string]string{"TERM": "xterm-256color", "VTE_VERSION": "6003"}, true},

		{"old vte", map[string]string{"TERM": "xterm-256color", "VTE_VERSION": "4205"}, false},
		{"plain xterm", map[string]string{"TERM": "xterm-256color"}, false},
		{"dumb", map[string]string{"TERM": "dumb"}, false},
		{"no term", map[string]string{}, false},
		// Apple Terminal sets TERM_PROGRAM but shows the escape as text.
		{"apple terminal", map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "Apple_Terminal"}, false},
		// Multiplexers may not forward the sequence.
		{"inside tmux", map[string]string{"TERM": "xterm-kitty", "TMUX": "/tmp/tmux-1000/default", "KITTY_WINDOW_ID": "1"}, false},
		{"screen", map[string]string{"TERM": "screen.xterm-256color", "TERM_PROGRAM": "iTerm.app"}, false},

		{"forced on", map[string]string{"TERM": "xterm-256color", "FORCE_HYPERLINK": "1"}, true},
		{"forced off value", map[string]string{"TERM": "xterm-256color", "FORCE_HYPERLINK": "0"}, false},
	}
	for _, tt := range tests {
		if got := supportsHyperlinks(envFrom(tt.env)); got != tt.want {
			t.Errorf("%s: supportsHyperlinks() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestNewLinkerModes(t *testing.T) {
	capable := envFrom(map[string]string{"TERM": "xterm-kitty", "KITTY_WINDOW_ID": "1"})
	plain := envFrom(map[string]string{"TERM": "xterm-256color"})

	tests := []struct {
		name  string
		mode  Hyperlinks
		isTTY bool
		env   func(string) string
		want  bool
	}{
		{"auto on a capable terminal", HyperlinksAuto, true, capable, true},
		{"auto on a plain terminal", HyperlinksAuto, true, plain, false},
		// Escape sequences would corrupt piped values, so auto must stay off.
		{"auto when piped", HyperlinksAuto, false, capable, false},
		{"always when piped", HyperlinksAlways, false, plain, true},
		{"never on a capable terminal", HyperlinksNever, true, capable, false},
	}
	for _, tt := range tests {
		if got := newLinker(tt.mode, tt.isTTY, tt.env).enabled; got != tt.want {
			t.Errorf("%s: enabled = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestParseHyperlinks(t *testing.T) {
	for in, want := range map[string]Hyperlinks{
		"": HyperlinksAuto, "auto": HyperlinksAuto,
		"always": HyperlinksAlways, "yes": HyperlinksAlways, "TRUE": HyperlinksAlways,
		"never": HyperlinksNever, "no": HyperlinksNever,
	} {
		got, err := ParseHyperlinks(in)
		if err != nil {
			t.Errorf("ParseHyperlinks(%q): %v", in, err)
		} else if got != want {
			t.Errorf("ParseHyperlinks(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ParseHyperlinks("sometimes"); err == nil {
		t.Error("an unknown mode should fail")
	}
}

func TestLinkerWrap(t *testing.T) {
	on := linker{enabled: true}
	got := on.wrap("https://example.com", "text")
	want := esc + "]8;;https://example.com" + esc + `\` + "text" + esc + "]8;;" + esc + `\`
	if got != want {
		t.Errorf("wrap() = %q, want %q", got, want)
	}

	if got := on.wrap("", "text"); got != "text" {
		t.Errorf("with no URL, wrap() = %q, want the text unchanged", got)
	}
	off := linker{}
	if got := off.wrap("https://example.com", "text"); got != "text" {
		t.Errorf("when disabled, wrap() = %q, want the text unchanged", got)
	}
}

// The table printer pads fields before handing them to the colouriser, and the
// padding must stay outside the link so the clickable area does not run into
// the next column.
func TestLinkerFieldKeepsPaddingOutsideTheLink(t *testing.T) {
	l := linker{enabled: true}
	got := l.field("https://example.com", nil)("name      ")

	if !strings.HasSuffix(got, esc+"]8;;"+esc+`\`+"      ") {
		t.Errorf("padding should follow the closing sequence, got %q", got)
	}
	if strings.Contains(got, "name      "+esc) {
		t.Error("the padding was included inside the link")
	}
}

func TestLinkerFieldComposesWithColor(t *testing.T) {
	l := linker{enabled: true}
	c := palette{enabled: true}
	got := l.field("https://example.com", c.gray)("name")

	if !strings.Contains(got, "]8;;https://example.com") {
		t.Errorf("the link is missing: %q", got)
	}
	if !strings.Contains(got, "[90m") {
		t.Errorf("the colour is missing: %q", got)
	}
}

func TestLinkerFieldWithoutURL(t *testing.T) {
	l := linker{enabled: true}
	if got := l.field("", nil)("name  "); got != "name  " {
		t.Errorf("with no URL the field should pass through unchanged, got %q", got)
	}
}
