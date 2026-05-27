package ui

import (
	"strings"
	"testing"
	"time"
)

func TestAppendToActiveBlock_BuildsUpBody(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m.appendBlock(chatBlock{class: blockAspectThinking, speaker: "shadow", createdAt: time.Now()})
	m.activeBlockIdx = len(m.blocks) - 1
	m.appendToActiveBlock("hello ")
	m.appendToActiveBlock("world")
	got := m.blocks[m.activeBlockIdx].body.String()
	if got != "hello world" {
		t.Fatalf("active block body: want %q got %q", "hello world", got)
	}
}

func TestFinishActiveBlock_DemotesThinkingToAspect(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m.appendBlock(chatBlock{class: blockAspectThinking, speaker: "shadow", createdAt: time.Now()})
	m.activeBlockIdx = 0
	m.finishActiveBlock()
	if m.blocks[0].class != blockAspect {
		t.Fatalf("class after finish: want blockAspect got %d", m.blocks[0].class)
	}
	if m.activeBlockIdx != -1 {
		t.Fatalf("activeBlockIdx not cleared: got %d", m.activeBlockIdx)
	}
}

func TestMarkActiveBlockFailed_SetsFlagAndDemotesClass(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m.appendBlock(chatBlock{class: blockAspectThinking, speaker: "shadow", createdAt: time.Now()})
	m.activeBlockIdx = 0
	m.markActiveBlockFailed("send timeout")
	if !m.blocks[0].failed {
		t.Fatalf("failed flag not set")
	}
	if m.blocks[0].failedMsg != "send timeout" {
		t.Fatalf("failedMsg: want %q got %q", "send timeout", m.blocks[0].failedMsg)
	}
	if m.blocks[0].class != blockAspect {
		t.Fatalf("class after fail: want blockAspect got %d", m.blocks[0].class)
	}
}

func TestAppendBlock_EvictsOverHistoryDepth(t *testing.T) {
	m := NewModel(Config{HistoryDepth: 3, AspectID: "shadow"})
	for i := 0; i < 5; i++ {
		b := chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()}
		b.body.WriteString("msg")
		m.appendBlock(b)
	}
	if len(m.blocks) != 3 {
		t.Fatalf("blocks len after evict: want 3 got %d", len(m.blocks))
	}
}

func TestActiveBlockIdx_AdjustsOnEviction(t *testing.T) {
	// HistoryDepth=3; fill the buffer, point active at the tail, then
	// add one more block. Eviction should shift activeBlockIdx down by 1.
	m := NewModel(Config{HistoryDepth: 3, AspectID: "shadow"})
	for i := 0; i < 3; i++ {
		m.appendBlock(chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()})
	}
	m.activeBlockIdx = 2 // tail block
	m.appendBlock(chatBlock{class: blockAspectThinking, speaker: "shadow", createdAt: time.Now()})
	// After eviction of index 0: blocks=[old1, old2, new]. Old active(2) → 1.
	if m.activeBlockIdx != 1 {
		t.Fatalf("activeBlockIdx after eviction: want 1, got %d", m.activeBlockIdx)
	}
	if len(m.blocks) != 3 {
		t.Fatalf("blocks len after eviction: want 3, got %d", len(m.blocks))
	}
}

func TestActiveBlockIdx_ClearsWhenEvictedPastZero(t *testing.T) {
	m := NewModel(Config{HistoryDepth: 2, AspectID: "shadow"})
	m.appendBlock(chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()})
	m.activeBlockIdx = 0 // active is the only block
	m.appendBlock(chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()})
	m.appendBlock(chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()})
	// Two evictions; active was at index 0; should now be -1.
	if m.activeBlockIdx != -1 {
		t.Fatalf("activeBlockIdx after evicting past zero: want -1, got %d", m.activeBlockIdx)
	}
}

