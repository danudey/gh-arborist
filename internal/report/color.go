package report

import (
	"fmt"

	"github.com/danudey/gh-arborist/internal/checks"
)

// esc is the ASCII escape character that starts an ANSI colour sequence.
var esc = string(rune(27))

// palette applies ANSI colour, or leaves text untouched when colour is off.
type palette struct{ enabled bool }

func (p palette) wrap(code, s string) string {
	if !p.enabled || s == "" {
		return s
	}
	return fmt.Sprintf("%s[%sm%s%s[0m", esc, code, s, esc)
}

func (p palette) bold(s string) string   { return p.wrap("1", s) }
func (p palette) green(s string) string  { return p.wrap("32", s) }
func (p palette) yellow(s string) string { return p.wrap("33", s) }
func (p palette) gray(s string) string   { return p.wrap("90", s) }
func (p palette) red(s string) string    { return p.wrap("31", s) }
func (p palette) cyan(s string) string   { return p.wrap("36", s) }

// forAction picks the colour for a suggested action: red for deletions because
// they remove something, yellow where a human has to judge, cyan for work that
// needs a new owner.
func (p palette) forAction(a checks.Action) func(string) string {
	switch a {
	case checks.ActionDelete:
		return p.red
	case checks.ActionReassign:
		return p.cyan
	default:
		return p.yellow
	}
}
