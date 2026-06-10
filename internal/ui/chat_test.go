package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/opclient"
	tea "github.com/charmbracelet/bubbletea"
)

var errSendBoom = errors.New("rpc write failed")

func mkBlock(class blockClass, speaker, body string, ts time.Time) chatBlock {
	b := chatBlock{class: class, speaker: speaker, createdAt: ts}
	b.body.WriteString(body)
	return b
}

// mkBlockP is the pointer variant for tests that exercise coalesceBlocks
// (now operating on []*chatBlock so m.blocks' Builder fields don't get
// copied on slice reallocations — see model.go blocks field comment).
func mkBlockP(class blockClass, speaker, body string, ts time.Time) *chatBlock {
	b := mkBlock(class, speaker, body, ts)
	return &b
}

func TestRenderChatBlock_YouHeaderAndBody(t *testing.T) {
	now := time.Date(2026, 5, 23, 14, 32, 0, 0, time.UTC)
	b := mkBlock(blockYou, "you", "ship NEX-92 when keel's done", now)
	out := renderChatBlock(b, 60, false, nil)
	if !strings.Contains(out, "you") {
		t.Fatalf("header missing 'you': %q", out)
	}
	if !strings.Contains(out, "  ship NEX-92 when keel's done") {
		t.Fatalf("body not indented or missing: %q", out)
	}
	if strings.Contains(out, "14:32") {
		t.Fatalf("timestamp leaked with showTS=false: %q", out)
	}
}

func TestRenderChatBlock_TimestampToggle(t *testing.T) {
	now := time.Date(2026, 5, 23, 14, 32, 0, 0, time.UTC)
	b := mkBlock(blockYou, "you", "hi", now)
	out := renderChatBlock(b, 60, true, nil)
	if !strings.Contains(out, "14:32") {
		t.Fatalf("timestamp missing with showTS=true: %q", out)
	}
}

func TestRenderChatBlock_NotifyHeader(t *testing.T) {
	b := mkBlock(blockNotify, "shadow", "NEX-87 needs you", time.Now())
	out := renderChatBlock(b, 60, false, nil)
	if !strings.Contains(out, "⚡ shadow") {
		t.Fatalf("expected '⚡ shadow' in header: %q", out)
	}
}

func TestRenderChatBlock_FailedHeader(t *testing.T) {
	b := mkBlock(blockAspect, "shadow", "partial", time.Now())
	b.failed = true
	b.failedMsg = "send timeout"
	out := renderChatBlock(b, 60, false, nil)
	if !strings.Contains(out, "shadow · failed: send timeout") {
		t.Fatalf("expected failed header: %q", out)
	}
}

func TestRenderChatBlock_DividerInline(t *testing.T) {
	b := mkBlock(blockDivider, "", "since you left (2h 14m)", time.Now())
	out := renderChatBlock(b, 60, false, nil)
	if !strings.Contains(out, "since you left (2h 14m)") {
		t.Fatalf("divider missing label: %q", out)
	}
}

func TestCoalesceBlocks_SameSpeakerAdjacentFolds(t *testing.T) {
	now := time.Date(2026, 5, 23, 14, 0, 0, 0, time.UTC)
	blocks := []*chatBlock{
		mkBlockP(blockAspect, "shadow", "first", now),
		mkBlockP(blockAspect, "shadow", "second", now.Add(5*time.Second)),
	}
	out := coalesceBlocks(blocks)
	if len(out) != 1 {
		t.Fatalf("want 1 coalesced block, got %d", len(out))
	}
	body := out[0].body.String()
	if !strings.Contains(body, "first") || !strings.Contains(body, "second") {
		t.Fatalf("coalesced body missing parts: %q", body)
	}
	if !strings.Contains(body, "first\n\nsecond") {
		t.Fatalf("coalesced body not blank-line separated: %q", body)
	}
}

func TestCoalesceBlocks_DifferentSpeakerStaysSeparate(t *testing.T) {
	now := time.Now()
	blocks := []*chatBlock{
		mkBlockP(blockAspect, "shadow", "a", now),
		mkBlockP(blockYou, "you", "b", now),
	}
	out := coalesceBlocks(blocks)
	if len(out) != 2 {
		t.Fatalf("want 2 (different speakers), got %d", len(out))
	}
}

