// Block lifecycle, chat-region layout helpers, and status-line render.
// All methods are pointer-receivers on Model. Block-class rendering
// lives in chat.go.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/opclient"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// pendingSend is one unacked chat.send awaiting its broker echo.
// Reconciliation is FIFO over this queue with content equality as a
// guard; entries leave the queue on echo, RPC failure, or ack timeout.
type pendingSend struct {
	seq   int64
	text  string
	block *chatBlock
}

// turnState is the latest observe snapshot for one TurnID.
type turnState struct {
	label       string
	status      string
	started     time.Time
	lastFrameAt time.Time
}

// lightsPresence reports whether this turn counts toward the
// "<agent> is working…" presence line: only in-flight main turns do —
// compact/filter-judge run after the reply and must not re-light it.
func (t *turnState) lightsPresence() bool {
	return t.status == "in_flight" && (t.label == "" || t.label == "main")
}

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
		delivered: b.delivered,
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
	m.vp.SetContent(renderBlockContent(m.blocks, m.vp.Width, m.showTimestamps, m.mdr))
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
	if status == "online" {
		rightParts = append(rightParts, "online")
		// Presence is observe-driven (in-flight main turns), never
		// runs.* — dispatch Jobs are not DM turns.
		if m.presenceActive() {
			rightParts = append(rightParts, fmt.Sprintf("%s is working… %s", m.cfg.Agent, formatAgo(m.presenceElapsed())))
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

// applyOpEvent folds one opclient event into the model. The returned
// tea.Cmd (usually nil) starts the presence tick chain when an observe
// snapshot activates presence.
func (m *Model) applyOpEvent(ev opclient.Event) tea.Cmd {
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
		// The agent's reply ends the wait even if the turn's complete
		// snapshot is delayed or lost.
		if m.belongs(ev.Message) && ev.Message.From == m.cfg.Agent {
			m.clearActivePresence()
		}
	case opclient.RunEvent:
		// runs.* is dispatch Jobs, never DM turns; tracked but it does
		// not feed the rendered presence state.
		if ev.Run.Aspect == "" || ev.Run.Aspect == m.cfg.Agent {
			m.working = runStatusWorking(ev.Run.Status)
		}
	case opclient.ObserveTurn:
		return m.applyObserveTurn(ev)
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
	return nil
}

// applyObserveTurn replaces the tracked snapshot for the turn's TurnID.
// Complete/errored turns are dropped immediately; in-flight ones stamp
// lastFrameAt for the staleness guard.
func (m *Model) applyObserveTurn(ev opclient.ObserveTurn) tea.Cmd {
	if ev.Aspect != "" && ev.Aspect != m.cfg.Agent {
		return nil
	}
	if ev.Turn.TurnID == "" {
		return nil
	}
	if ev.Turn.Status == "complete" || ev.Turn.Status == "errored" {
		delete(m.turns, ev.Turn.TurnID)
		return nil
	}
	m.turns[ev.Turn.TurnID] = &turnState{
		label:       ev.Turn.Label,
		status:      ev.Turn.Status,
		started:     ev.Turn.Started,
		lastFrameAt: m.now(),
	}
	return m.ensurePresenceTick()
}

func (m Model) presenceActive() bool {
	for _, t := range m.turns {
		if t.lightsPresence() {
			return true
		}
	}
	return false
}

// presenceElapsed is time since the earliest in-flight main turn started.
func (m Model) presenceElapsed() time.Duration {
	var started time.Time
	for _, t := range m.turns {
		if !t.lightsPresence() {
			continue
		}
		if started.IsZero() || t.started.Before(started) {
			started = t.started
		}
	}
	if started.IsZero() {
		return 0
	}
	if d := m.now().Sub(started); d > 0 {
		return d
	}
	return 0
}

func (m *Model) clearActivePresence() {
	for id, t := range m.turns {
		if t.lightsPresence() {
			delete(m.turns, id)
		}
	}
}

// pruneStaleTurns drops turns with no fresh snapshot for
// presenceStaleAfter — the guard against a lost complete frame pinning
// "is working…" forever.
func (m *Model) pruneStaleTurns() {
	now := m.now()
	for id, t := range m.turns {
		if now.Sub(t.lastFrameAt) > presenceStaleAfter {
			delete(m.turns, id)
		}
	}
}

// ensurePresenceTick starts the 1s tick chain when presence just became
// active; at most one chain runs (presenceTicking).
func (m *Model) ensurePresenceTick() tea.Cmd {
	if m.presenceTicking || !m.presenceActive() {
		return nil
	}
	m.presenceTicking = true
	return presenceTickCmd()
}

func presenceTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return presenceTick{} })
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
	if msg.ID > 0 {
		if _, ok := m.seenIDs[msg.ID]; ok {
			return false
		}
	}
	body := displayChatContent(msg, m.cfg.Agent)
	if m.reconcilePending(msg, body) {
		return true
	}
	if msg.ID > 0 {
		m.seenIDs[msg.ID] = struct{}{}
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

// appendOptimistic locally echoes a just-sent message as a pending
// block ("…"), queues it for echo reconciliation, and returns the
// ack-timeout command the caller schedules alongside the send.
func (m *Model) appendOptimistic(text string) tea.Cmd {
	m.sendSeq++
	seq := m.sendSeq
	b := chatBlock{
		class:     blockYou,
		speaker:   m.cfg.OperatorName,
		createdAt: m.now(),
		msgID:     -seq,
		pending:   true,
	}
	b.body.WriteString(text)
	m.appendBlock(b)
	m.pendingSends = append(m.pendingSends, &pendingSend{
		seq:   seq,
		text:  text,
		block: m.blocks[len(m.blocks)-1],
	})
	return tea.Tick(echoAckTimeout, func(time.Time) tea.Msg { return sendEchoTimeout{seq: seq} })
}

// reconcilePending matches a broker echo of the operator's own message
// against the unacked-send queue — FIFO, with content equality as a
// guard. On match the pending block flips to delivered ("✓") in place
// and the server id is adopted into seenIDs so the echo never appends
// a duplicate block.
func (m *Model) reconcilePending(msg opclient.ChatMessage, body string) bool {
	if msg.From != m.cfg.OperatorName && msg.From != "operator" {
		return false
	}
	for i, ps := range m.pendingSends {
		if strings.TrimSpace(ps.text) != strings.TrimSpace(body) {
			continue
		}
		ps.block.pending = false
		ps.block.delivered = true
		ps.block.failed = false
		ps.block.failedMsg = ""
		ps.block.msgID = msg.ID
		if msg.ID > 0 {
			m.seenIDs[msg.ID] = struct{}{}
		}
		m.pendingSends = append(m.pendingSends[:i], m.pendingSends[i+1:]...)
		return true
	}
	return false
}

// markPendingFailed handles the SendFailed (RPC error) path: the
// pending block itself flips to ✗ undelivered rather than only printing
// a system error. Texts with no queued send fall back to a system block.
func (m *Model) markPendingFailed(text string, err error) {
	if err == nil {
		return
	}
	for i, ps := range m.pendingSends {
		if strings.TrimSpace(ps.text) != strings.TrimSpace(text) {
			continue
		}
		ps.block.pending = false
		ps.block.failed = true
		ps.block.failedMsg = err.Error()
		m.pendingSends = append(m.pendingSends[:i], m.pendingSends[i+1:]...)
		return
	}
	m.appendSystem("send failed: " + err.Error())
}

// expirePendingSend marks one queued send undelivered after its ack
// window lapses. No-op (false) if the echo already reconciled it.
func (m *Model) expirePendingSend(seq int64) bool {
	for i, ps := range m.pendingSends {
		if ps.seq != seq {
			continue
		}
		ps.block.pending = false
		ps.block.failed = true
		ps.block.failedMsg = "undelivered"
		m.pendingSends = append(m.pendingSends[:i], m.pendingSends[i+1:]...)
		return true
	}
	return false
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
