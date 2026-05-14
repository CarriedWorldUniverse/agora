// Package inbox is agora's FIFO work queue.
//
// Per the spec §14, every item that flows toward the engine has a
// Source tag: "chat" for items arriving via the nexus bus (chat.deliver
// frames), "tty" for items typed by the operator into the TUI. The
// engine sees both through a single channel, and routing back uses the
// Source tag to decide which channel the reply lands on (spec §8).
//
// FIFO order is strict — a chat message that arrived before a tty
// keystroke is processed first. The engine reads one item per Deliberate.
package inbox

import (
	"sync"
	"time"
)

// Source tags the origin channel of an Item. Spec §14.
type Source string

const (
	// SourceChat is set on items derived from a chat.deliver frame.
	// Replies route to nexus chat (spec §8.1).
	SourceChat Source = "chat"
	// SourceTTY is set on items typed by the operator into the TUI.
	// Replies render in the chat panel only (spec §8.2).
	SourceTTY Source = "tty"
)

// Item is the funnel-side representation of one piece of incoming work.
// The fields mirror what nexus's wsasp.DeliveredMessage carries, plus
// the source tag and a local arrival timestamp.
type Item struct {
	Source     Source
	From       string
	Content    string
	MsgID      int64  // 0 for tty items
	ReplyTo    int64  // 0 for tty items
	ThreadRoot int64  // 0 for tty items / un-threaded chat
	Reason     string // mention|reply|thread|all for chat; empty for tty
	ReceivedAt time.Time
}

// Inbox is the FIFO queue. Producers (the WS client, the TUI input
// handler) call Push. The engine consumer pulls via Take or watches
// the Updates channel.
//
// The mutex protects the slice; Updates is the bubbletea-side signal
// path. Sending on Updates is non-blocking — if the UI is mid-render
// and not yet reading the channel, we drop the signal because the
// next Take will still find the item; the UI re-checks on its own
// tick anyway.
type Inbox struct {
	mu      sync.Mutex
	items   []Item
	updates chan struct{}
}

// New builds an empty Inbox. The updates channel is buffered to 1
// so a single pending signal is enough — multiple producers all
// signalling "wake up" coalesces to one wake-up.
func New() *Inbox {
	return &Inbox{updates: make(chan struct{}, 1)}
}

// Push appends an item to the tail of the queue and signals listeners.
func (q *Inbox) Push(it Item) {
	q.mu.Lock()
	q.items = append(q.items, it)
	q.mu.Unlock()

	select {
	case q.updates <- struct{}{}:
	default:
	}
}

// Take pulls the head item off the queue. Returns (Item{}, false)
// when empty. Callers block on Updates() between empties — Take
// itself never blocks.
func (q *Inbox) Take() (Item, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return Item{}, false
	}
	head := q.items[0]
	q.items = q.items[1:]
	return head, true
}

// Len reports the current queue depth. Used by the TUI status line
// (spec §9.1) and by tests.
func (q *Inbox) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Updates is a wake-up channel: receive returns whenever an item has
// been Pushed since the last receive. Coalesced via a buffered chan
// of cap 1 — multiple Pushes between receives yield one signal.
func (q *Inbox) Updates() <-chan struct{} {
	return q.updates
}