func TestCoalesceBlocks_OverGapStaysSeparate(t *testing.T) {
	now := time.Now()
	blocks := []*chatBlock{
		mkBlockP(blockAspect, "shadow", "old", now),
		mkBlockP(blockAspect, "shadow", "new", now.Add(2*time.Minute)),
	}
	out := coalesceBlocks(blocks)
	if len(out) != 2 {
		t.Fatalf("want 2 (over 60s gap), got %d", len(out))
	}
}

func TestCoalesceBlocks_DividerNeverFolds(t *testing.T) {
	now := time.Now()
	blocks := []*chatBlock{
		mkBlockP(blockDivider, "", "first divider", now),
		mkBlockP(blockDivider, "", "second divider", now.Add(time.Second)),
	}
	out := coalesceBlocks(blocks)
	if len(out) != 2 {
		t.Fatalf("dividers must never fold; want 2 got %d", len(out))
	}
}

func TestOpMsgEventAppendsDMThread(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})

	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
		ID:      7,
		From:    "maren",
		Content: "hi",
		Topic:   "dm:maren",
	}})

	if len(m.blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(m.blocks))
	}
	if got := m.blocks[0].body.String(); got != "hi" {
		t.Fatalf("body = %q, want hi", got)
	}
}

func TestOpMsgEventIgnoresOtherTopics(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})

	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{ID: 1, From: "maren", Content: "ticket", Topic: "NEX-1"}})
	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{ID: 2, From: "maren", Content: "empty"}})

	if len(m.blocks) != 0 {
		t.Fatalf("want no blocks, got %d", len(m.blocks))
	}
}

func TestOwnSendOptimisticReconcilesOnEcho(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.appendOptimistic("hello")

	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
		ID:      42,
		From:    "operator",
		Content: "@maren hello",
		Topic:   "dm:maren",
	}})

	if len(m.blocks) != 1 {
		t.Fatalf("want one reconciled block, got %d", len(m.blocks))
	}
	if m.blocks[0].pending {
		t.Fatalf("block still pending after echo")
	}
	if got := m.blocks[0].msgID; got != 42 {
		t.Fatalf("msgID = %d, want 42", got)
	}
}

func TestSendRendersPendingMarker(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.appendOptimistic("hello")

	if len(m.blocks) != 1 {
		t.Fatalf("want 1 pending block, got %d", len(m.blocks))
	}
	if !m.blocks[0].pending {
		t.Fatalf("optimistic block not pending")
	}
	out := renderBlockContent(m.blocks, 80, false, nil)
	if !strings.Contains(out, "…") {
		t.Fatalf("pending block missing … marker:\n%s", out)
	}
}

func TestEchoDeliverFlipsPendingToAck(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.appendOptimistic("hello")

	echo := opclient.MsgEvent{Message: opclient.ChatMessage{
		ID:      42,
		From:    "operator",
		Content: "@maren hello",
		Topic:   "dm:maren",
	}}
	m.applyOpEvent(echo)

	if len(m.blocks) != 1 {
		t.Fatalf("want one reconciled block, got %d", len(m.blocks))
	}
	if !m.blocks[0].delivered {
		t.Fatalf("block not marked delivered after echo")
	}
	out := renderBlockContent(m.blocks, 80, false, nil)
	if !strings.Contains(out, "✓") {
		t.Fatalf("delivered block missing ✓ marker:\n%s", out)
	}
	if strings.Contains(out, "…") {
		t.Fatalf("delivered block still shows … marker:\n%s", out)
	}
	if _, ok := m.seenIDs[42]; !ok {
		t.Fatalf("echo id 42 not recorded as seen")
	}
	// Replayed echo must not append a duplicate block.
	m.applyOpEvent(echo)
	if len(m.blocks) != 1 {
		t.Fatalf("duplicate echo appended a block: got %d", len(m.blocks))
	}
}

