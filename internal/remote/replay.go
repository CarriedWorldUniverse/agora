// GapTracker implements reconnect gap-replay (spec §9): "events carry
// per-thread seq; on reattach the client sends last_seq and the daemon
// replays from there (bounded window; beyond it → full-tail replay)."
//
// This package tracks last_seq per (device, thread) and computes exactly
// the events a reattaching device missed from a given backlog snapshot; it
// does not itself hold the backlog (that's io.Session's replay tail,
// already built) — GapTracker is the seq-aware filter U16 puts in front of
// it, keyed by AUTHENTICATED device identity rather than a client-supplied
// replay count.
package remote

import (
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

type gapKey struct {
	device string
	thread string
}

// GapTracker records the last item seq each device has been delivered per
// thread. Safe for concurrent use.
type GapTracker struct {
	mu      sync.Mutex
	lastSeq map[gapKey]int64
}

// NewGapTracker builds an empty tracker.
func NewGapTracker() *GapTracker {
	return &GapTracker{lastSeq: make(map[gapKey]int64)}
}

// LastSeq returns the last seq recorded for (deviceID, threadID); 0 means
// "never seen this thread" (contracts.ItemRef.Seq starts at a positive
// value, so 0 is a safe sentinel for "nothing yet").
func (g *GapTracker) LastSeq(deviceID, threadID string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastSeq[gapKey{deviceID, threadID}]
}

// Ack records that deviceID has now seen up through seq on threadID.
// Monotonic: a lower/stale seq never regresses what's recorded (a
// reordered ack — e.g. from a delta-droppable event, which never carries a
// meaningful seq — must never rewind the watermark).
func (g *GapTracker) Ack(deviceID, threadID string, seq int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := gapKey{deviceID, threadID}
	if seq > g.lastSeq[k] {
		g.lastSeq[k] = seq
	}
}

// Replay computes exactly the events deviceID missed on threadID, given a
// backlog snapshot ordered oldest-first (io.Session's replay tail, spec
// §0a). It returns the subset of backlog with Item.Seq strictly greater
// than the device's last recorded seq, preserving order; non-item events
// (presence, approval.resolved, ...) carry no seq and are always included
// — gap tracking only ever governs the item-level transcript, per §9's
// wording ("events carry per-thread seq").
//
// If the device has a recorded last_seq that falls BEFORE the oldest
// item-seq present in backlog, the retained window doesn't reach far
// enough back to reconstruct the exact gap: Replay returns
// ErrGapWindowExceeded, and per spec §9 ("beyond it → full-tail replay")
// the caller's fallback is simply to deliver backlog in full (which this
// same backlog snapshot already is) rather than treat the reattach as a
// refusal.
func (g *GapTracker) Replay(deviceID, threadID string, backlog []contracts.Event) ([]contracts.Event, error) {
	last := g.LastSeq(deviceID, threadID)
	if last == 0 {
		// Never seen this thread: everything in the snapshot is new to it.
		out := append([]contracts.Event(nil), backlog...)
		g.ackHighest(deviceID, threadID, backlog)
		return out, nil
	}

	oldestSeq, sawItem := int64(0), false
	for _, ev := range backlog {
		if ev.Item == nil {
			continue
		}
		if !sawItem || ev.Item.Seq < oldestSeq {
			oldestSeq = ev.Item.Seq
			sawItem = true
		}
	}
	if sawItem && oldestSeq > last+1 {
		return nil, ErrGapWindowExceeded
	}

	out := make([]contracts.Event, 0, len(backlog))
	for _, ev := range backlog {
		if ev.Item != nil && ev.Item.Seq <= last {
			continue
		}
		out = append(out, ev)
	}
	g.ackHighest(deviceID, threadID, backlog)
	return out, nil
}

// ackHighest advances the watermark to the highest item seq seen in
// events, so a Replay call itself keeps the tracker current (a caller does
// not have to separately Ack every delivered event one at a time).
func (g *GapTracker) ackHighest(deviceID, threadID string, events []contracts.Event) {
	var highest int64
	for _, ev := range events {
		if ev.Item != nil && ev.Item.Seq > highest {
			highest = ev.Item.Seq
		}
	}
	if highest > 0 {
		g.Ack(deviceID, threadID, highest)
	}
}
