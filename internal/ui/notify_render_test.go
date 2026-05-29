package ui

import (
	"strings"
	"testing"
)

// countSubstr returns the number of times needle appears across all
// block bodies.
func countSubstr(m Model, needle string) int {
	n := 0
	for _, b := range m.blocks {
		n += strings.Count(b.body.String(), needle)
	}
	return n
}

// aspectBlock returns the single blockAspect block (the finalised
// streamed reply), or nil if there isn't exactly one.
func aspectBlock(m Model) *chatBlock {
	var found *chatBlock
	count := 0
	for _, b := range m.blocks {
		if b.class == blockAspect {
			found = b
			count++
		}
	}
	if count != 1 {
		return nil
	}
	return found
}

// TestTurnDone_StripsNotifyFenceFromInlineBlock reproduces the
// double-render bug: the model streams its raw reply (fence included)
// into the active block, TurnDone finalises it, and a separate
// NotifyOperator renders the red block. The inline block must be
// reconciled to the fence-stripped text so the notify body shows once.
func TestTurnDone_StripsNotifyFenceFromInlineBlock(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	raw := "Here's the plan.\n\n```notify-operator\nheads up: risky\n```"
	m, _ = runUpdate(m, TurnStarted{Source: "chat", MsgID: 1})
	m, _ = runUpdate(m, TurnChunk{Text: raw})
	m, _ = runUpdate(m, TurnDone{FinalText: "Here's the plan.", HadNotify: true})
	m, _ = runUpdate(m, NotifyOperator{Body: "heads up: risky"})

	ab := aspectBlock(m)
	if ab == nil {
		t.Fatalf("want exactly one blockAspect; blocks=%d", len(m.blocks))
	}
	if got := ab.body.String(); got != "Here's the plan." {
		t.Fatalf("aspect block body: want %q got %q", "Here's the plan.", got)
	}

	notifyCount := 0
	for _, b := range m.blocks {
		if b.class == blockNotify {
			notifyCount++
			if got := b.body.String(); got != "heads up: risky" {
				t.Fatalf("notify block body: want %q got %q", "heads up: risky", got)
			}
		}
	}
	if notifyCount != 1 {
		t.Fatalf("want exactly one blockNotify, got %d", notifyCount)
	}
	if c := countSubstr(m, "heads up: risky"); c != 1 {
		t.Fatalf("notify body should appear exactly once across all blocks, got %d", c)
	}
}

// TestTurnDone_NoNotifyPathUnchanged guards the common (no-notify)
// path: the streamed reply must finalise as-is with no reconciliation.
func TestTurnDone_NoNotifyPathUnchanged(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m, _ = runUpdate(m, TurnStarted{Source: "chat", MsgID: 1})
	m, _ = runUpdate(m, TurnChunk{Text: "Just a normal reply."})
	m, _ = runUpdate(m, TurnDone{FinalText: "Just a normal reply.", HadNotify: false})

	ab := aspectBlock(m)
	if ab == nil {
		t.Fatalf("want exactly one blockAspect; blocks=%d", len(m.blocks))
	}
	if got := ab.body.String(); got != "Just a normal reply." {
		t.Fatalf("aspect block body: want %q got %q", "Just a normal reply.", got)
	}
}

// TestTurnDone_PureNotifyDropsEmptyBlock covers a reply that is ONLY a
// notify fence: the cleaned final text is empty, so the inline aspect
// block must be dropped entirely — leaving only the red notify block.
func TestTurnDone_PureNotifyDropsEmptyBlock(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	raw := "```notify-operator\nonly this\n```"
	m, _ = runUpdate(m, TurnStarted{Source: "chat", MsgID: 1})
	m, _ = runUpdate(m, TurnChunk{Text: raw})
	m, _ = runUpdate(m, TurnDone{FinalText: "", HadNotify: true})
	m, _ = runUpdate(m, NotifyOperator{Body: "only this"})

	for _, b := range m.blocks {
		if b.class == blockAspect || b.class == blockAspectThinking {
			t.Fatalf("pure-notify left a stray aspect block: %q", b.body.String())
		}
	}
	if c := countSubstr(m, "only this"); c != 1 {
		t.Fatalf("notify body should appear exactly once, got %d", c)
	}
}