func TestEchoTimeoutMarksUndelivered(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.appendOptimistic("hello")
	seq := m.sendSeq

	updated, _ := m.Update(sendEchoTimeout{seq: seq})
	m = updated.(Model)

	if len(m.blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(m.blocks))
	}
	if m.blocks[0].pending {
		t.Fatalf("block still pending after timeout")
	}
	if !m.blocks[0].failed {
		t.Fatalf("block not failed after timeout")
	}
	out := renderBlockContent(m.blocks, 80, false, nil)
	if !strings.Contains(out, "✗ undelivered") {
		t.Fatalf("timed-out block missing ✗ undelivered marker:\n%s", out)
	}
	// A late echo after the timeout must not resurrect the queue entry
	// into a duplicate block.
	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
		ID: 9, From: "operator", Content: "@maren hello", Topic: "dm:maren",
	}})
	if len(m.blocks) != 2 {
		t.Fatalf("late echo after timeout: want 2 blocks (failed + echoed), got %d", len(m.blocks))
	}
}

func TestEchoTimeoutIgnoredOnceDelivered(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.appendOptimistic("hello")
	seq := m.sendSeq
	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
		ID: 42, From: "operator", Content: "@maren hello", Topic: "dm:maren",
	}})

	updated, _ := m.Update(sendEchoTimeout{seq: seq})
	m = updated.(Model)

	if m.blocks[0].failed {
		t.Fatalf("delivered block flipped to failed by stale timeout")
	}
}

func TestSendFailedMarksPendingBlockFailed(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.appendOptimistic("hello")

	updated, _ := m.Update(SendFailed{Text: "hello", Err: errSendBoom})
	m = updated.(Model)

	if len(m.blocks) != 1 {
		t.Fatalf("want 1 block (no extra system error), got %d", len(m.blocks))
	}
	if !m.blocks[0].failed {
		t.Fatalf("pending block not failed after SendFailed")
	}
	out := renderBlockContent(m.blocks, 80, false, nil)
	if !strings.Contains(out, "✗ undelivered") {
		t.Fatalf("failed block missing ✗ undelivered marker:\n%s", out)
	}
}

func TestStatusLineConnectionStates(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.width = 80

	if got := m.renderStatus(); !strings.Contains(got, "connecting") {
		t.Fatalf("initial status missing connecting: %q", got)
	}
	m.applyOpEvent(opclient.ConnState{Connected: true})
	if got := m.renderStatus(); !strings.Contains(got, "online") {
		t.Fatalf("online status missing: %q", got)
	}
	m.applyOpEvent(opclient.ConnState{Connected: false})
	if got := m.renderStatus(); !strings.Contains(got, "reconnecting…") {
		t.Fatalf("reconnecting status missing: %q", got)
	}
}

// runs.* is dispatch Jobs, never DM turns: a RunEvent must not light
// the "is working…" presence line (that is observe-driven only).
func TestRunEventDoesNotLightPresence(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.width = 80
	m.applyOpEvent(opclient.ConnState{Connected: true})

	m.applyOpEvent(opclient.RunEvent{Run: opclient.Run{Aspect: "maren", Status: "running"}})

	if got := m.renderStatus(); strings.Contains(got, "working") {
		t.Fatalf("runs.* must not light presence: %q", got)
	}
}

func TestObserveTurnInFlightLightsPresence(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.width = 80
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.applyOpEvent(opclient.ConnState{Connected: true})

	m.applyOpEvent(opclient.ObserveTurn{Aspect: "maren", Turn: opclient.TurnFrame{
		TurnID:  "t1",
		Status:  "in_flight",
		Started: now.Add(-12 * time.Second),
	}})

	got := m.renderStatus()
	if !strings.Contains(got, "maren is working…") {
		t.Fatalf("presence not shown for in_flight turn: %q", got)
	}
	if !strings.Contains(got, "12s") {
		t.Fatalf("presence elapsed missing: %q", got)
	}

	m.applyOpEvent(opclient.ObserveTurn{Aspect: "maren", Turn: opclient.TurnFrame{
		TurnID:  "t1",
		Status:  "complete",
		Started: now.Add(-12 * time.Second),
	}})
	got = m.renderStatus()
	if strings.Contains(got, "is working…") {
		t.Fatalf("presence not cleared by complete snapshot: %q", got)
	}
	if !strings.Contains(got, "online") {
		t.Fatalf("status not back to online: %q", got)
	}
}