func TestReentry_DividerDropsOnNextKeystroke(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	// Simulate idle threshold crossed
	m.lastInteractionAt = time.Now().Add(-10 * time.Minute)
	m.awaitingReentry = true
	m.idleSince = m.lastInteractionAt
	// Block lands during idle
	m.appendBlock(chatBlock{class: blockNotify, speaker: "shadow", createdAt: time.Now()})
	if m.blocksDuringIdle != 1 {
		t.Fatalf("blocksDuringIdle after notify: want 1 got %d", m.blocksDuringIdle)
	}
	// Operator keystroke
	m.markInteraction()
	// Find a divider block in m.blocks
	foundDivider := false
	for _, b := range m.blocks {
		if b.class == blockDivider {
			foundDivider = true
			body := b.body.String()
			if !strings.Contains(body, "since you left") {
				t.Fatalf("divider body: want 'since you left' got %q", body)
			}
			break
		}
	}
	if !foundDivider {
		t.Fatalf("divider not appended after keystroke")
	}
	if m.awaitingReentry {
		t.Fatalf("awaitingReentry should be cleared after divider drop")
	}
}

func TestReentry_NoDividerWhenIdleWasSilent(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m.lastInteractionAt = time.Now().Add(-10 * time.Minute)
	m.awaitingReentry = true
	m.idleSince = m.lastInteractionAt
	// No blocks during idle
	m.markInteraction()
	for _, b := range m.blocks {
		if b.class == blockDivider {
			t.Fatalf("divider dropped despite silent idle: %v", b)
		}
	}
	if m.awaitingReentry {
		t.Fatalf("awaitingReentry should still clear after keystroke")
	}
}

func TestFormatIdleDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{6 * time.Minute, "6m"},
		{2*time.Hour + 14*time.Minute, "2h 14m"},
		{45 * time.Second, "0m"},
	}
	for _, c := range cases {
		if got := formatIdleDuration(c.d); got != c.want {
			t.Fatalf("formatIdleDuration(%v): want %q got %q", c.d, c.want, got)
		}
	}
}

// Regression for the 2026-05-28 panic ("strings.Builder must not be
// copied after first use") that crashed agora mid-streaming. The
// trigger pattern: m.blocks held chatBlock by value, append-grow
// copied each chatBlock (including its strings.Builder), then the
// next appendToActiveBlock write tripped Builder.copyCheck.
//
// Fix: m.blocks is now []*chatBlock; appendBlock clones the Builder
// contents into a fresh pointer. This test forces a slice-cap grow
// while the active block is mid-write and confirms no panic.
func TestAppendBlock_NoPanicWhenAppendGrowsWithActiveStreamingBlock(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic during stream-while-grow: %v", r)
		}
	}()
	m := NewModel(Config{AspectID: "shadow"})
	// Empty HistoryDepth so we don't evict — let the slice grow naturally.
	m.appendBlock(chatBlock{class: blockAspectThinking, speaker: "shadow", createdAt: time.Now()})
	m.activeBlockIdx = len(m.blocks) - 1
	// First write binds the Builder's self-addr. After this, the Builder
	// must NEVER be copied or it'll panic on the next write.
	m.appendToActiveBlock("seed")
	// Force many subsequent appends — at least one cap-grow happens
	// well before iteration 64 (Go's growth pattern doubles capacity).
	for i := 0; i < 128; i++ {
		m.appendBlock(chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()})
		// Interleave streaming writes to the still-active block. Pre-fix,
		// the cap-grow inside appendBlock copied the active block's
		// Builder; this write would then panic.
		m.appendToActiveBlock("chunk")
	}
	body := m.blocks[m.activeBlockIdx].body.String()
	if !strings.HasPrefix(body, "seed") {
		t.Fatalf("active block body lost prefix: %q", body[:min(40, len(body))])
	}
	if !strings.Contains(body, "chunk") {
		t.Fatalf("active block body missing streamed chunks: %q", body[:min(40, len(body))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
