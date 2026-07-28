package turnengine

import (
	"testing"

	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// TestNewManager_ComposesWarningsOnlyOnce is the integration half: it drives
// the REAL path the operator hits — NewManager seeds from DevProfile(), which
// composes the dev system prompt including the skills catalog — and asserts
// the warnings do not repeat across constructions.
//
// This needs skills to actually be discovered, so it skips when the
// environment has none rather than passing vacuously.
func TestNewManager_ComposesWarningsOnlyOnce(t *testing.T) {
	resetWarnOnce()
	var got []string
	restore := swapWarnSink(func(s string) { got = append(got, s) })
	defer restore()

	p := fake.NewProvider()
	var _ bridle.Provider = p

	// First construction: whatever warnings this environment produces.
	_ = NewManager("t1", p)
	first := len(got)
	if first == 0 {
		t.Skip("no composition warnings in this environment (no skills discovered); nothing to dedupe")
	}

	// Every subsequent construction — the session's own Manager, the
	// subagent runner's profile, and every agent() spawn — must add nothing.
	_ = NewManager("t2", p)
	_ = NewManager("t3", p)
	if len(got) != first {
		t.Errorf("warnings grew from %d to %d across Manager constructions; every agent() spawn would reprint them over a live TUI:\n%v", first, len(got), got)
	}
}
