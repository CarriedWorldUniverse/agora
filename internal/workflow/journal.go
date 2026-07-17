package workflow

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
)

// EntryKind tags one journal.jsonl line's shape.
// Spec: agora-spec-workflows.md §4.
type EntryKind string

const (
	// EntryAgent: one ctx.agent() call — "{seq, prompt_hash(prompt+opts), result}".
	EntryAgent EntryKind = "agent"
	// EntryQuestion: one ctx.question() call.
	EntryQuestion EntryKind = "question"
	// EntryApproval: one ctx.approval() call — same shape as EntryQuestion,
	// spec §2: "same pipeline as ctx.question, one implementation, two verbs".
	EntryApproval EntryKind = "approval"
	// EntryLog: a ctx.log() narrator line.
	EntryLog EntryKind = "log"
	// EntryPhase: a ctx.phase()/phase= progress-grouping event.
	EntryPhase EntryKind = "phase"
	// EntryRunStart: the run's frozen ctx.now instant (Message holds the
	// RFC3339Nano string), recorded once per run at Branch="",LocalSeq=0 —
	// review finding: "frozen instant not persisted for resume." Kept
	// distinct from every other kind so it never collides with a script's
	// own (Branch, LocalSeq) call positions.
	EntryRunStart EntryKind = "run_start"
)

