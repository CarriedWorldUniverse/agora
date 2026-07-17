package turnengine

import (
	"fmt"
	"sync/atomic"
)

// IDGen mints turn ids. Injectable so tests get deterministic ids
// (tu_0001, tu_0002, ...) without wall-clock/uuid randomness leaking into
// event-order assertions — mirrors ctxmgr's Clock injection pattern
// (internal/ctxmgr/clock.go), applied to id minting instead of time.
type IDGen interface {
	NextTurnID() string
}

// SeqIDGen is the default IDGen: turn ids are "tu_%04d" off an atomic
// counter, matching the contracts/testdata/flows/*.jsonl fixture id shape
// (tu_0001) so a fixture-comparison test can assert against real Manager
// output, not just a ScriptedEngine stub.
type SeqIDGen struct {
	n atomic.Int64
}

// NextTurnID implements IDGen.
func (g *SeqIDGen) NextTurnID() string {
	return fmt.Sprintf("tu_%04d", g.n.Add(1))
}

// FakeIDGen is a test double that replays a fixed sequence of ids, falling
// back to a final repeated value once exhausted (rather than panicking) so
// a test driving more turns than it scripted ids for still gets a
// deterministic, if reused, id instead of a crash.
type FakeIDGen struct {
	IDs []string
	pos int
}

// NextTurnID implements IDGen.
func (g *FakeIDGen) NextTurnID() string {
	if len(g.IDs) == 0 {
		return "tu_0000"
	}
	if g.pos >= len(g.IDs) {
		return g.IDs[len(g.IDs)-1]
	}
	id := g.IDs[g.pos]
	g.pos++
	return id
}
