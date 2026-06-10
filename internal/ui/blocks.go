// Block lifecycle, chat-region layout helpers, and status-line render.
// All methods are pointer-receivers on Model. Block-class rendering
// lives in chat.go.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/opclient"
	"github.com/charmbracelet/lipgloss"
)

// appendBlock takes a value chatBlock but stores it as *chatBlock
// after deep-cloning the Builder contents. Caller can keep using
// literal {field: value} syntax at call sites without worrying about
// the Builder-copy hazard: this single boundary owns the conversion.
func (m *Model) appendBlock(b chatBlock) {
	cloned := &chatBlock{
		class:     b.class,
		speaker:   b.speaker,
		createdAt: b.createdAt,
		failed:    b.failed,
		failedMsg: b.failedMsg,
		msgID:     b.msgID,
		pending:   b.pending,
	}
	// Builder content survives the value→pointer transition. Empty
	// Builders skip the write (avoids unnecessarily binding the new
	// Builder's self-addr before any real use).
	if s := b.body.String(); s != "" {
		cloned.body.WriteString(s)
	}
	m.blocks = append(m.blocks, cloned)
	if m.awaitingReentry && b.class != blockDivider {
		m.blocksDuringIdle++
	}
	if cap := m.cfg.HistoryDepth; cap > 0 && len(m.blocks) > cap {
		evicted := len(m.blocks) - cap
		m.blocks = m.blocks[evicted:]
	}
}

func (m *Model) markInteraction() {
	now := time.Now()
	if m.awaitingReentry && m.blocksDuringIdle > 0 {
		dur := now.Sub(m.idleSince)
		divider := &chatBlock{
			class:     blockDivider,
			createdAt: now,
		}
		divider.body.WriteString("since you left (" + formatIdleDuration(dur) + ")")
		// Insert divider BEFORE the new content. The keystroke that
		// triggered this hasn't appended yet; the divider lands at
		// the tail of existing content.
		m.blocks = append(m.blocks, divider)
		if cap := m.cfg.HistoryDepth; cap > 0 && len(m.blocks) > cap {
			m.blocks = m.blocks[len(m.blocks)-cap:]
		}
		m.refreshChatContent(false)
	}
	if m.awaitingReentry {
		m.awaitingReentry = false
		m.blocksDuringIdle = 0
	}
	m.lastInteractionAt = now
}

