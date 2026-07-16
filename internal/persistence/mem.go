package persistence

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// MemStore is a pure in-memory contracts.ThreadStore — tests and ephemeral
// pods with persist=false. Spec: agora-spec-persistence.md §3.
//
// Behaves identically to LocalStore for every ThreadStore method (same
// table-driven suite in behavior_test.go drives both); it simply has no
// JSONL/SQLite underneath. Same Seq-assignment note as LocalStore applies.
type MemStore struct {
	mu      sync.Mutex
	threads map[string]*memThread
}

type memThread struct {
	meta      contracts.ThreadMeta
	items     []contracts.ThreadItem
	archived  bool
	lastSeq   int64
	updatedAt time.Time // mirrors LocalStore's updated_at, for List ordering
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{threads: make(map[string]*memThread)}
}

var _ contracts.ThreadStore = (*MemStore)(nil)

func (s *MemStore) Create(meta contracts.ThreadMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateThreadID(meta.ThreadID); err != nil {
		return err
	}
	if _, ok := s.threads[meta.ThreadID]; ok {
		return fmt.Errorf("%w: %s", ErrExists, meta.ThreadID)
	}
	lastSeq := int64(0)
	if meta.ForkOf != nil {
		lastSeq = meta.ForkOf.Seq
	}
	s.threads[meta.ThreadID] = &memThread{meta: meta, lastSeq: lastSeq, updatedAt: meta.CreatedAt}
	return nil
}

func (s *MemStore) Meta(threadID string) (contracts.ThreadMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[threadID]
	if !ok {
		return contracts.ThreadMeta{}, fmt.Errorf("%w: %s", ErrNotFound, threadID)
	}
	return t.meta, nil
}

func (s *MemStore) resolveChain(threadID string) ([]contracts.ThreadItem, error) {
	t, ok := s.threads[threadID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, threadID)
	}
	if t.meta.ForkOf == nil {
		out := make([]contracts.ThreadItem, len(t.items))
		copy(out, t.items)
		return out, nil
	}
	parentItems, err := s.resolveChain(t.meta.ForkOf.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("persistence: resolve fork parent %s: %w", t.meta.ForkOf.ThreadID, err)
	}
	forkSeq := t.meta.ForkOf.Seq
	out := make([]contracts.ThreadItem, 0, len(parentItems)+len(t.items))
	for _, it := range parentItems {
		if it.Seq <= forkSeq {
			out = append(out, it)
		}
	}
	out = append(out, t.items...)
	return out, nil
}

func (s *MemStore) Resume(threadID string) (contracts.ItemIterator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.resolveChain(threadID)
	if err != nil {
		return nil, err
	}
	return newSliceIterator(items), nil
}

func (s *MemStore) Append(threadID string, items []contracts.ThreadItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(items) == 0 {
		return nil
	}
	t, ok := s.threads[threadID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, threadID)
	}
	for _, it := range items {
		t.lastSeq++
		it.Seq = t.lastSeq
		if it.TS.IsZero() {
			it.TS = time.Now().UTC()
		}
		if it.TS.After(t.updatedAt) {
			t.updatedAt = it.TS
		}
		if it.Type == contracts.TIWDChanged {
			if wd, pr, ok := decodeWDChanged(it.Payload); ok {
				t.meta.WorkingDir = wd
				t.meta.ProjectRoot = pr
			}
		}
		t.items = append(t.items, it)
	}
	return nil
}

func (s *MemStore) List(f contracts.ListFilter) ([]contracts.ThreadMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []contracts.ThreadMeta
	for _, t := range s.threads {
		if f.WorkingDir != "" && t.meta.WorkingDir != f.WorkingDir {
			continue
		}
		if f.ProjectRoot != "" && t.meta.ProjectRoot != f.ProjectRoot {
			continue
		}
		if f.IdentityFP != "" && t.meta.IdentityFP != f.IdentityFP {
			continue
		}
		if f.Archived != nil && t.archived != *f.Archived {
			continue
		}
		if f.Text != "" && !containsText(t.items, f.Text) {
			continue
		}
		out = append(out, t.meta)
	}
	// Match LocalStore's ORDER BY updated_at DESC, id ASC so the two stores
	// return identical order for the shared behavioral suite.
	sort.Slice(out, func(i, j int) bool {
		ui, uj := s.threads[out[i].ThreadID].updatedAt, s.threads[out[j].ThreadID].updatedAt
		if !ui.Equal(uj) {
			return ui.After(uj)
		}
		return out[i].ThreadID < out[j].ThreadID
	})
	return out, nil
}

func containsText(items []contracts.ThreadItem, text string) bool {
	for _, it := range items {
		if it.Type != contracts.TIUserMessage && it.Type != contracts.TIAgentMessage {
			continue
		}
		if s := extractText(it.Payload); s != "" && stringsContains(s, text) {
			return true
		}
	}
	return false
}

func stringsContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return len(substr) == 0
}

func (s *MemStore) Fork(threadID string, seq int64) (contracts.ThreadMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, ok := s.threads[threadID]
	if !ok {
		return contracts.ThreadMeta{}, fmt.Errorf("%w: %s", ErrNotFound, threadID)
	}
	if seq < 0 || seq > parent.lastSeq {
		return contracts.ThreadMeta{}, fmt.Errorf("%w: %d (parent last_seq=%d)", ErrInvalidForkSeq, seq, parent.lastSeq)
	}
	childID, err := newThreadID()
	if err != nil {
		return contracts.ThreadMeta{}, err
	}
	childMeta := contracts.ThreadMeta{
		ThreadID:    childID,
		CreatedAt:   time.Now().UTC(),
		IdentityFP:  parent.meta.IdentityFP,
		Profile:     parent.meta.Profile,
		WorkingDir:  parent.meta.WorkingDir,
		ProjectRoot: parent.meta.ProjectRoot,
		ForkOf:      &contracts.ForkRef{ThreadID: threadID, Seq: seq},
	}
	s.threads[childID] = &memThread{meta: childMeta, lastSeq: seq, updatedAt: childMeta.CreatedAt}
	return childMeta, nil
}

func (s *MemStore) Archive(threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[threadID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, threadID)
	}
	t.archived = true
	return nil
}

func (s *MemStore) Delete(threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.threads[threadID]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, threadID)
	}
	delete(s.threads, threadID)
	return nil
}
