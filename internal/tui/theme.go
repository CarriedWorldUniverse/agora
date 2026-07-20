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
	// Accent/Success/Warning are the semantic accent colors: Accent for
	// interactive affordances (composer prompt, user-message prefix, agent
	// id), Success for approve/exit-0 signals, Warning for in-flight
	// (running) status. Their COLOR strips to nothing under PlainTheme —
	// but styling changes that alter plain TEXT (glyphs like ✓/❯) do reach
	// goldens and updated two of them in this pass.
	Accent  lipgloss.Style
	Success lipgloss.Style
	Warning lipgloss.Style
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
		// Header drops the underline for bold+accent — underline read as
		// 1995 on modal titles and diff paths; the accent family below is
		// tuned to sit next to the diff tints (#7FBF8F green is shared).
		Header:   r.NewStyle().Bold(true).Foreground(lipgloss.Color("#89B4FA")),
		Danger:   r.NewStyle().Bold(true).Foreground(lipgloss.Color("#D9534F")),
		Selected: r.NewStyle().Reverse(true),
		Accent:   r.NewStyle().Foreground(lipgloss.Color("#89B4FA")),
		Success:  r.NewStyle().Foreground(lipgloss.Color("#7FBF8F")),
		Warning:  r.NewStyle().Foreground(lipgloss.Color("#D8B36A")),
	}
}
