package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestThreadStoreBehavior is the SAME table-driven behavioral suite driven
// against LocalStore and MemStore, per the U3 brief: "MemStore and
// LocalStore pass the SAME table-driven behavioral suite."
func TestThreadStoreBehavior(t *testing.T) {
	factories := map[string]func(t *testing.T) contracts.ThreadStore{
		"LocalStore": func(t *testing.T) contracts.ThreadStore {
			t.Helper()
			s, err := NewLocalStore(t.TempDir(), Config{})
			if err != nil {
				t.Fatalf("NewLocalStore: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
		"MemStore": func(t *testing.T) contracts.ThreadStore {
			return NewMemStore()
		},
	}

	cases := map[string]func(t *testing.T, newStore func(t *testing.T) contracts.ThreadStore){
		"CreateAppendResumeRoundTrip":  testCreateAppendResumeRoundTrip,
		"ForkChainThroughAndIsolation": testForkChainThroughAndIsolation,
		"ListFilterByWorkingDir":       testListFilterByWorkingDir,
		"NotFoundErrors":               testNotFoundErrors,
		"DoubleCreateFails":            testDoubleCreateFails,
		"ArchiveHidesFromDefaultList":  testArchiveHidesFromDefaultList,
		"DeletePurges":                 testDeletePurges,
		"WDChangedLatestWinsAtResume":  testWDChangedLatestWinsAtResume,
	}

	for storeName, newStore := range factories {
		for caseName, fn := range cases {
			t.Run(storeName+"/"+caseName, func(t *testing.T) {
				fn(t, newStore)
			})
		}
	}
}

func mustCreate(t *testing.T, s contracts.ThreadStore, meta contracts.ThreadMeta) {
	t.Helper()
	if err := s.Create(meta); err != nil {
		t.Fatalf("Create(%s): %v", meta.ThreadID, err)
	}
}

func resumeAll(t *testing.T, s contracts.ThreadStore, threadID string) []contracts.ThreadItem {
	t.Helper()
	it, err := s.Resume(threadID)
	if err != nil {
		t.Fatalf("Resume(%s): %v", threadID, err)
	}
	defer it.Close()
	var out []contracts.ThreadItem
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		out = append(out, item)
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator Err: %v", err)
	}
	return out
}

func testCreateAppendResumeRoundTrip(t *testing.T, newStore func(t *testing.T) contracts.ThreadStore) {
	s := newStore(t)
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	meta := contracts.ThreadMeta{
		ThreadID:   "th_round_trip",
		CreatedAt:  now,
		IdentityFP: "agora:test",
		Profile:    "dev",
		WorkingDir: "/work/a",
	}
	mustCreate(t, s, meta)

	got, err := s.Meta("th_round_trip")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if got.ThreadID != meta.ThreadID || got.WorkingDir != meta.WorkingDir {
		t.Fatalf("Meta mismatch: got %+v want %+v", got, meta)
	}

	items := []contracts.ThreadItem{
		{TS: now.Add(1 * time.Second), Type: contracts.TIUserMessage, Payload: "hello"},
		{TS: now.Add(2 * time.Second), Type: contracts.TIAgentMessage, Payload: "hi"},
		{TS: now.Add(3 * time.Second), Type: contracts.TIToolCall, Payload: map[string]any{"tool": "ls"}},
	}
	if err := s.Append("th_round_trip", items); err != nil {
		t.Fatalf("Append: %v", err)
	}

	replayed := resumeAll(t, s, "th_round_trip")
	if len(replayed) != 3 {
		t.Fatalf("replayed len = %d, want 3", len(replayed))
	}
	for i, it := range replayed {
		wantSeq := int64(i + 1)
		if it.Seq != wantSeq {
			t.Errorf("item %d: Seq = %d, want %d", i, it.Seq, wantSeq)
		}
		if it.Type != items[i].Type {
			t.Errorf("item %d: Type = %s, want %s", i, it.Type, items[i].Type)
		}
	}

	// A second Append continues the sequence.
	more := []contracts.ThreadItem{{TS: now.Add(4 * time.Second), Type: contracts.TIToolResult, Payload: "ok"}}
	if err := s.Append("th_round_trip", more); err != nil {
		t.Fatalf("second Append: %v", err)
	}
	replayed = resumeAll(t, s, "th_round_trip")
	if len(replayed) != 4 || replayed[3].Seq != 4 {
		t.Fatalf("after second append: len=%d seq=%v", len(replayed), replayed)
	}
}

func testForkChainThroughAndIsolation(t *testing.T, newStore func(t *testing.T) contracts.ThreadStore) {
	s := newStore(t)
	now := time.Date(2026, 7, 16, 11, 0, 0, 0, time.UTC)
	parent := contracts.ThreadMeta{
		ThreadID:   "th_parent",
		CreatedAt:  now,
		IdentityFP: "agora:test",
		Profile:    "dev",
		WorkingDir: "/work/a",
	}
	mustCreate(t, s, parent)
	if err := s.Append("th_parent", []contracts.ThreadItem{
		{TS: now.Add(1 * time.Second), Type: contracts.TIUserMessage, Payload: "p1"},
		{TS: now.Add(2 * time.Second), Type: contracts.TIAgentMessage, Payload: "p2"},
		{TS: now.Add(3 * time.Second), Type: contracts.TIAgentMessage, Payload: "p3"},
	}); err != nil {
		t.Fatalf("Append parent: %v", err)
	}

	// Fork at seq 2 (backtrack before p3).
	childMeta, err := s.Fork("th_parent", 2)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if childMeta.ForkOf == nil || childMeta.ForkOf.ThreadID != "th_parent" || childMeta.ForkOf.Seq != 2 {
		t.Fatalf("child ForkOf = %+v, want {th_parent 2}", childMeta.ForkOf)
	}

	if err := s.Append(childMeta.ThreadID, []contracts.ThreadItem{
		{TS: now.Add(10 * time.Second), Type: contracts.TIUserMessage, Payload: "c1"},
	}); err != nil {
		t.Fatalf("Append child: %v", err)
	}

	childItems := resumeAll(t, s, childMeta.ThreadID)
	if len(childItems) != 3 {
		t.Fatalf("child replay len = %d, want 3 (p1,p2,c1)", len(childItems))
	}
	if p, ok := childItems[0].Payload.(string); !ok || p != "p1" {
		t.Errorf("childItems[0] = %+v, want p1", childItems[0])
	}
	if p, ok := childItems[1].Payload.(string); !ok || p != "p2" {
		t.Errorf("childItems[1] = %+v, want p2", childItems[1])
	}
	if childItems[2].Seq != 3 {
		t.Errorf("child's own item Seq = %d, want 3 (continues after fork point)", childItems[2].Seq)
	}
	if p, ok := childItems[2].Payload.(string); !ok || p != "c1" {
		t.Errorf("childItems[2] = %+v, want c1", childItems[2])
	}

	// Post-fork parent activity must NOT leak into the child.
	if err := s.Append("th_parent", []contracts.ThreadItem{
		{TS: now.Add(20 * time.Second), Type: contracts.TIAgentMessage, Payload: "p4-after-fork"},
	}); err != nil {
		t.Fatalf("Append parent post-fork: %v", err)
	}
	childItemsAfter := resumeAll(t, s, childMeta.ThreadID)
	if len(childItemsAfter) != 3 {
		t.Fatalf("child replay after parent post-fork append: len = %d, want still 3", len(childItemsAfter))
	}
	for _, it := range childItemsAfter {
		if p, ok := it.Payload.(string); ok && p == "p4-after-fork" {
			t.Fatalf("child sees post-fork parent item — isolation violated")
		}
	}

	// Parent's own replay includes p4 (parent unaffected by the fork).
	parentItems := resumeAll(t, s, "th_parent")
	if len(parentItems) != 4 {
		t.Fatalf("parent replay len = %d, want 4", len(parentItems))
	}
}

func testListFilterByWorkingDir(t *testing.T, newStore func(t *testing.T) contracts.ThreadStore) {
	s := newStore(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_wd_a1", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work/a"})
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_wd_a2", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work/a"})
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_wd_b1", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work/b"})

	got, err := s.List(contracts.ListFilter{WorkingDir: "/work/a"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List(/work/a) len = %d, want 2: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, m := range got {
		seen[m.ThreadID] = true
		if m.WorkingDir != "/work/a" {
			t.Errorf("returned thread %s has WorkingDir %s", m.ThreadID, m.WorkingDir)
		}
	}
	if !seen["th_wd_a1"] || !seen["th_wd_a2"] {
		t.Fatalf("List(/work/a) missing expected threads: %+v", got)
	}

	all, err := s.List(contracts.ListFilter{})
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List(all) len = %d, want 3", len(all))
	}
}

