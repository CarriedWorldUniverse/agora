package persistence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestRebuildIndexProperty: build a store through normal API use (Create,
// Append, Fork, Archive), drop the SQLite mirror, RebuildIndex, and check
// List() is identical before/after — the mirror is "always derivable from
// the JSONL" (spec §2), corruption of state.db is "an inconvenience, never
// data loss".
func TestRebuildIndexProperty(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalStore(root, Config{})
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}

	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_rb_1", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work/a"})
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_rb_2", CreatedAt: now, IdentityFP: "agora:y", Profile: "dev", WorkingDir: "/work/b"})

	if err := s.Append("th_rb_1", []contracts.ThreadItem{
		{TS: now.Add(time.Second), Type: contracts.TIUserMessage, Payload: "hello world"},
		{TS: now.Add(2 * time.Second), Type: contracts.TIAgentMessage, Payload: "hi there"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	child, err := s.Fork("th_rb_1", 1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if err := s.Append(child.ThreadID, []contracts.ThreadItem{
		{TS: now.Add(3 * time.Second), Type: contracts.TIUserMessage, Payload: "child msg"},
	}); err != nil {
		t.Fatalf("Append child: %v", err)
	}
	if err := s.Archive("th_rb_2"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	before, err := s.List(contracts.ListFilter{})
	if err != nil {
		t.Fatalf("List before: %v", err)
	}
	beforeItems1 := resumeAll(t, s, "th_rb_1")
	beforeItemsChild := resumeAll(t, s, child.ThreadID)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "state.db")); err != nil {
		t.Fatalf("remove state.db: %v", err)
	}

	s2, err := NewLocalStore(root, Config{})
	if err != nil {
		t.Fatalf("reopen NewLocalStore: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if _, err := s2.RebuildIndex(); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}

	after, err := s2.List(contracts.ListFilter{})
	if err != nil {
		t.Fatalf("List after: %v", err)
	}
	assertMetaListsEqual(t, before, after)

	afterItems1 := resumeAll(t, s2, "th_rb_1")
	assertItemsEqual(t, beforeItems1, afterItems1)
	afterItemsChild := resumeAll(t, s2, child.ThreadID)
	assertItemsEqual(t, beforeItemsChild, afterItemsChild)

	// archived is PRIMARY state (state.db only, NOT JSONL-derived). This test
	// DELETED state.db entirely, so archived is legitimately lost — that's the
	// spec §2 model (primary state is backed up with the db, not the JSONL).
	// Preservation of archived across an IN-PLACE rebuild is covered by
	// TestArchivedSurvivesInPlaceRebuild.
	archivedFlag := true
	archivedList, err := s2.List(contracts.ListFilter{Archived: &archivedFlag})
	if err != nil {
		t.Fatalf("List archived after rebuild: %v", err)
	}
	if len(archivedList) != 0 {
		t.Fatalf("archived after full state.db loss = %+v, want none (primary state is not JSONL-derived)", archivedList)
	}
}

// TestArchivedSurvivesInPlaceRebuild: archived is primary state held in
// state.db; an IN-PLACE RebuildIndex (mirror index corruption recovery, the
// db file still present) must preserve it while it rebuilds the derived
// columns from the JSONL. Regression for the review finding that replaced
// the fragile .archived sidecar file with a preserved primary column.
func TestArchivedSurvivesInPlaceRebuild(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalStore(root, Config{})
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_keep", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/w"})
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_arc", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/w"})
	if err := s.Archive("th_arc"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	// In-place rebuild (no db removal — the file survives).
	if _, err := s.RebuildIndex(); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	yes := true
	arc, err := s.List(contracts.ListFilter{Archived: &yes})
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if len(arc) != 1 || arc[0].ThreadID != "th_arc" {
		t.Fatalf("archived after in-place rebuild = %+v, want just th_arc", arc)
	}
}

func assertMetaListsEqual(t *testing.T, before, after []contracts.ThreadMeta) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("List length changed: before=%d after=%d", len(before), len(after))
	}
	sort.Slice(before, func(i, j int) bool { return before[i].ThreadID < before[j].ThreadID })
	sort.Slice(after, func(i, j int) bool { return after[i].ThreadID < after[j].ThreadID })
	for i := range before {
		b, a := before[i], after[i]
		if b.ThreadID != a.ThreadID || b.WorkingDir != a.WorkingDir || b.ProjectRoot != a.ProjectRoot ||
			b.IdentityFP != a.IdentityFP || b.Profile != a.Profile || !b.CreatedAt.Equal(a.CreatedAt) ||
			!forkRefEqual(b.ForkOf, a.ForkOf) {
			t.Errorf("thread %d differs before/after rebuild:\n before=%+v\n after=%+v", i, b, a)
		}
	}
}

func assertItemsEqual(t *testing.T, before, after []contracts.ThreadItem) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("items length changed: before=%d after=%d", len(before), len(after))
	}
	for i := range before {
		b, a := before[i], after[i]
		if b.Seq != a.Seq || b.Type != a.Type || !b.TS.Equal(a.TS) {
			t.Errorf("item %d differs before/after rebuild:\n before=%+v\n after=%+v", i, b, a)
		}
		// Payload must also survive (JSON round-trip: compare canonicalized).
		if jsonEq(b.Payload) != jsonEq(a.Payload) {
			t.Errorf("item %d payload differs: before=%v after=%v", i, b.Payload, a.Payload)
		}
	}
}

// jsonEq canonicalizes a payload to its JSON string for comparison across a
// store round-trip (where a typed payload may re-decode as map[string]any).
func jsonEq(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(b)
}

// TestCrashSafety: append items, then — WITHOUT calling Close (simulating a
// process crash before clean shutdown) — open a brand-new LocalStore
// against the same root and verify every item is present. Spec §1: "append
// + fsync on turn boundaries."
func TestCrashSafety(t *testing.T) {
	root := t.TempDir()

	func() {
		s, err := NewLocalStore(root, Config{Fsync: FsyncItem})
		if err != nil {
			t.Fatalf("NewLocalStore: %v", err)
		}
		// Deliberately no s.Close() / defer — simulate an unclean stop.
		now := time.Now().UTC()
		mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_crash", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work"})
		if err := s.Append("th_crash", []contracts.ThreadItem{
			{TS: now.Add(time.Second), Type: contracts.TIUserMessage, Payload: "before crash"},
			{TS: now.Add(2 * time.Second), Type: contracts.TIAgentMessage, Payload: "still before crash"},
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}()

	s2, err := NewLocalStore(root, Config{})
	if err != nil {
		t.Fatalf("reopen after crash: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	items := resumeAll(t, s2, "th_crash")
	if len(items) != 2 {
		t.Fatalf("items after crash-reopen = %d, want 2: %+v", len(items), items)
	}
	if p, ok := items[0].Payload.(string); !ok || p != "before crash" {
		t.Errorf("items[0] = %+v", items[0])
	}

	// Also verify directly against the JSONL file on disk — the actual
	// source of truth, independent of the (also-surviving) mirror.
	meta, err := s2.Meta("th_crash")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	path := threadPath(root, meta.CreatedAt, "th_crash")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("jsonl file is empty after crash")
	}
}
