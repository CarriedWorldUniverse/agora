package ctxmgr

import (
	"sort"
)

// Tier is where a key's live copy currently sits.
// Spec: agora-spec-context-curation.md §3 ("hot -> warm -> tracked -> thread").
type Tier int

const (
	// TierResident: the live copy is IN context (hot if within HotSteps of
	// LastTouchStep, warm/LRU-eligible otherwise — hot/warm is a query over
	// LastTouchStep, not a separate stored state, so it can never drift out
	// of sync with "step").
	TierResident Tier = iota
	// TierTracked: demoted — metadata only, instantly re-addable (§3b).
	TierTracked
)

// Entry is one ledger row: the live copy of a key, wherever it currently
// lives (resident content, or tracked metadata after demotion).
// Spec §2 (live copy/hash/last-touch/staleness), §3b (tracked metadata).
type Entry struct {
	Key Key
	// Seq is the thread seq of the message currently carrying the key's
	// content (the live copy) — every OTHER thread item naming this key is
	// superseded/stubbed at assembly time (§2, §4).
	Seq int64
	// ContentHash of the live copy's bytes (§2 — shared with the edit-guard
	// staleness check and the identical-rewrite no-op test).
	ContentHash string
	// SizeBytes is the (possibly per-item-cap-truncated, §3a) resident size.
	// Meaningless once Tier == TierTracked (tracked entries are ~100 bytes
	// of metadata regardless, §3b) but kept for the freed-bytes accounting
	// at the moment of demotion.
	SizeBytes int
	// Truncated marks that SizeBytes reflects a head-truncation at
	// MaxRetainBytes, not the artifact's real size (§3a per-item cap).
	Truncated bool
	// LastTouchStep: most recent step that read, wrote, edited, or named
	// the key (§2 — drives LRU and hot-set immunity).
	LastTouchStep int
	// Tier: resident or tracked.
	Tier Tier
	// Stale: a mutation without full content invalidated the live copy
	// (§2). A stale entry is stubbed regardless of Tier — correctness
	// rule, not a tuning knob.
	Stale bool
	// DiskBacked: whether this key has on-disk ground truth (file
	// read/write/edit classes) vs none (web fetch, MCP result,
	// unrepeatable command output, §3b re-admission source 3).
	DiskBacked bool
	// Span, if non-nil, is the resident window (partial re-admission,
	// §3b) — nil means the whole artifact is resident/tracked.
	Span *Span
}

// residentBytes is what this entry costs against the budget right now.
func (e *Entry) residentBytes() int {
	if e.Tier != TierResident {
		return 0
	}
	return e.SizeBytes
}

// Ledger is the working-set ledger: one Entry per Key, in-memory, and —
// per §8 — rebuildable purely by thread replay; nothing here is persisted
// across process restarts. A Manager keeps one Ledger per thread.
type Ledger struct {
	cfg     Config
	entries map[string]*Entry
}

// NewLedger builds an empty ledger for cfg.
func NewLedger(cfg Config) *Ledger {
	return &Ledger{cfg: cfg, entries: make(map[string]*Entry)}
}

// Get returns the entry for k, if any.
func (l *Ledger) Get(k Key) (*Entry, bool) {
	e, ok := l.entries[k.String()]
	return e, ok
}

// Entries returns every entry, sorted deterministically by Key string —
// ground rule 3 (no map-iteration order in any decision output).
func (l *Ledger) Entries() []*Entry {
	out := make([]*Entry, 0, len(l.entries))
	for _, e := range l.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out
}

// recordLive records a fresh full-content copy (a read result, or a write
// call's args) as the key's live copy: resident, fresh, un-stale. This is
// what makes assistant write args "working-set entries like any read — one
// live copy per key" (§2).
func (l *Ledger) recordLive(step int, k Key, seq int64, hash string, size int, diskBacked bool) *Entry {
	truncated := false
	if size > l.cfg.MaxRetainBytes {
		size = l.cfg.MaxRetainBytes
		truncated = true
	}
	e := &Entry{
		Key:           k,
		Seq:           seq,
		ContentHash:   hash,
		SizeBytes:     size,
		Truncated:     truncated,
		LastTouchStep: step,
		Tier:          TierResident,
		Stale:         false,
		DiskBacked:    diskBacked,
	}
	l.entries[k.String()] = e
	return e
}

