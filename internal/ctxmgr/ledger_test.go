package ctxmgr

import "testing"

func fileKey(id string) Key { return Key{Domain: "file", ID: id} }

func TestLedger_RecordReadThenWrite_OneLiveCopyPerKey(t *testing.T) {
	// §2: "one live copy per key in the whole assembly, wherever it
	// lives" — a write_file on the same path a read_file already covered
	// must collide onto the SAME entry, not create a second one.
	l := NewLedger(DefaultConfig())
	k := fileKey("src/a.py")
	l.RecordRead(0, k, 10, "hash1", 100, true)
	l.RecordWrite(1, k, 11, "hash2", 120, true)

	if len(l.Entries()) != 1 {
		t.Fatalf("entries = %d, want 1 (one live copy per key)", len(l.Entries()))
	}
	e, ok := l.Get(k)
	if !ok {
		t.Fatal("entry not found")
	}
	if e.Seq != 11 || e.ContentHash != "hash2" {
		t.Fatalf("live copy = %+v, want the write's args as newest truth", e)
	}
}

func TestLedger_EditInvalidatesLiveCopy(t *testing.T) {
	l := NewLedger(DefaultConfig())
	k := fileKey("src/a.py")
	l.RecordRead(0, k, 10, "hash1", 100, true)
	l.RecordMutation(1, k, true)

	e, _ := l.Get(k)
	if !e.Stale {
		t.Fatalf("edit must invalidate the live copy: %+v", e)
	}
}

func TestLedger_FSChangeMarksStaleOnHashMismatch(t *testing.T) {
	l := NewLedger(DefaultConfig())
	k := fileKey("src/a.py")
	l.RecordRead(0, k, 10, "hash1", 100, true)

	l.ApplyFSChange(k, "modified", "hash1") // identical bytes -> no-op
	if e, _ := l.Get(k); e.Stale {
		t.Fatalf("identical-hash modified event must NOT invalidate: %+v", e)
	}

	l.ApplyFSChange(k, "modified", "hash2")
	if e, _ := l.Get(k); !e.Stale {
		t.Fatalf("hash-mismatch modified event must invalidate")
	}
}

func TestLedger_PerItemCapTruncatesBeforeBudget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxRetainBytes = 10
	l := NewLedger(cfg)
	k := fileKey("big.py")
	e := l.RecordRead(0, k, 1, "h", 1000, true)
	if !e.Truncated || e.SizeBytes != 10 {
		t.Fatalf("entry = %+v, want truncated to MaxRetainBytes", e)
	}
}

func TestLedger_HotSetImmuneToEviction(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HotSteps = 3
	l := NewLedger(cfg)
	hot := fileKey("hot.py")
	cold := fileKey("cold.py")
	l.RecordRead(0, cold, 1, "h1", 1000, true)
	l.RecordRead(9, hot, 2, "h2", 1000, true) // touched at the current step

	res := l.RunEvictionEpisode(10, 100) // budget tiny: forces an episode
	if contains(res.Demoted, hot) {
		t.Fatalf("hot key must never be demoted: %+v", res.Demoted)
	}
	if !contains(res.Demoted, cold) {
		t.Fatalf("cold key should be demoted: %+v", res.Demoted)
	}
}

func TestLedger_EvictionHysteresis_TriggersAt100DemotesTo70(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HotSteps = 0
	cfg.EvictTo = 0.70
	l := NewLedger(cfg)
	// Four 100-byte cold entries, distinct LastTouchStep so LRU order is
	// deterministic.
	keys := []Key{fileKey("a"), fileKey("b"), fileKey("c"), fileKey("d")}
	for i, k := range keys {
		l.RecordRead(i, k, int64(i), "h", 100, true)
	}
	// resident = 400, budget = 100 -> over 100% (400), must demote to
	// <= 70 (floor). Demote oldest (a, step0) then (b, step1): 400-100=300,
	// still > 70, 300-100=200 > 70, ... continues until <= 70: needs to
	// demote all but nothing fits under 70 except 0. With 100-byte items
	// and floor 70, every demotion leaves a multiple of 100 until 0.
	res := l.RunEvictionEpisode(10, 100)
	if res.BytesAfter > 70 {
		t.Fatalf("bytes after = %d, want <= floor 70", res.BytesAfter)
	}
	// oldest-first order: a demoted before b before c...
	if len(res.Demoted) == 0 || res.Demoted[0] != fileKey("a") {
		t.Fatalf("demoted order = %+v, want oldest (a) first", res.Demoted)
	}
}

