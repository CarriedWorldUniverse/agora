package persistence

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// LocalStore is the JSONL-source-of-truth + SQLite-mirror ThreadStore.
// Spec: agora-spec-persistence.md §1–§3.
//
// Root is injected via NewLocalStore — callers (including every test in
// this package) point it at a temp dir; nothing here defaults to ~/.agora.
//
// Seq assignment: the contracts.ThreadStore.Append signature returns only
// an error (no assigned-Seq echo), so this store treats Seq assignment as
// its own responsibility — monotonically increasing per thread, continuing
// from a fork's ForkOf.Seq for a forked child — rather than trusting
// caller-supplied Seq values. Any Seq set by the caller on an item passed
// to Append is overwritten. This is the resolved reading of contracts
// §3/§1: the store is the durable order-of-record, so it, not the caller,
// assigns order.
type LocalStore struct {
	root string
	cfg  Config

	mu sync.Mutex
	db *sql.DB
}

// NewLocalStore opens (creating if absent) a LocalStore rooted at root:
// <root>/threads/... for JSONL, <root>/state.db for the SQLite mirror.
func NewLocalStore(root string, cfg Config) (*LocalStore, error) {
	if err := os.MkdirAll(filepath.Join(root, "threads"), 0o755); err != nil {
		return nil, fmt.Errorf("persistence: create threads root: %w", err)
	}
	db, err := openMirror(filepath.Join(root, "state.db"))
	if err != nil {
		return nil, err
	}
	return &LocalStore{root: root, cfg: cfg, db: db}, nil
}

// Close releases the SQLite mirror connection. Not part of contracts.ThreadStore
// (that interface has no lifecycle method) — callers that construct a
// LocalStore directly may call it for clean shutdown; the crash-safety test
// in this package deliberately does NOT call it, to prove data survives an
// unclean stop.
func (s *LocalStore) Close() error {
	return s.db.Close()
}

var _ contracts.ThreadStore = (*LocalStore)(nil)

// Create implements contracts.ThreadStore.
// Spec §1, §3.
func (s *LocalStore) Create(meta contracts.ThreadMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateThreadID(meta.ThreadID); err != nil {
		return err
	}
	if _, err := getThread(s.db, meta.ThreadID); err == nil {
		return fmt.Errorf("%w: %s", ErrExists, meta.ThreadID)
	}

	dir := monthDir(s.root, meta.CreatedAt)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("persistence: create month dir: %w", err)
	}
	path := threadPath(s.root, meta.CreatedAt, meta.ThreadID)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s (file exists)", ErrExists, meta.ThreadID)
	}
	if err := createThreadFile(path, meta); err != nil {
		return err
	}
	if err := insertThread(s.db, meta); err != nil {
		return err
	}
	return nil
}

// Meta implements contracts.ThreadStore.
func (s *LocalStore) Meta(threadID string) (contracts.ThreadMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := getThread(s.db, threadID)
	if err != nil {
		return contracts.ThreadMeta{}, err
	}
	return r.toMeta(), nil
}

// resolveChain returns the full, in-Seq-order item slice for threadID,
// chaining through fork ancestors up to their ForkOf.Seq. Spec §1: "Fork
// ... reads chain through the parent up to the fork point"; post-fork
// parent items are never visible to the child (they simply aren't <= the
// recorded fork Seq).
func (s *LocalStore) resolveChain(threadID string) ([]contracts.ThreadItem, error) {
	r, err := getThread(s.db, threadID)
	if err != nil {
		return nil, err
	}
	path := threadPath(s.root, r.CreatedAt, threadID)
	_, ownItems, err := readThreadFile(path)
	if err != nil {
		return nil, err
	}

	if r.ForkOfThread == "" {
		return ownItems, nil
	}
	parentItems, err := s.resolveChain(r.ForkOfThread)
	if err != nil {
		return nil, fmt.Errorf("persistence: resolve fork parent %s: %w", r.ForkOfThread, err)
	}
	forkSeq := r.ForkOfSeq.Int64
	out := make([]contracts.ThreadItem, 0, len(parentItems)+len(ownItems))
	for _, it := range parentItems {
		if it.Seq <= forkSeq {
			out = append(out, it)
		}
	}
	out = append(out, ownItems...)
	return out, nil
}

// Resume implements contracts.ThreadStore.
func (s *LocalStore) Resume(threadID string) (contracts.ItemIterator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.resolveChain(threadID)
	if err != nil {
		return nil, err
	}
	return newSliceIterator(items), nil
}

