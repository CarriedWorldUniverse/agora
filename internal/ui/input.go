// Keystroke handling, input-history recall, and textarea resize.
// Pulled out of model.go's giant Update switch. Each handler returns
// the (mutated) Model + any tea.Cmd; the Update dispatcher in
// model.go routes tea.KeyMsg to handleKey.
package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.quitting {
			return m, tea.Quit
		}
		m.appendBlock(chatBlock{
			class:     blockSystem,
			speaker:   "system",
			createdAt: time.Now(),
		})
		m.blocks[len(m.blocks)-1].body.WriteString("ctrl-c — deregistering... (press again to force exit)")
		m.refreshChatContent(false)
		return m, func() tea.Msg { return QuitGraceful{} }
	case "enter":
		text := strings.TrimRight(m.input.Value(), " \t\n")
		if text == "" {
			return m, nil
		}
		m.input.SetValue("")
		m.input.SetHeight(1)
		if n := len(m.inputHistory); n == 0 || m.inputHistory[n-1] != text {
			m.inputHistory = append(m.inputHistory, text)
			if limit := m.cfg.InputHistory; limit > 0 && len(m.inputHistory) > limit {
				m.inputHistory = m.inputHistory[len(m.inputHistory)-limit:]
			}
		}
		m.historyIdx = -1
		m.draftSnapshot = ""
		if cmd, handled := dispatchCommand(&m, text); handled {
			return m, cmd
		}
		m.appendBlock(chatBlock{
			class:     blockYou,
			speaker:   m.cfg.OperatorName,
			createdAt: time.Now(),
		})
		m.blocks[len(m.blocks)-1].body.WriteString(text)
		m.refreshChatContent(false)
		if m.onSubmit != nil {
			m.onSubmit(text)
		}
		return m, nil
	case "pgup", "pgdown", "ctrl+u", "ctrl+d":
		var vpCmd tea.Cmd
		m.vp, vpCmd = m.vp.Update(msg)
		if m.vp.AtBottom() {
			m.unreadBelow = 0
		}
		return m, vpCmd
	case "ctrl+e", "end":
		if m.vpReady {
			m.vp.GotoBottom()
			m.unreadBelow = 0
		}
		return m, nil
	case "ctrl+a", "home":
		if m.vpReady {
			m.vp.GotoTop()
		}
		return m, nil
	}
	switch msg.String() {
	case "up":
		if m.input.Line() > 0 {
			break
		}
		if len(m.inputHistory) > 0 {
			m.historyBack()
			return m, nil
		}
		if m.input.Value() == "" {
			var vpCmd tea.Cmd
			m.vp, vpCmd = m.vp.Update(msg)
			return m, vpCmd
		}
	case "down":
		if m.input.LineCount() > 1 && m.input.Line() < m.input.LineCount()-1 {
			break
		}
		if m.historyIdx != -1 {
			m.historyForward()
			return m, nil
		}
		if m.input.Value() == "" {
			var vpCmd tea.Cmd
			m.vp, vpCmd = m.vp.Update(msg)
			return m, vpCmd
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.resizeInputForContent()
	return m, cmd
}

func (m *Model) historyBack() {
	if len(m.inputHistory) == 0 {
		return
	}
	if m.historyIdx == -1 {
		m.draftSnapshot = m.input.Value()
		m.historyIdx = len(m.inputHistory) - 1
	} else if m.historyIdx > 0 {
		m.historyIdx--
	} else {
		return
	}
	m.input.SetValue(m.inputHistory[m.historyIdx])
	m.input.CursorEnd()
}

func (m *Model) historyForward() {
	if m.historyIdx == -1 {
		return
	}
	if m.historyIdx+1 >= len(m.inputHistory) {
		m.historyIdx = -1
		m.input.SetValue(m.draftSnapshot)
		m.draftSnapshot = ""
		m.input.CursorEnd()
		return
	}
	m.historyIdx++
	m.input.SetValue(m.inputHistory[m.historyIdx])
	m.input.CursorEnd()
}

func (m *Model) resizeInputForContent() {
	const maxInputLines = 6
	lines := m.input.LineCount()
	if lines < 1 {
		lines = 1
	}
	if lines > maxInputLines {
		lines = maxInputLines
	}
	if m.input.Height() != lines {
		m.input.SetHeight(lines)
	}
}
