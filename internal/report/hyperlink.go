package report

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Hyperlinks says whether to emit OSC 8 terminal hyperlinks.
type Hyperlinks string

const (
	// HyperlinksAuto emits hyperlinks only when the terminal is known to
	// support them.
	HyperlinksAuto Hyperlinks = "auto"
	// HyperlinksAlways emits them regardless.
	HyperlinksAlways Hyperlinks = "always"
	// HyperlinksNever never emits them.
	HyperlinksNever Hyperlinks = "never"
)

// ParseHyperlinks resolves a hyperlink mode name.
func ParseHyperlinks(s string) (Hyperlinks, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return HyperlinksAuto, nil
	case "always", "yes", "true":
		return HyperlinksAlways, nil
	case "never", "no", "false":
		return HyperlinksNever, nil
	}
	return "", fmt.Errorf("unknown hyperlink mode %q; choose from auto, always, never", s)
}

// linker turns URLs into OSC 8 terminal hyperlinks.
//
// OSC 8 lets a terminal attach a URL to a run of text, so the branch name in
// the report becomes clickable without the URL taking up any width. Terminals
// that do not understand the sequence should ignore it, but enough of them
// print it as garbage that this is only enabled for terminals known to support
// it.
type linker struct{ enabled bool }

func newLinker(mode Hyperlinks, isTTY bool, env func(string) string) linker {
	if env == nil {
		env = os.Getenv
	}
	switch mode {
	case HyperlinksNever:
		return linker{}
	case HyperlinksAlways:
		return linker{enabled: true}
	}
	// Piped output must stay plain: escape sequences would corrupt the values.
	return linker{enabled: isTTY && supportsHyperlinks(env)}
}

// supportsHyperlinks guesses from the environment whether OSC 8 is safe to
// emit. There is no way to ask a terminal, so this is a list of terminals known
// to handle it.
func supportsHyperlinks(env func(string) string) bool {
	// The convention other tools use for forcing this on.
	if v := env("FORCE_HYPERLINK"); v != "" && v != "0" {
		return true
	}

	term := env("TERM")
	if term == "" || term == "dumb" {
		return false
	}
	// Multiplexers only forward OSC 8 in recent versions, and a mangled
	// sequence is worse than a plain name, so stay out of them.
	if env("TMUX") != "" || strings.HasPrefix(term, "screen") || strings.HasPrefix(term, "tmux") {
		return false
	}

	for _, key := range []string{
		"KITTY_WINDOW_ID",       // kitty
		"WT_SESSION",            // Windows Terminal
		"KONSOLE_VERSION",       // Konsole
		"DOMTERM",               // DomTerm
		"WEZTERM_PANE",          // WezTerm
		"ALACRITTY_WINDOW_ID",   // Alacritty 0.11 and later
		"GHOSTTY_RESOURCES_DIR", // Ghostty
	} {
		if env(key) != "" {
			return true
		}
	}
	if term == "xterm-kitty" || term == "xterm-ghostty" {
		return true
	}

	// Apple Terminal is deliberately absent: it sets TERM_PROGRAM but prints
	// the escape sequence as text.
	switch env("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "vscode", "Hyper", "ghostty", "rio", "Tabby", "tabby":
		return true
	}

	// GNOME Terminal and the other VTE terminals, from VTE 0.50 onwards.
	if n, err := strconv.Atoi(env("VTE_VERSION")); err == nil && n >= 5000 {
		return true
	}
	return false
}

// osc8 starts a hyperlink sequence; st terminates each half of it.
var (
	osc8 = esc + "]8;;"
	st   = esc + `\`
)

// wrap makes text a hyperlink to url.
func (l linker) wrap(url, text string) string {
	if !l.enabled || url == "" || text == "" {
		return text
	}
	return osc8 + url + st + text + osc8 + st
}

// field links a table field. The table printer pads fields to the column width
// before handing them here, so the padding is kept outside the link; otherwise
// the clickable area would run to the next column.
func (l linker) field(url string, inner func(string) string) func(string) string {
	return func(padded string) string {
		visible := strings.TrimRight(padded, " ")
		padding := padded[len(visible):]
		if inner != nil {
			visible = inner(visible)
		}
		return l.wrap(url, visible) + padding
	}
}
