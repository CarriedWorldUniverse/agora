package ui

import (
	"strings"
	"testing"
	"time"
)

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

func TestRenderChatBlock_AspectThinkingHeader(t *testing.T) {
	b := mkBlock(blockAspectThinking, "shadow", "tokens streaming", time.Now())
	out := renderChatBlock(b, 60, false)
	if !strings.Contains(out, "shadow · thinking") {
		t.Fatalf("expected 'shadow · thinking' in header: %q", out)
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
