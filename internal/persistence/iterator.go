package persistence

import "github.com/CarriedWorldUniverse/agora/contracts"

// sliceIterator implements contracts.ItemIterator over an already-resolved,
// in-order slice. Resume's contract is "full replay order by Seq" — how the
// slice was resolved (chain-through-fork, tail-first, etc.) is an
// implementation concern (contracts/thread.go doc comment on Resume).
// Spec: agora-spec-persistence.md §3.
type sliceIterator struct {
	items []contracts.ThreadItem
	i     int
}

func newSliceIterator(items []contracts.ThreadItem) *sliceIterator {
	return &sliceIterator{items: items}
}

func (s *sliceIterator) Next() (contracts.ThreadItem, bool) {
	if s.i >= len(s.items) {
		return contracts.ThreadItem{}, false
	}
	it := s.items[s.i]
	s.i++
	return it, true
}

func (s *sliceIterator) Err() error { return nil }

func (s *sliceIterator) Close() error { return nil }
