package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/opclient"
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
	out := renderChatBlock(b, 60, false)
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
	out := renderChatBlock(b, 60, true)
	if !strings.Contains(out, "14:32") {
		t.Fatalf("timestamp missing with showTS=true: %q", out)
	}
}

func TestRenderChatBlock_NotifyHeader(t *testing.T) {
	b := mkBlock(blockNotify, "shadow", "NEX-87 needs you", time.Now())
	out := renderChatBlock(b, 60, false)
	if !strings.Contains(out, "⚡ shadow") {
		t.Fatalf("expected '⚡ shadow' in header: %q", out)
	}
}

func TestRenderChatBlock_FailedHeader(t *testing.T) {
	b := mkBlock(blockAspect, "shadow", "partial", time.Now())
	b.failed = true
	b.failedMsg = "send timeout"
	out := renderChatBlock(b, 60, false)
	if !strings.Contains(out, "shadow · failed: send timeout") {
		t.Fatalf("expected failed header: %q", out)
	}
}

func TestRenderChatBlock_DividerInline(t *testing.T) {
	b := mkBlock(blockDivider, "", "since you left (2h 14m)", time.Now())
	out := renderChatBlock(b, 60, false)
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
	out := renderBlockContent(m.blocks, 80, false)
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
	out := renderBlockContent(m.blocks, 80, false)
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
	out := renderBlockContent(m.blocks, 80, false)
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
	out := renderBlockContent(m.blocks, 80, false)
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

func TestRunEventDoesNotDriveWorkingStatus(t *testing.T) {
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