func TestLedger_EvictionNoTriggerUnderBudget(t *testing.T) {
	l := NewLedger(DefaultConfig())
	k := fileKey("a")
	l.RecordRead(0, k, 1, "h", 10, true)
	res := l.RunEvictionEpisode(1, 1000)
	if len(res.Demoted) != 0 {
		t.Fatalf("under budget must not trigger an episode: %+v", res)
	}
}

func TestLedger_ReadmitFreeUnstub(t *testing.T) {
	l := NewLedger(DefaultConfig())
	k := fileKey("a")
	l.RecordRead(0, k, 1, "h", 10, true)
	l.RunEvictionEpisode(10, 0) // force demotion (budget 0, well past HotSteps)
	if e, _ := l.Get(k); e.Tier != TierTracked {
		t.Fatalf("expected demoted, got %+v", e)
	}

	e, src := l.Readmit(5, k)
	if src != ReadmitFreeUnstub {
		t.Fatalf("src = %v, want ReadmitFreeUnstub", src)
	}
	if e.Tier != TierResident {
		t.Fatalf("readmit must flip to resident: %+v", e)
	}
}

func TestLedger_ReadmitStaleDiskBackedNeedsRead(t *testing.T) {
	l := NewLedger(DefaultConfig())
	k := fileKey("a")
	l.RecordRead(0, k, 1, "h1", 10, true)
	l.RunEvictionEpisode(10, 0)
	l.ApplyFSChange(k, "modified", "h2")

	_, src := l.Readmit(5, k)
	if src != ReadmitNeedsDiskRead {
		t.Fatalf("src = %v, want ReadmitNeedsDiskRead", src)
	}
}

func TestLedger_ReadmitNoDiskGroundTruth(t *testing.T) {
	l := NewLedger(DefaultConfig())
	k := Key{Domain: "web", ID: "https://example.com"}
	l.RecordRead(0, k, 1, "h1", 10, false)
	l.RunEvictionEpisode(10, 0)
	// stale it via an edit-equivalent path is not possible (no disk
	// backing) — mark it stale to exercise source 3 directly.
	e, _ := l.Get(k)
	e.Stale = true

	e, src := l.Readmit(5, k)
	if src != ReadmitTrackedNoGroundTruth {
		t.Fatalf("src = %v, want ReadmitTrackedNoGroundTruth", src)
	}
	if e.Tier != TierResident {
		t.Fatalf("no-ground-truth readmit must still serve the tracked copy: %+v", e)
	}
}

func TestLedger_ReadmitKeyNotFound(t *testing.T) {
	l := NewLedger(DefaultConfig())
	_, src := l.Readmit(0, fileKey("nope"))
	if src != ReadmitNone {
		t.Fatalf("src = %v, want ReadmitNone", src)
	}
}

func TestLedger_TrackedBoundEvictsColdestMetadata(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TrackedMaxKeys = 2
	l := NewLedger(cfg)
	for i, id := range []string{"a", "b", "c"} {
		k := fileKey(id)
		l.RecordRead(i, k, int64(i), "h", 10, true)
	}
	l.RunEvictionEpisode(100, 0) // demote all three
	dropped := l.EnforceTrackedBound()
	if len(dropped) != 1 || dropped[0] != fileKey("a") {
		t.Fatalf("dropped = %+v, want [a] (coldest)", dropped)
	}
	if _, ok := l.Get(fileKey("a")); ok {
		t.Fatal("dropped key must be gone entirely — falling off tracked loses nothing durable in the THREAD, but the ledger row is gone")
	}
}

func TestLedger_DeterministicEntryOrder(t *testing.T) {
	l := NewLedger(DefaultConfig())
	for _, id := range []string{"z", "a", "m"} {
		l.RecordRead(0, fileKey(id), 1, "h", 1, true)
	}
	for i := 0; i < 5; i++ {
		entries := l.Entries()
		if entries[0].Key.ID != "a" || entries[1].Key.ID != "m" || entries[2].Key.ID != "z" {
			t.Fatalf("iteration %d: entries not deterministically sorted: %+v", i, entries)
		}
	}
}

func contains(ks []Key, k Key) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}