func TestObserveTurnFilterJudgeDoesNotLightPresence(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.width = 80
	m.applyOpEvent(opclient.ConnState{Connected: true})

	m.applyOpEvent(opclient.ObserveTurn{Aspect: "maren", Turn: opclient.TurnFrame{
		TurnID:  "t2",
		Label:   "filter-judge",
		Status:  "in_flight",
		Started: time.Now(),
	}})

	if got := m.renderStatus(); strings.Contains(got, "is working…") {
		t.Fatalf("filter-judge turn must not light presence: %q", got)
	}
}

func TestAgentReplyClearsPresence(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.width = 80
	m.applyOpEvent(opclient.ConnState{Connected: true})
	m.applyOpEvent(opclient.ObserveTurn{Aspect: "maren", Turn: opclient.TurnFrame{
		TurnID:  "t1",
		Status:  "in_flight",
		Started: time.Now(),
	}})
	if got := m.renderStatus(); !strings.Contains(got, "is working…") {
		t.Fatalf("precondition: presence not active: %q", got)
	}

	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
		ID: 7, From: "maren", Content: "done", Topic: "dm:maren",
	}})

	if got := m.renderStatus(); strings.Contains(got, "is working…") {
		t.Fatalf("agent reply did not clear presence: %q", got)
	}
}

func TestPresenceStaleTurnExpiresOnTick(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.width = 80
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.applyOpEvent(opclient.ConnState{Connected: true})
	m.applyOpEvent(opclient.ObserveTurn{Aspect: "maren", Turn: opclient.TurnFrame{
		TurnID:  "t1",
		Status:  "in_flight",
		Started: now,
	}})

	// No frames for >5m: the tick prunes the turn and stops the chain.
	now = now.Add(5*time.Minute + time.Second)
	m.now = func() time.Time { return now }
	updated, cmd := m.Update(presenceTick{})
	m = updated.(Model)

	if got := m.renderStatus(); strings.Contains(got, "is working…") {
		t.Fatalf("stale turn still lights presence: %q", got)
	}
	if cmd != nil {
		t.Fatalf("tick chain should stop when presence clears")
	}
}

func TestBelongsRejectsNonDMTopics(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	for _, topic := range []string{"", "general", "dm:harrow"} {
		m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
			ID: 1, From: "maren", Content: "hi", Topic: topic,
		}})
	}
	if len(m.blocks) != 0 {
		t.Fatalf("non-dm topics appended blocks: got %d", len(m.blocks))
	}
}

func TestHistoryLoadedFiltersAndSortsOldestFirst(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	updated, _ := m.Update(HistoryLoaded{Messages: []opclient.ChatMessage{
		{ID: 3, From: "maren", Content: "new", Topic: "dm:maren"},
		{ID: 1, From: "maren", Content: "old", Topic: "dm:maren"},
		{ID: 2, From: "maren", Content: "ticket", Topic: "NEX-1"},
	}})
	m = updated.(Model)

	if len(m.blocks) != 2 {
		t.Fatalf("want 2 dm blocks, got %d", len(m.blocks))
	}
	if got := m.blocks[0].body.String(); got != "old" {
		t.Fatalf("first body = %q, want old", got)
	}
}

// --- Task 10: scroll hold + unread indicator + FIFO double-send ---

// newScrolledModel builds a sized model whose transcript overflows the
// viewport, scrolled to the top (not at bottom).
func newScrolledModel(t *testing.T) Model {
	t.Helper()
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = updated.(Model)
	base := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		speaker, class := "maren", blockAspect
		if i%2 == 0 {
			speaker, class = "operator", blockYou
		}
		m.appendBlock(mkBlock(class, speaker, fmt.Sprintf("line %d", i), base.Add(time.Duration(i)*time.Minute)))
	}
	m.refreshChatContent(true)
	m.vp.GotoTop()
	if m.vp.AtBottom() {
		t.Fatalf("setup: viewport still at bottom; not enough content to scroll")
	}
	if m.unreadBelow != 0 {
		t.Fatalf("setup: unreadBelow = %d, want 0", m.unreadBelow)
	}
	return m
}

