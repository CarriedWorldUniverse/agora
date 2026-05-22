// Block lifecycle, chat-region layout helpers, and status-line
// render. Pulled out of model.go so the Model struct + bubbletea
// lifecycle stay focused. All methods are pointer-receivers on Model.
package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *Model) appendChat(line chatLine) {
	line.operatorRelevant = markOperatorRelevant(line.class, line.from, line.body, m.cfg.OperatorName)
	m.chat = appendChatLine(m.chat, line, m.cfg.HistoryDepth)
	m.refreshChatContent(false)
}

func (m *Model) refreshChatContent(forceBottom bool) {
	if !m.vpReady {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(renderChatContent(m.chat, m.vp.Width, m.filterChatter))
	if forceBottom || atBottom {
		m.vp.GotoBottom()
		m.unreadBelow = 0
	} else {
		m.unreadBelow++
	}
}

func (m Model) chatHeight() int {
	inputLines := 1
	if h := m.input.Height(); h > 0 {
		inputLines = h
	}
	chrome := 3 + inputLines
	if m.liveLine != "" {
		chrome++
	}
	h := m.height - chrome
	if h < 1 {
		h = 1
	}
	return h
}

func (m Model) renderStatus() string {
	left := headerStyle.Render(fmt.Sprintf("agora · %s", m.cfg.AspectID))
	wsState := "offline"
	if m.wsConnected {
		wsState = "online"
	}
	rightParts := []string{fmt.Sprintf("ws:%s · inbox:%d", wsState, m.inboxDepth)}
	if m.vpReady && !m.vp.AtBottom() && m.unreadBelow > 0 {
		rightParts = append(rightParts, fmt.Sprintf("↓ %d below (Ctrl-E)", m.unreadBelow))
	}
	if !m.filterChatter {
		rightParts = append(rightParts, "all-chat (Ctrl-T)")
	}
	right := dimStyle.Render(strings.Join(rightParts, " · "))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