// Entry is one journal.jsonl line. Branch/LocalSeq together are the cache
// key spec §4 calls "seq": LocalSeq is a per-branch call counter (0, 1, 2,
// ...) and Branch is a structural path identifying which ctx.parallel/
// ctx.pipeline thunk this call happened inside ("" for the root main()
// body) — see doc.go for why a flat run-wide counter cannot be used once
// calls run concurrently (goroutine scheduling is not deterministic, but
// the SCRIPT'S STRUCTURE is). Seq is a monotonically increasing, run-wide,
// write-order number kept only so the persisted file has a stable total
// order to display/inspect by; replay matching never reads it.
type Entry struct {
	Seq      int64     `json:"seq"`
	Branch   string    `json:"branch"`
	LocalSeq int64     `json:"local_seq"`
	Kind     EntryKind `json:"kind"`
	// Hash is prompt_hash(prompt+opts) for EntryAgent, payload_hash for
	// EntryQuestion/EntryApproval. Log/phase entries carry no hash (they are
	// never replay-matched, only recorded).
	Hash string `json:"hash,omitempty"`

	// EntryAgent fields.
	Result    json.RawMessage `json:"result,omitempty"`
	ResultErr string          `json:"result_err,omitempty"`

	// EntryQuestion/EntryApproval fields. Answer is absent (nil) for a
	// still-parked, unanswered entry — spec §4: "answered stages replay
	// from the journal on resume", implying an unanswered one does not.
	// QuestionID is the planning-minted id (needed to look up whether an
	// answer has since arrived); Args is the QuestionArgs payload that was
	// asked, kept so a still-parked run can re-surface the exact same card
	// on recovery without re-deriving it.
	QuestionID string          `json:"question_id,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Answer     json.RawMessage `json:"answer,omitempty"`
	By         string          `json:"by,omitempty"`

	// EntryLog/EntryPhase fields.
	Message string `json:"message,omitempty"`
	Phase   string `json:"phase,omitempty"`
}

// key is the replay lookup key: (Branch, LocalSeq, Kind). Kind is part of
// the key because a script edit could in principle change what kind of
// call happens at a given branch position; requiring Kind to match too
// keeps that a clean cache miss rather than a type-confused replay.
type key struct {
	Branch   string
	LocalSeq int64
	Kind     EntryKind
}

// JournalStore is the storage-neutral seam for a run's journal — parallels
// contracts.ThreadStore/subagent.GraphStore's role for their own units.
// MemJournalStore (tests, ephemeral) and FileJournalStore (JSONL under the
// run dir, spec §4) both implement it.
type JournalStore interface {
	// Read returns runID's persisted entries in Seq order, or (nil, nil) if
	// the run has never been saved (a fresh run, not a resume).
	Read(runID string) ([]Entry, error)
	// Save replaces runID's entire persisted journal with entries (already
	// Seq-ordered by the caller). Called incrementally as a run produces
	// entries — see runState.flush — so a resume after a crash sees
	// everything recorded up to the crash.
	Save(runID string, entries []Entry) error
}

// MemJournalStore is a pure in-memory JournalStore — tests and ephemeral
// pods.
type MemJournalStore struct {
	mu   sync.Mutex
	runs map[string][]Entry
}

// NewMemJournalStore returns an empty MemJournalStore.
func NewMemJournalStore() *MemJournalStore {
	return &MemJournalStore{runs: make(map[string][]Entry)}
}

var _ JournalStore = (*MemJournalStore)(nil)

func (m *MemJournalStore) Read(runID string) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := m.runs[runID]
	if entries == nil {
		return nil, nil
	}
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out, nil
}

func (m *MemJournalStore) Save(runID string, entries []Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	saved := make([]Entry, len(entries))
	copy(saved, entries)
	m.runs[runID] = saved
	return nil
}

// FileJournalStore persists journal.jsonl under <dir>/<runID>/journal.jsonl
// (spec §4: "Run dir: ~/.agora/workflow-runs/<run_id>/ ... journal ...").
// One line per Entry, LF-terminated. Save rewrites the whole file
// (create-truncate + write) rather than append-only streaming: simpler and
// still crash-safe enough for this unit's scope (each call to Save is one
// atomic write of the current, complete entry list) — true incremental
// append is a future perf refinement, not a correctness requirement here.
type FileJournalStore struct {
	dir string

	// mu serializes the whole write+rename (review finding: "concurrent
	// FileJournalStore.Save races") — combined with the unique tmp
	// filename below, this removes the shared-".tmp"-file race (two
	// concurrent Saves opening the SAME tmp path, one's O_TRUNC clobbering
	// the other's in-flight write, then a Rename failing ENOENT, which
	// propagated as an error that got swallowed as None for an agent call
	// that actually completed). This alone does NOT protect against a
	// stale snapshot overwriting a newer one written first — that half of
	// the finding is fixed at the source instead: runState.record (engine.
	// go) now holds its own mutex across the ENTIRE snapshot+Save call, so
	// two record() calls for the same run can never race each other's
	// Save at all (Save calls for one run are strictly ordered by
	// snapshot recency). This store-level mu still matters for any OTHER
	// concurrent caller of the same runID outside a single runState (e.g.
	// two independent Run() invocations racing the same runID, or a
	// future non-workflow caller of JournalStore directly).
	mu sync.Mutex
}

// NewFileJournalStore builds a FileJournalStore rooted at dir (the parent
// of all run directories, e.g. ~/.agora/workflow-runs).
func NewFileJournalStore(dir string) *FileJournalStore {
	return &FileJournalStore{dir: dir}
}

var _ JournalStore = (*FileJournalStore)(nil)

// runIDPattern is the single-path-component allowlist a runID must match —
// review finding: "FileJournalStore runID path traversal." runID is joined
// directly into a filesystem path (journalPath); without this, a runID of
// "../../etc" or an absolute path would escape f.dir entirely (no caller
// does this today, but the spec plans a user-facing `--resume <run_id>`).
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validateRunID(runID string) error {
	if !runIDPattern.MatchString(runID) {
		return fmt.Errorf("workflow: invalid run id %q: must match %s (single path component, no slashes/dots)", runID, runIDPattern.String())
	}
	return nil
}

func (f *FileJournalStore) journalPath(runID string) string {
	return filepath.Join(f.dir, runID, "journal.jsonl")
}

// tmpSeq gives every FileJournalStore.Save call its own tmp filename (review
// finding 11's other half — see the struct doc comment above).
var tmpSeq int64

func (f *FileJournalStore) Read(runID string) ([]Entry, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	path := f.journalPath(runID)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workflow: open journal %s: %w", path, err)
	}
	defer file.Close()

	var entries []Entry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("workflow: decode journal line in %s: %w", path, err)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("workflow: scan journal %s: %w", path, err)
	}
	return entries, nil
}

func (f *FileJournalStore) Save(runID string, entries []Entry) error {
	if err := validateRunID(runID); err != nil {
		return err
	}

	// Serialize the whole write+rename per store (see the struct doc
	// comment).
	f.mu.Lock()
	defer f.mu.Unlock()

	dir := filepath.Join(f.dir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("workflow: mkdir run dir %s: %w", dir, err)
	}
	path := f.journalPath(runID)
	tmp := fmt.Sprintf("%s.tmp.%d", path, atomic.AddInt64(&tmpSeq, 1))
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("workflow: create journal tmp %s: %w", tmp, err)
	}
	w := bufio.NewWriter(file)
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			file.Close()
			os.Remove(tmp)
			return fmt.Errorf("workflow: encode journal entry: %w", err)
		}
		if _, err := w.Write(b); err != nil {
			file.Close()
			os.Remove(tmp)
			return fmt.Errorf("workflow: write journal line: %w", err)
		}
		if err := w.WriteByte('\n'); err != nil {
			file.Close()
			os.Remove(tmp)
			return fmt.Errorf("workflow: write journal newline: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		file.Close()
		os.Remove(tmp)
		return fmt.Errorf("workflow: flush journal: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("workflow: close journal: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("workflow: rename journal into place: %w", err)
	}
	return nil
}

// indexEntries builds the replay lookup map from a loaded entry list. Later
// entries for the same key win (a resumed-and-diverged-again run could in
// principle record the same key twice across attempts before this refactor;
// kept defensive) — deterministic because entries are read in file/Seq
// order, not map iteration order.
func indexEntries(entries []Entry) map[key]Entry {
	idx := make(map[key]Entry, len(entries))
	for _, e := range entries {
		idx[key{Branch: e.Branch, LocalSeq: e.LocalSeq, Kind: e.Kind}] = e
	}
	return idx
}

// sortBySeq sorts entries by Seq ascending, in place — used before Save so
// a persisted journal is always in write order regardless of concurrent
// branch completion order.
func sortBySeq(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })
}
