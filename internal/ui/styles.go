// Package-shared lipgloss.Style declarations. Pulled out of chat.go
// so rendering logic and visual choices can evolve independently.
package ui

import "github.com/charmbracelet/lipgloss"

var (
	dimStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	chatInStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC")).Bold(true)
	chatOutStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
	ttyStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24")).Bold(true)
	notifyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#F472B6")).Bold(true)
	notifyBodyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F472B6"))
	systemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	modelStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399")).Bold(true)
	headerStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1E90FF"))
	dividerStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)