// formatAgo returns a short human-readable duration string for use in
// "submitted N ago" / "wait N more" messages. Durations under one
// minute are shown as seconds ("Ns"); one minute or more as minutes ("Nm").
func formatAgo(d time.Duration) string {
	m := int(d.Minutes())
	if m < 1 {
		s := int(d.Seconds())
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm", m)
}

func formatIdleDuration(d time.Duration) string {
	h := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

func (m *Model) refreshChatContent(forceBottom bool) {
	if !m.vpReady {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(renderBlockContent(m.blocks, m.vp.Width, m.showTimestamps))
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
	// chrome = status(1) + blank(1) + bottomDivider(1) + input(N)
	chrome := 3 + inputLines
	if m.slashHint != "" {
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
	rightParts := []string{}
	status := m.connStatus
	if status == "" {
		status = "connecting"
	}
	if m.working && status == "online" {
		status = "working"
	}
	if status == "online" || status == "working" {
		rightParts = append(rightParts, "online")
		if status == "working" {
			rightParts = append(rightParts, "working")
		}
	} else {
		rightParts = append(rightParts, status)
	}
	rightParts = append(rightParts, "since "+m.sessionStart.Format("15:04"))
	tsState := "off"
	if m.showTimestamps {
		tsState = "on"
	}
	rightParts = append(rightParts, "ts:"+tsState)
	if m.vpReady && !m.vp.AtBottom() && m.unreadBelow > 0 {
		rightParts = append(rightParts, fmt.Sprintf("↓ %d below (Ctrl-E)", m.unreadBelow))
	}
	if m.statusNotice != "" {
		rightParts = append(rightParts, m.statusNotice)
	}
	right := dimStyle.Render(strings.Join(rightParts, " · "))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) threadTopic() string {
	return "dm:" + m.cfg.Agent
}

func (m Model) belongs(msg opclient.ChatMessage) bool {
	return msg.Topic == m.threadTopic()
}

func (m *Model) applyOpEvent(ev opclient.Event) {
	switch ev := ev.(type) {
	case opclient.ConnState:
		// Three states only: "connecting" (initial dial, set in
		// NewModel), "online", "reconnecting…". The client always
		// retries, so there is no terminal offline state. Transcript
		// markers fire only on online→offline→online transitions —
		// the first-ever connect stays silent.
		if ev.Connected {
			wasReconnecting := m.connStatus == "reconnecting…"
			m.connStatus = "online"
			if wasReconnecting {
				m.appendSystem("— reconnected")
				m.refreshChatContent(false)
			}
		} else if m.connStatus == "online" {
			m.connStatus = "reconnecting…"
			m.appendSystem("— connection lost; reconnecting…")
			m.refreshChatContent(false)
		}
	case opclient.MsgEvent:
		if m.appendChatMessage(ev.Message) {
			m.refreshChatContent(false)
		}
	case opclient.RunEvent:
		if ev.Run.Aspect == "" || ev.Run.Aspect == m.cfg.Agent {
			m.working = runStatusWorking(ev.Run.Status)
		}
	case opclient.EscalationEvent:
		m.escalation = newEscalationModal(EscalationRequestReceived{
			RequestID: ev.RequestID,
			Aspect:    ev.Aspect,
			Tool:      ev.Tool,
			Args:      ev.Args,
			Reason:    ev.Reason,
		})
		if m.vpReady {
			m.vp.GotoBottom()
			m.unreadBelow = 0
		}
	}
}

func runStatusWorking(status string) bool {
	switch strings.ToLower(status) {
	case "queued", "running", "working", "in_progress", "started":
		return true
	default:
		return false
	}
}

func (m *Model) appendChatMessage(msg opclient.ChatMessage) bool {
	if !m.belongs(msg) {
		return false
	}
	body := displayChatContent(msg, m.cfg.Agent)
	if m.reconcilePending(msg, body) {
		return true
	}
	class := blockAspect
	speaker := msg.From
	if speaker == "" {
		speaker = m.cfg.Agent
	}
	if msg.From == m.cfg.OperatorName || msg.From == "operator" {
		class = blockYou
		speaker = m.cfg.OperatorName
	}
	b := chatBlock{
		class:     class,
		speaker:   speaker,
		createdAt: parseChatTime(msg.ReceivedAt),
		msgID:     msg.ID,
	}
	b.body.WriteString(body)
	m.appendBlock(b)
	return true
}

func (m *Model) appendOptimistic(text string) {
	b := chatBlock{
		class:     blockYou,
		speaker:   m.cfg.OperatorName,
		createdAt: time.Now(),
		msgID:     -time.Now().UnixNano(),
		pending:   true,
	}
	b.body.WriteString(text)
	m.appendBlock(b)
}

func (m *Model) reconcilePending(msg opclient.ChatMessage, body string) bool {
	if msg.From != m.cfg.OperatorName && msg.From != "operator" {
		return false
	}
	for _, b := range m.blocks {
		if b.class != blockYou || !b.pending {
			continue
		}
		if strings.TrimSpace(b.body.String()) != strings.TrimSpace(body) {
			continue
		}
		b.pending = false
		b.failed = false
		b.failedMsg = ""
		b.msgID = msg.ID
		return true
	}
	for _, b := range m.blocks {
		if msg.ID > 0 && b.msgID == msg.ID {
			return true
		}
	}
	return false
}

func (m *Model) markPendingFailed(text string, err error) {
	if err == nil {
		return
	}
	for _, b := range m.blocks {
		if b.class == blockYou && b.pending && strings.TrimSpace(b.body.String()) == strings.TrimSpace(text) {
			b.pending = false
			b.failed = true
			b.failedMsg = err.Error()
			return
		}
	}
	m.appendSystem("send failed: " + err.Error())
}

func (m *Model) appendSystem(text string) {
	m.appendBlock(chatBlock{
		class:     blockSystem,
		speaker:   "system",
		createdAt: time.Now(),
	})
	m.blocks[len(m.blocks)-1].body.WriteString(text)
}

func displayChatContent(msg opclient.ChatMessage, agent string) string {
	content := strings.TrimSpace(msg.Content)
	prefix := "@" + agent + " "
	return strings.TrimPrefix(content, prefix)
}

func parseChatTime(v string) time.Time {
	if v == "" {
		return time.Now()
	}
	if ts, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return ts
	}
	if ts, err := time.Parse(time.RFC3339, v); err == nil {
		return ts
	}
	return time.Now()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