// An arriving message while scrolled up must NOT jump the view to the
// bottom; the unread-below counter increments per arrival instead.
func TestArrivalHoldsScrollAndIncrementsUnread(t *testing.T) {
	m := newScrolledModel(t)

	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
		ID: 100, From: "maren", Content: "first while away", Topic: "dm:maren",
	}})

	if got := m.vp.YOffset; got != 0 {
		t.Fatalf("arrival moved the viewport: YOffset = %d, want 0", got)
	}
	if m.vp.AtBottom() {
		t.Fatalf("arrival jumped the view to the bottom")
	}
	if m.unreadBelow != 1 {
		t.Fatalf("unreadBelow = %d after first arrival, want 1", m.unreadBelow)
	}

	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
		ID: 101, From: "maren", Content: "second while away", Topic: "dm:maren",
	}})

	if m.unreadBelow != 2 {
		t.Fatalf("unreadBelow = %d after second arrival, want 2", m.unreadBelow)
	}
	if got := m.renderStatus(); !strings.Contains(got, "↓ 2 below") {
		t.Fatalf("status line missing unread indicator: %q", got)
	}
}

// `end` jumps to the bottom and clears the unread counter (so does the
// status line's advertised Ctrl-E; both route through the same case).
func TestEndJumpsToBottomAndClearsUnread(t *testing.T) {
	m := newScrolledModel(t)
	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
		ID: 100, From: "maren", Content: "while away", Topic: "dm:maren",
	}})
	if m.unreadBelow != 1 {
		t.Fatalf("precondition: unreadBelow = %d, want 1", m.unreadBelow)
	}

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnd})

	if !updated.vp.AtBottom() {
		t.Fatalf("end did not scroll to bottom")
	}
	if updated.unreadBelow != 0 {
		t.Fatalf("unreadBelow = %d after end, want 0", updated.unreadBelow)
	}
}

// Sending the same text twice quickly queues two pending blocks; echo
// reconciliation is FIFO, so the first echo flips the FIRST block only
// and the second echo flips the second.
func TestDoubleSendReconcilesFIFO(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	m.appendOptimistic("dup")
	m.appendOptimistic("dup")
	if len(m.blocks) != 2 || !m.blocks[0].pending || !m.blocks[1].pending {
		t.Fatalf("want 2 pending blocks, got %d", len(m.blocks))
	}

	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
		ID: 100, From: "operator", Content: "@maren dup", Topic: "dm:maren",
	}})

	if !m.blocks[0].delivered || m.blocks[0].msgID != 100 {
		t.Fatalf("first echo did not flip the FIRST pending block: %+v", m.blocks[0])
	}
	if m.blocks[1].delivered || !m.blocks[1].pending {
		t.Fatalf("first echo touched the second pending block")
	}

	m.applyOpEvent(opclient.MsgEvent{Message: opclient.ChatMessage{
		ID: 101, From: "operator", Content: "@maren dup", Topic: "dm:maren",
	}})

	if !m.blocks[1].delivered || m.blocks[1].msgID != 101 {
		t.Fatalf("second echo did not flip the second pending block: %+v", m.blocks[1])
	}
	if len(m.pendingSends) != 0 {
		t.Fatalf("pendingSends not drained: %d left", len(m.pendingSends))
	}
}

// --- Task 9: transcript styling + markdown ---

func TestBlockHeaderStyles_OperatorVsAgentDistinct(t *testing.T) {
	you := mkBlock(blockYou, "you", "hi", time.Now())
	agent := mkBlock(blockAspect, "maren", "hello", time.Now())
	youFg := blockHeaderStyle(you).GetForeground()
	agentFg := blockHeaderStyle(agent).GetForeground()
	if youFg == agentFg {
		t.Fatalf("operator and agent headers share a foreground: %v", youFg)
	}
}

