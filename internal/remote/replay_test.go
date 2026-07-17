package remote

import (
	"errors"
	"reflect"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func evItem(seq int64) contracts.Event {
	return contracts.Event{Type: contracts.EvItemCompleted, ThreadID: "th1", Item: &contracts.ItemRef{Seq: seq, Type: contracts.ItemAgentMessage}}
}

func evPresence() contracts.Event {
	return contracts.Event{Type: contracts.EvClientAttached, ThreadID: "th1"}
}

// TestGapReplayDeterministic: on reattach the device replays exactly the
// events it missed (spec §9).
func TestGapReplayDeterministic(t *testing.T) {
	g := NewGapTracker()
	backlog := []contracts.Event{evItem(1), evItem(2), evItem(3), evItem(4), evItem(5)}

	// First attach: nothing seen yet, gets everything, and the watermark
	// advances to the highest seq delivered.
	got, err := g.Replay("dev1", "th1", backlog)
	if err != nil {
		t.Fatalf("first Replay: %v", err)
	}
	if !reflect.DeepEqual(got, backlog) {
		t.Fatalf("first Replay: got %v want full backlog", got)
	}
	if g.LastSeq("dev1", "th1") != 5 {
		t.Fatalf("watermark after first Replay: got %d want 5", g.LastSeq("dev1", "th1"))
	}

	// Device disconnects after seeing up to seq 3 (simulate partial
	// delivery by resetting the watermark explicitly — a live session
	// would Ack incrementally as it delivers, not all at once).
	g2 := NewGapTracker()
	g2.Ack("dev1", "th1", 3)
	newBacklog := []contracts.Event{evItem(1), evItem(2), evItem(3), evItem(4), evItem(5), evItem(6)}
	got, err = g2.Replay("dev1", "th1", newBacklog)
	if err != nil {
		t.Fatalf("gap Replay: %v", err)
	}
	want := []contracts.Event{evItem(4), evItem(5), evItem(6)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gap Replay: got %v want %v", got, want)
	}
	if g2.LastSeq("dev1", "th1") != 6 {
		t.Fatalf("watermark after gap Replay: got %d want 6", g2.LastSeq("dev1", "th1"))
	}
}

// TestGapReplayNoGapReturnsNothing: a device that is fully caught up
// (last_seq == highest backlog seq) gets an empty replay, not the whole
// backlog again.
func TestGapReplayNoGapReturnsNothing(t *testing.T) {
	g := NewGapTracker()
	g.Ack("dev1", "th1", 5)
	backlog := []contracts.Event{evItem(1), evItem(2), evItem(3), evItem(4), evItem(5)}
	got, err := g.Replay("dev1", "th1", backlog)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Replay for a fully caught-up device: got %d events, want 0", len(got))
	}
}

// TestGapReplayNonItemEventsAlwaysIncluded: presence/approval-resolved
// events carry no seq and are never filtered by the gap window.
func TestGapReplayNonItemEventsAlwaysIncluded(t *testing.T) {
	g := NewGapTracker()
	g.Ack("dev1", "th1", 5)
	backlog := []contracts.Event{evItem(3), evPresence(), evItem(6)}
	got, err := g.Replay("dev1", "th1", backlog)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	want := []contracts.Event{evPresence(), evItem(6)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Replay: got %v want %v", got, want)
	}
}

// TestGapReplayWindowExceededFallsBackToFullTail: a device whose last_seq
// predates the retained backlog window gets ErrGapWindowExceeded — the
// caller's documented fallback is full-tail replay (the backlog it
// already has), per spec §9.
func TestGapReplayWindowExceededFallsBackToFullTail(t *testing.T) {
	g := NewGapTracker()
	g.Ack("dev1", "th1", 1) // device last saw seq 1, long ago
	// The retained backlog window has since evicted everything before
	// seq 10 — there's a real gap this snapshot cannot reconstruct.
	backlog := []contracts.Event{evItem(10), evItem(11)}
	_, err := g.Replay("dev1", "th1", backlog)
	if !errors.Is(err, ErrGapWindowExceeded) {
		t.Fatalf("Replay: got %v want ErrGapWindowExceeded", err)
	}
}

// TestGapReplayMultiDeviceMultiThreadIsolated: watermarks are tracked
// per (device, thread), never leaking across either axis.
func TestGapReplayMultiDeviceMultiThreadIsolated(t *testing.T) {
	g := NewGapTracker()
	g.Ack("dev1", "th1", 5)
	if got := g.LastSeq("dev2", "th1"); got != 0 {
		t.Fatalf("dev2/th1 watermark: got %d want 0 (isolated from dev1)", got)
	}
	if got := g.LastSeq("dev1", "th2"); got != 0 {
		t.Fatalf("dev1/th2 watermark: got %d want 0 (isolated from th1)", got)
	}
}

// TestGapReplayAckIsMonotonic: a stale/lower Ack never regresses the
// watermark.
func TestGapReplayAckIsMonotonic(t *testing.T) {
	g := NewGapTracker()
	g.Ack("dev1", "th1", 10)
	g.Ack("dev1", "th1", 3) // stale, out-of-order ack
	if got := g.LastSeq("dev1", "th1"); got != 10 {
		t.Fatalf("watermark after stale Ack: got %d want 10 (monotonic)", got)
	}
}