func testNotFoundErrors(t *testing.T, newStore func(t *testing.T) contracts.ThreadStore) {
	s := newStore(t)
	if _, err := s.Meta("th_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Meta(missing) err = %v, want ErrNotFound", err)
	}
	if _, err := s.Resume("th_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resume(missing) err = %v, want ErrNotFound", err)
	}
	if err := s.Append("th_missing", []contracts.ThreadItem{{Type: contracts.TIUserMessage}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Append(missing) err = %v, want ErrNotFound", err)
	}
	if _, err := s.Fork("th_missing", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("Fork(missing) err = %v, want ErrNotFound", err)
	}
	if err := s.Archive("th_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Archive(missing) err = %v, want ErrNotFound", err)
	}
	if err := s.Delete("th_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(missing) err = %v, want ErrNotFound", err)
	}
}

func testDoubleCreateFails(t *testing.T, newStore func(t *testing.T) contracts.ThreadStore) {
	s := newStore(t)
	meta := contracts.ThreadMeta{ThreadID: "th_dup", CreatedAt: time.Now().UTC(), IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work"}
	mustCreate(t, s, meta)
	if err := s.Create(meta); !errors.Is(err, ErrExists) {
		t.Fatalf("second Create err = %v, want ErrExists", err)
	}
}

func testArchiveHidesFromDefaultList(t *testing.T, newStore func(t *testing.T) contracts.ThreadStore) {
	s := newStore(t)
	now := time.Now().UTC()
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_arch_1", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work"})
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_arch_2", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work"})

	if err := s.Archive("th_arch_1"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	notArchived := false
	got, err := s.List(contracts.ListFilter{WorkingDir: "/work", Archived: &notArchived})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range got {
		if m.ThreadID == "th_arch_1" {
			t.Fatalf("archived thread th_arch_1 appeared in Archived=false List")
		}
	}
	archived := true
	got, err = s.List(contracts.ListFilter{WorkingDir: "/work", Archived: &archived})
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if len(got) != 1 || got[0].ThreadID != "th_arch_1" {
		t.Fatalf("List(Archived=true) = %+v, want just th_arch_1", got)
	}
}

