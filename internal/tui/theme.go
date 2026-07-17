package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Theme bundles the styles the cell/diff/modal renderers use. Passed
// explicitly (never a package-global renderer) so tests can pin a
// deterministic, colorless theme and get byte-stable output regardless of
// which terminal/CI OS runs them (§ conventions: "no wall-clock in snapshot
// paths ... byte-stable").
type Theme struct {
	renderer *lipgloss.Renderer

	Muted    lipgloss.Style
	Bold     lipgloss.Style
	DiffAdd  lipgloss.Style
	DiffDel  lipgloss.Style
	DiffLine lipgloss.Style
	Header   lipgloss.Style
	Danger   lipgloss.Style
	Selected lipgloss.Style
}

// PlainTheme is a colorless theme (lipgloss.NewRenderer with a nil output
// forces the "no color" ANSI-16/off profile) — used by every golden
// snapshot test so goldens don't depend on terminal color-profile
// detection, which varies across CI OSes/TTY states.
func PlainTheme() Theme {
	r := lipgloss.NewRenderer(nil)
	r.SetColorProfile(termenv.Ascii) // fg-only, matches §7's ANSI-16 fallback
	return newTheme(r)
}

// DefaultTheme uses lipgloss's ambient terminal renderer (auto-detected
// color profile) — what the real interactive TUI uses.
func DefaultTheme() Theme {
	return newTheme(lipgloss.DefaultRenderer())
}

func newTheme(r *lipgloss.Renderer) Theme {
	return Theme{
		renderer: r,
		Muted:    r.NewStyle().Faint(true),
		Bold:     r.NewStyle().Bold(true),
		// §7: muted add/del background tints, dark-theme values as the
		// baseline (light/ANSI-16 fallback is future polish — noted in the
		// build report).
		DiffAdd:  r.NewStyle().Background(lipgloss.Color("#213A2B")).Foreground(lipgloss.Color("#7FBF8F")),
		DiffDel:  r.NewStyle().Background(lipgloss.Color("#4A221D")).Foreground(lipgloss.Color("#D98E82")),
		DiffLine: r.NewStyle().Faint(true),
		Header:   r.NewStyle().Bold(true).Underline(true),
		Danger:   r.NewStyle().Bold(true).Foreground(lipgloss.Color("#D9534F")),
		Selected: r.NewStyle().Reverse(true),
	}
}
