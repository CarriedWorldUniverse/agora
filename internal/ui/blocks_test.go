package ui

import (
	"strings"
	"testing"
	"time"
)

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

func TestAppendBlock_NoPanicWhenAppendGrowsWithBuilderBlocks(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic during append-grow: %v", r)
		}
	}()
	m := NewModel(Config{AspectID: "shadow"})
	for i := 0; i < 128; i++ {
		b := chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()}
		b.body.WriteString("msg")
		m.appendBlock(b)
	}
	for _, b := range m.blocks {
		if b.body.String() != "msg" {
			t.Fatalf("body changed during append-grow: %q", b.body.String())
		}
	}
}