func testDeletePurges(t *testing.T, newStore func(t *testing.T) contracts.ThreadStore) {
	s := newStore(t)
	now := time.Now().UTC()
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_del", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work"})
	if err := s.Delete("th_del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Meta("th_del"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Meta after Delete err = %v, want ErrNotFound", err)
	}
	got, err := s.List(contracts.ListFilter{WorkingDir: "/work"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range got {
		if m.ThreadID == "th_del" {
			t.Fatalf("deleted thread still in List")
		}
	}
}

func testWDChangedLatestWinsAtResume(t *testing.T, newStore func(t *testing.T) contracts.ThreadStore) {
	s := newStore(t)
	now := time.Now().UTC()
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_wd_change", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work/old"})

	if err := s.Append("th_wd_change", []contracts.ThreadItem{
		{TS: now.Add(time.Second), Type: contracts.TIWDChanged, Payload: map[string]any{"working_dir": "/work/new"}},
	}); err != nil {
		t.Fatalf("Append wd_changed: %v", err)
	}

	got, err := s.Meta("th_wd_change")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if got.WorkingDir != "/work/new" {
		t.Fatalf("Meta.WorkingDir = %s, want /work/new (latest wd_changed wins)", got.WorkingDir)
	}

	byNewWD, err := s.List(contracts.ListFilter{WorkingDir: "/work/new"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, m := range byNewWD {
		if m.ThreadID == "th_wd_change" {
			found = true
		}
	}
	if !found {
		t.Fatalf("List(WorkingDir=/work/new) did not find th_wd_change: %+v", byNewWD)
	}
}