// RecordRead records a full-content read (ClassRead result) as the key's
// live copy. Spec §2.
func (l *Ledger) RecordRead(step int, k Key, seq int64, hash string, size int, diskBacked bool) *Entry {
	return l.recordLive(step, k, seq, hash, size, diskBacked)
}

// RecordWrite records a full-content write (ClassWrite call args) as the
// key's live copy — "a write's ARGS are the newest truth of the file"
// (§2). Same mechanics as RecordRead; kept as a distinct name for callers
// (and tests) that want to assert on write-specific behavior (§4
// supersession rewrites only the content arg, id/tool_use preserved —
// that rendering detail lives in the assembler, not the ledger).
func (l *Ledger) RecordWrite(step int, k Key, seq int64, hash string, size int, diskBacked bool) *Entry {
	return l.recordLive(step, k, seq, hash, size, diskBacked)
}

// RecordMutation records a content-less mutation (ClassEdit, or any
// run_command the fs-watcher attributes to the path): it invalidates the
// live copy without replacing it — the key becomes Stale (§2). LastTouchStep
// still advances (a mutation is a touch — the file is under active edit,
// hot-set immunity per §3a applies to it too). If the key was tracked
// (demoted), the mutation ITSELF is the §3b trigger-1 "non-read touch"
// re-admission: Tier flips back to resident so "the model is never
// editing a file whose content nothing in context holds" — paired with
// Stale=true so it renders as the re-read stub rather than stale bytes
// (the correctness rule from §2 still wins: never re-inject the old
// content as if current).
func (l *Ledger) RecordMutation(step int, k Key, diskBacked bool) *Entry {
	e, ok := l.entries[k.String()]
	if !ok {
		// Never seen before: nothing to invalidate, but the touch itself
		// still needs tracking so LRU/hot-set logic sees it.
		e = &Entry{Key: k, Tier: TierResident, DiskBacked: diskBacked}
		l.entries[k.String()] = e
	}
	e.LastTouchStep = step
	e.Stale = true
	e.Tier = TierResident
	return e
}

// ApplyFSChange marks k stale if the on-disk content hash no longer
// matches the ledger's recorded hash for it — the fs-watcher-driven half
// of the §2 staleness rule (mtime-sweep/notify feeding contracts.FSChange;
// mcp §5a). A "modified" event with an UNCHANGED hash (identical bytes) is
// explicitly a no-op per contracts.FSChange's doc comment.
func (l *Ledger) ApplyFSChange(k Key, kind, newHash string) {
	e, ok := l.entries[k.String()]
	if !ok {
		return
	}
	switch kind {
	case "deleted":
		e.Stale = true
	case "modified", "created":
		if newHash != "" && newHash != e.ContentHash {
			e.Stale = true
		}
	}
}

// Touch records that step named k without producing new content — the
// re-admission trigger 1 "touches the key… in any tool-call arg" (§3b) and
// trigger 2's cheap-string-match mention (§3b), both funnel here. Touch on
// a tracked, non-stale entry is exactly a re-admission: it flips back to
// resident (free — "un-stub the original bytes at the original position").
// A stale entry stays Stale (Touch alone never supplies fresh content —
// callers needing the stale-recovery paths use Readmit).
func (l *Ledger) Touch(step int, k Key) (*Entry, bool) {
	e, ok := l.entries[k.String()]
	if !ok {
		return nil, false
	}
	e.LastTouchStep = step
	if e.Tier == TierTracked && !e.Stale {
		e.Tier = TierResident
	}
	return e, true
}

// ResidentBytes sums the resident layer's cost.
func (l *Ledger) ResidentBytes() int {
	total := 0
	for _, e := range l.entries {
		total += e.residentBytes()
	}
	return total
}

// EvictionResult reports one LRU episode (§3a).
type EvictionResult struct {
	Demoted     []Key
	FreedBytes  int
	BytesBefore int
	BytesAfter  int
}