// Append implements contracts.ThreadStore. See the Seq-assignment note on
// LocalStore.
func (s *LocalStore) Append(threadID string, items []contracts.ThreadItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(items) == 0 {
		return nil
	}
	r, err := getThread(s.db, threadID)
	if err != nil {
		return err
	}
	path := threadPath(s.root, r.CreatedAt, threadID)

	toWrite := make([]contracts.ThreadItem, len(items))
	copy(toWrite, items)
	next := r.LastSeq
	updatedAt := r.UpdatedAt
	var wdPtr, rootPtr *string
	for i := range toWrite {
		next++
		toWrite[i].Seq = next
		if toWrite[i].TS.IsZero() {
			toWrite[i].TS = time.Now().UTC()
		}
		if toWrite[i].TS.After(updatedAt) {
			updatedAt = toWrite[i].TS
		}
		if toWrite[i].Type == contracts.TIWDChanged {
			if wd, pr, ok := decodeWDChanged(toWrite[i].Payload); ok {
				wdPtr = &wd
				rootPtr = &pr
			}
		}
	}

	if err := appendItems(path, toWrite, s.cfg.Fsync); err != nil {
		return err
	}
	if err := updateThreadAfterAppend(s.db, threadID, next, updatedAt, wdPtr, rootPtr); err != nil {
		return err
	}
	if err := indexFTS(s.db, threadID, toWrite); err != nil {
		return err
	}
	return nil
}

// List implements contracts.ThreadStore.
func (s *LocalStore) List(f contracts.ListFilter) ([]contracts.ThreadMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listThreads(s.db, f)
}

// Fork implements contracts.ThreadStore. No copying: the new thread's file
// holds only its own post-fork items; Resume chains through the parent.
// Spec §1, §3.
func (s *LocalStore) Fork(threadID string, seq int64) (contracts.ThreadMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	parent, err := getThread(s.db, threadID)
	if err != nil {
		return contracts.ThreadMeta{}, err
	}
	if seq < 0 || seq > parent.LastSeq {
		return contracts.ThreadMeta{}, fmt.Errorf("%w: %d (parent last_seq=%d)", ErrInvalidForkSeq, seq, parent.LastSeq)
	}

	childID, err := newThreadID()
	if err != nil {
		return contracts.ThreadMeta{}, err
	}
	childMeta := contracts.ThreadMeta{
		ThreadID:    childID,
		CreatedAt:   time.Now().UTC(),
		IdentityFP:  parent.IdentityFP,
		Profile:     parent.Profile,
		WorkingDir:  parent.WorkingDir,
		ProjectRoot: parent.ProjectRoot,
		ForkOf:      &contracts.ForkRef{ThreadID: threadID, Seq: seq},
	}

	dir := monthDir(s.root, childMeta.CreatedAt)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return contracts.ThreadMeta{}, fmt.Errorf("persistence: fork: create month dir: %w", err)
	}
	path := threadPath(s.root, childMeta.CreatedAt, childID)
	if err := createThreadFile(path, childMeta); err != nil {
		return contracts.ThreadMeta{}, err
	}
	if err := insertThread(s.db, childMeta); err != nil {
		return contracts.ThreadMeta{}, err
	}
	return childMeta, nil
}

// Archive implements contracts.ThreadStore. Spec §4: "Archive = flag
// (hidden from default /resume)."
//
// archived is PRIMARY daemon state in state.db, not derived from the JSONL
// (spec §2 explicitly carves out primary state — enrollments, hook trust,
// session grants — that is "backed up with the identity dir", not
// rebuild-derivable). RebuildIndex preserves it in place; a total loss of
// state.db loses it, by design, so back up state.db as primary state. This
// replaces the earlier sidecar-marker approach, which was a fragile third
// on-disk format with a crash-ordering + missing-fsync bug (review, PR #38).
func (s *LocalStore) Archive(threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := getThread(s.db, threadID); err != nil {
		return err
	}
	return setArchived(s.db, threadID, true)
}

// Delete implements contracts.ThreadStore. Spec §4: "Delete = file removal
// + index purge, TUI-confirmed" (confirmation is a UI-layer concern, out
// of scope here).
func (s *LocalStore) Delete(threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := getThread(s.db, threadID)
	if err != nil {
		return err
	}
	path := threadPath(s.root, r.CreatedAt, threadID)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("persistence: delete thread file: %w", err)
	}
	return deleteThreadRow(s.db, threadID)
}

// RebuildIndex regenerates the derived columns of the SQLite mirror from
// the JSONL files under root — "a rebuild-index command regenerates it;
// corruption is an inconvenience, never data loss" (spec §2). It PRESERVES
// primary state (archived, agent_edges — see sqlite.go schemaDDL doc).
// Returns the ids of any thread files it had to skip because they were
// unreadable (a corrupt file must not defeat the recovery of the rest);
// nil when every file rebuilt cleanly.
func (s *LocalStore) RebuildIndex() (skipped []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return rebuildIndex(s.root, s.db)
}
