// Keystroke handling, input-history recall, and textarea resize.
// Pulled out of model.go's giant Update switch. Each handler returns
// the (mutated) Model + any tea.Cmd; the Update dispatcher in
// model.go routes tea.KeyMsg to handleKey.
package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) updateSlashHint() {
	v := m.input.Value()
	if !strings.HasPrefix(v, "/") {
		m.slashHint = ""
		return
	}
	prefix := strings.TrimPrefix(v, "/")
	if i := strings.Index(prefix, " "); i >= 0 {
		prefix = prefix[:i]
	}
	matches := []string{}
	for _, name := range commandNames() {
		if strings.HasPrefix(name, prefix) {
			matches = append(matches, "/"+name)
		}
	}
	if len(matches) == 0 {
		m.slashHint = ""
		return
	}
	m.slashHint = "commands: " + strings.Join(matches, " ")
}

func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if !m.textareaEnabled {
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
	m.markInteraction()
	if msg.Type == tea.KeyTab && strings.HasPrefix(m.input.Value(), "/") {
		prefix := strings.TrimPrefix(m.input.Value(), "/")
		if i := strings.Index(prefix, " "); i >= 0 {
			prefix = prefix[:i]
		}
		matches := []string{}
		for _, name := range commandNames() {
			if strings.HasPrefix(name, prefix) {
				matches = append(matches, name)
			}
		}
		if len(matches) == 1 {
			m.input.SetValue("/" + matches[0])
			m.input.CursorEnd()
			m.updateSlashHint()
		}
		return m, nil
	}
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
		m.blocks[len(m.blocks)-1].body.WriteString("ctrl-c — exiting... (press again to force exit)")
		m.refreshChatContent(false)
		return m, func() tea.Msg { return QuitGraceful{} }
	case "y":
		if m.input.Value() != "" {
			break
		}
		return m.yankLastMessage()
	case "enter":
		text := strings.TrimRight(m.input.Value(), " \t\n")
		if text == "" {
			return m, nil
		}
		m.input.SetValue("")
		m.input.SetHeight(1)
		m.lastSubmitted = text
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
		m.appendOptimistic(text)
		m.refreshChatContent(false)
		if m.cfg.Client != nil {
			content := "@" + m.cfg.Agent + " " + text
			topic := m.threadTopic()
			return m, func() tea.Msg {
				if err := m.cfg.Client.ChatSend(context.Background(), content, topic, 0); err != nil {
					return SendFailed{Text: text, Err: err}
				}
				return nil
			}
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
	case "ctrl+g":
		m.showTimestamps = !m.showTimestamps
		m.refreshChatContent(false)
		return m, nil
	case "ctrl+k":
		if m.input.Value() == "" && m.vpReady {
			m.vp.LineUp(1)
			if m.vp.AtBottom() {
				m.unreadBelow = 0
			}
		}
		return m, nil
	case "ctrl+j":
		if m.input.Value() == "" && m.vpReady {
			m.vp.LineDown(1)
			if m.vp.AtBottom() {
				m.unreadBelow = 0
			}
		}
		return m, nil
	case "alt+up":
		if m.vpReady {
			m.vp.LineUp(1)
			if m.vp.AtBottom() {
				m.unreadBelow = 0
			}
		}
		return m, nil
	case "alt+down":
		if m.vpReady {
			m.vp.LineDown(1)
			if m.vp.AtBottom() {
				m.unreadBelow = 0
			}
		}
		return m, nil
	}
	switch msg.String() {
	case "up":
		if m.input.Line() > 0 {
			break
		}
		cur := m.input.Value()
		matches := m.historyPrefixMatch(cur)
		if len(matches) > 0 {
			m.historyBackPrefix(cur, matches)
			return m, nil
		}
		if cur == "" {
			var vpCmd tea.Cmd
			m.vp, vpCmd = m.vp.Update(msg)
			return m, vpCmd
		}
	case "down":
		if m.input.LineCount() > 1 && m.input.Line() < m.input.LineCount()-1 {
			break
		}
		if m.historyIdx != -1 {
			m.historyForwardPrefix(m.draftSnapshot)
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
	m.updateSlashHint()
	return m, cmd
}

func (m Model) yankLastMessage() (Model, tea.Cmd) {
	text, ok := m.lastYankableMessage()
	if !ok {
		m.statusNotice = "copy: no message"
		return m, clearStatusNoticeCmd()
	}
	if m.writeClipboard == nil {
		m.writeClipboard = func(string) error { return nil }
	}
	if err := m.writeClipboard(text); err != nil {
		m.statusNotice = "copy failed: " + err.Error()
		return m, clearStatusNoticeCmd()
	}
	m.statusNotice = "copied message"
	return m, clearStatusNoticeCmd()
}

func clearStatusNoticeCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return ClearStatusNotice{} })
}

func (m Model) lastYankableMessage() (string, bool) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := m.blocks[i]
		if b == nil {
			continue
		}
		switch b.class {
		case blockYou, blockAspect, blockNotify:
			text := b.body.String()
			if strings.TrimSpace(text) == "" {
				continue
			}
			return text, true
		}
	}
	return "", false
}

// historyPrefixMatch returns history entries (newest first) whose
// first line starts with the given prefix. Empty prefix returns all
// entries newest first.
func (m *Model) historyPrefixMatch(prefix string) []string {
	var out []string
	for i := len(m.inputHistory) - 1; i >= 0; i-- {
		entry := m.inputHistory[i]
		firstLine := entry
		if nl := strings.Index(entry, "\n"); nl >= 0 {
			firstLine = entry[:nl]
		}
		if prefix == "" || strings.HasPrefix(firstLine, prefix) {
			out = append(out, entry)
		}
	}
	return out
}

func (m *Model) historyBackPrefix(prefix string, matches []string) {
	// matches is newest-first; advance through them
	if m.historyIdx == -1 {
		m.draftSnapshot = prefix
		m.historyIdx = 0
	} else if m.historyIdx+1 < len(matches) {
		m.historyIdx++
	} else {
		return // at oldest
	}
	if m.historyIdx < len(matches) {
		m.input.SetValue(matches[m.historyIdx])
		m.input.CursorEnd()
	}
}

func (m *Model) historyForwardPrefix(draft string) {
	if m.historyIdx == -1 {
		return
	}
	if m.historyIdx > 0 {
		m.historyIdx--
		matches := m.historyPrefixMatch(draft)
		if m.historyIdx < len(matches) {
			m.input.SetValue(matches[m.historyIdx])
			m.input.CursorEnd()
		}
		return
	}
	// past newest match: restore draft
	m.historyIdx = -1
	m.input.SetValue(m.draftSnapshot)
	m.draftSnapshot = ""
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