// RunEvictionEpisode applies §3a's hysteresis rule: only triggers when
// resident bytes exceed budgetBytes (100%); once triggered, demotes
// least-recently-touched, non-hot keys until resident bytes are at or
// below evictTo*budgetBytes (default 70%). Ties broken by Key string for
// determinism (ground rule 3). Demoting a key only changes Tier — the
// tracked-layer metadata (hash, seq, staleness) is exactly the same Entry,
// per §3b "no second content store exists or is needed". budgetBytes is
// caller-computed (Config.BudgetBytes needs the model's context window,
// which the ledger itself doesn't know).
func (l *Ledger) RunEvictionEpisode(step int, budgetBytes int64) EvictionResult {
	before := l.ResidentBytes()
	budget := int(budgetBytes)
	result := EvictionResult{BytesBefore: before, BytesAfter: before}
	if budgetBytes < 0 || before <= budget {
		return result
	}

	floor := int(float64(budget) * l.cfg.EvictTo)

	type cand struct {
		key string
		e   *Entry
	}
	var candidates []cand
	for ks, e := range l.entries {
		if e.Tier != TierResident {
			continue
		}
		if step-e.LastTouchStep <= l.cfg.HotSteps {
			continue // hot set immune, §3a
		}
		candidates = append(candidates, cand{ks, e})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].e.LastTouchStep != candidates[j].e.LastTouchStep {
			return candidates[i].e.LastTouchStep < candidates[j].e.LastTouchStep
		}
		return candidates[i].key < candidates[j].key
	})

	total := before
	var demoted []Key
	for _, c := range candidates {
		if total <= floor {
			break
		}
		freed := c.e.residentBytes()
		c.e.Tier = TierTracked
		total -= freed
		result.FreedBytes += freed
		demoted = append(demoted, c.e.Key)
	}
	result.Demoted = demoted
	result.BytesAfter = total
	return result
}

// ReadmitSource classifies how Readmit served (or refused) a key, per §3b
// "Re-admission sources".
type ReadmitSource int

const (
	// ReadmitNone: key not in the ledger at all.
	ReadmitNone ReadmitSource = iota
	// ReadmitAlreadyResident: no-op, nothing to do.
	ReadmitAlreadyResident
	// ReadmitFreeUnstub: tracked and valid (hash still matches disk) —
	// un-stub the original bytes at the original position, no I/O (§3b 1).
	ReadmitFreeUnstub
	// ReadmitNeedsDiskRead: stale but disk-backed — the harness must read
	// the disk itself and deliver fresh content (§3b 2). Readmit does not
	// perform the read (no DiskReader is required by the ledger); it
	// signals the caller to do so and calls RecordRead with the result.
	ReadmitNeedsDiskRead
	// ReadmitTrackedNoGroundTruth: no disk ground truth exists — the
	// tracked copy is the only truth there is; served as-is (§3b 3).
	ReadmitTrackedNoGroundTruth
)

// Readmit classifies key for re-admission per §3b, without itself doing
// any disk I/O (that seam is DiskReader, wired at the Manager level so the
// ledger stays pure/testable). Callers use the returned Source to decide
// what to do next; ReadmitFreeUnstub and ReadmitTrackedNoGroundTruth both
// flip the entry back to TierResident immediately (no further action
// needed) — ReadmitNeedsDiskRead leaves Tier as-is until the caller
// supplies fresh content via RecordRead.
func (l *Ledger) Readmit(step int, k Key) (*Entry, ReadmitSource) {
	e, ok := l.entries[k.String()]
	if !ok {
		return nil, ReadmitNone
	}
	e.LastTouchStep = step
	if e.Tier == TierResident && !e.Stale {
		return e, ReadmitAlreadyResident
	}
	if !e.Stale {
		e.Tier = TierResident
		return e, ReadmitFreeUnstub
	}
	if e.DiskBacked {
		return e, ReadmitNeedsDiskRead
	}
	e.Tier = TierResident
	return e, ReadmitTrackedNoGroundTruth
}

// EnforceTrackedBound evicts the coldest tracked entries once the tracked
// layer exceeds cfg.TrackedMaxKeys (§3b "purely to bound sweep cost").
// Deterministic tie-break by Key string.
func (l *Ledger) EnforceTrackedBound() []Key {
	var tracked []*Entry
	for _, e := range l.entries {
		if e.Tier == TierTracked {
			tracked = append(tracked, e)
		}
	}
	if len(tracked) <= l.cfg.TrackedMaxKeys {
		return nil
	}
	sort.Slice(tracked, func(i, j int) bool {
		if tracked[i].LastTouchStep != tracked[j].LastTouchStep {
			return tracked[i].LastTouchStep < tracked[j].LastTouchStep
		}
		return tracked[i].Key.String() < tracked[j].Key.String()
	})
	overflow := len(tracked) - l.cfg.TrackedMaxKeys
	var dropped []Key
	for i := 0; i < overflow; i++ {
		delete(l.entries, tracked[i].Key.String())
		dropped = append(dropped, tracked[i].Key)
	}
	return dropped
}
