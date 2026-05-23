// Block lifecycle, chat-region layout helpers, and status-line render.
// All methods are pointer-receivers on Model. Block-class rendering
// lives in chat.go.
package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *Model) appendBlock(b chatBlock) {
	m.blocks = append(m.blocks, b)
	if cap := m.cfg.HistoryDepth; cap > 0 && len(m.blocks) > cap {
		evicted := len(m.blocks) - cap
		m.blocks = m.blocks[evicted:]
		if m.activeBlockIdx >= 0 {
			m.activeBlockIdx -= evicted
			if m.activeBlockIdx < 0 {
				m.activeBlockIdx = -1
			}
		}
	}
}

func (m *Model) refreshChatContent(forceBottom bool) {
	if !m.vpReady {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(renderBlockContent(m.blocks, m.vp.Width, false))
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

func (m *Model) appendToActiveBlock(text string) {
	if m.activeBlockIdx < 0 || m.activeBlockIdx >= len(m.blocks) {
		return
	}
	m.blocks[m.activeBlockIdx].body.WriteString(text)
}

func (m *Model) finishActiveBlock() {
	if m.activeBlockIdx < 0 || m.activeBlockIdx >= len(m.blocks) {
		return
	}
	if m.blocks[m.activeBlockIdx].class == blockAspectThinking {
		m.blocks[m.activeBlockIdx].class = blockAspect
	}
	m.activeBlockIdx = -1
}

func (m *Model) markActiveBlockFailed(reason string) {
	if m.activeBlockIdx < 0 || m.activeBlockIdx >= len(m.blocks) {
		return
	}
	m.blocks[m.activeBlockIdx].failed = true
	m.blocks[m.activeBlockIdx].failedMsg = reason
	if m.blocks[m.activeBlockIdx].class == blockAspectThinking {
		m.blocks[m.activeBlockIdx].class = blockAspect
	}
	m.activeBlockIdx = -1
}