func TestTranscriptTimestampsOnEachHeader(t *testing.T) {
	t1 := time.Date(2026, 6, 10, 9, 5, 0, 0, time.UTC)
	t2 := time.Date(2026, 6, 10, 9, 7, 0, 0, time.UTC)
	blocks := []*chatBlock{
		mkBlockP(blockYou, "you", "morning", t1),
		mkBlockP(blockAspect, "maren", "hello", t2),
	}
	out := renderBlockContent(blocks, 80, true, nil)
	if !strings.Contains(out, "09:05") {
		t.Fatalf("operator header missing HH:MM timestamp:\n%s", out)
	}
	if !strings.Contains(out, "09:07") {
		t.Fatalf("agent header missing HH:MM timestamp:\n%s", out)
	}
}

func TestTimestampsDefaultOn(t *testing.T) {
	m := NewModel(Config{Agent: "maren", OperatorName: "operator"})
	if !m.showTimestamps {
		t.Fatalf("showTimestamps should default to on")
	}
}

func TestRenderBlockContent_BlankLineBetweenDifferentSpeakers(t *testing.T) {
	now := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	blocks := []*chatBlock{
		mkBlockP(blockYou, "you", "ping", now),
		mkBlockP(blockAspect, "maren", "pong", now.Add(time.Second)),
	}
	out := renderBlockContent(blocks, 80, false, nil)
	lines := strings.Split(out, "\n")
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
		}
	}
	if blank < 1 {
		t.Fatalf("no blank separator line between different speakers:\n%s", out)
	}
}

func TestRenderBlockContent_SameSpeakerBlocksStayTight(t *testing.T) {
	now := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	// 2 minutes apart: past the coalesce gap, so they stay separate
	// blocks — but same speaker, so no blank breathing-room line.
	blocks := []*chatBlock{
		mkBlockP(blockAspect, "maren", "first", now),
		mkBlockP(blockAspect, "maren", "second", now.Add(2*time.Minute)),
	}
	out := renderBlockContent(blocks, 80, false, nil)
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == "" {
			t.Fatalf("same-speaker consecutive blocks separated by blank line:\n%s", out)
		}
	}
}

func TestAgentMarkdownRendersViaGlamour(t *testing.T) {
	md := newMarkdownRenderer(80)
	if md == nil {
		t.Fatalf("newMarkdownRenderer returned nil")
	}
	b := mkBlock(blockAspect, "maren", "this is **bold** stuff\n\n```go\nfmt.Println(\"hi\")\n```", time.Now())
	out := renderChatBlock(b, 80, false, md)
	// Glamour output characteristics, not raw-markdown survival: the
	// auto style resolves per-environment (notty in tests keeps **
	// emphasis markers literal), but fences are ALWAYS transformed —
	// the code renders as a styled/indented block, backticks gone.
	if strings.Contains(out, "```") {
		t.Fatalf("raw code fence survived glamour render:\n%s", out)
	}
	if !strings.Contains(out, "bold") {
		t.Fatalf("bold text content lost:\n%s", out)
	}
	if !strings.Contains(out, "fmt.Println") {
		t.Fatalf("code block content lost:\n%s", out)
	}
	// And it must actually differ from the plain-text path.
	plain := renderChatBlock(b, 80, false, nil)
	if out == plain {
		t.Fatalf("glamour render identical to plain render:\n%s", out)
	}
}

func TestAgentMarkdownCompact_NoLeadingTrailingBlankPadding(t *testing.T) {
	md := newMarkdownRenderer(80)
	b := mkBlock(blockAspect, "maren", "just a line", time.Now())
	out := renderChatBlock(b, 80, false, md)
	lines := strings.Split(out, "\n")
	if strings.TrimSpace(lines[len(lines)-1]) == "" {
		t.Fatalf("trailing blank padding left by glamour:\n%q", out)
	}
	// Header is line 0; body should start on line 1, not after blank padding.
	if len(lines) < 2 || strings.TrimSpace(lines[1]) == "" {
		t.Fatalf("leading blank padding between header and body:\n%q", out)
	}
}

func TestOperatorMessagesStayPlain(t *testing.T) {
	md := newMarkdownRenderer(80)
	b := mkBlock(blockYou, "you", "keep my **stars** literal", time.Now())
	out := renderChatBlock(b, 80, false, md)
	if !strings.Contains(out, "**stars**") {
		t.Fatalf("operator body was markdown-rendered, want raw text:\n%s", out)
	}
}
