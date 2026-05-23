package ui

import (
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
