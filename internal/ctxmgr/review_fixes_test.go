package ctxmgr

// Regression tests for the U12 review gate (security-validator).

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// MEDIUM — §3b re-admission source 3: a stale entry with NO disk ground truth
// (web/MCP/unrepeatable output) must serve its tracked copy AS the current
// truth ("it is the only truth there is"), not the stale stub. The bug left
// Stale=true, so renderKeyed's stale-first gate stubbed it every time and
// source 3 was unreachable.
func TestReadmit_NoGroundTruth_ServesTrackedCopyNotStub(t *testing.T) {
	l := NewLedger(DefaultConfig())
	k := Key{Domain: "web", ID: "https://example.com/x"}
	l.RecordRead(0, k, 10, "h1", 100, false) // NOT disk-backed
	l.RecordMutation(1, k, false)            // stale, still no disk ground truth
	if e, _ := l.Get(k); !e.Stale {
		t.Fatal("precondition: entry must be stale after mutation")
	}

	got, src := l.Readmit(2, k)
	if src != ReadmitTrackedNoGroundTruth {
		t.Fatalf("src=%v, want ReadmitTrackedNoGroundTruth", src)
	}
	if got.Stale {
		t.Fatal("no fresher source exists — Stale must be cleared so the tracked copy is served, not stubbed")
	}
	if !got.NoGroundTruth || got.Tier != TierResident {
		t.Fatalf("want NoGroundTruth resident entry, got %+v", got)
	}

	rendered := renderKeyed(l, k, 10, "ORIGINAL WEB BODY")
	if strings.Contains(rendered, "modified since this read") {
		t.Fatalf("must not render the stale stub for a no-ground-truth re-admit: %q", rendered)
	}
	if !strings.Contains(rendered, "ORIGINAL WEB BODY") || !strings.Contains(rendered, "no fresher source") {
		t.Fatalf("want served body + provenance marker, got %q", rendered)
	}
}

// LOW — DrainEvents must be deterministic: two events of the same Type need a
// fixed order (byte-identical assembly is a §7 pillar). A non-stable Type-only
// sort left same-type events in undefined order.
func TestDrainEvents_DeterministicOrderForSameType(t *testing.T) {
	order := func() []contracts.EventType {
		m := NewManager(DefaultConfig(), testModel())
		m.lastEvents = []contracts.Event{
			NewCurationReadmittedEvent("t1", Key{Domain: "file", ID: "z.py"}),
			NewCurationReadmittedEvent("t1", Key{Domain: "file", ID: "a.py"}),
			NewCurationDemotedEvent("t1", []Key{{Domain: "file", ID: "m.py"}}, 10),
			NewCurationReadmittedEvent("t1", Key{Domain: "file", ID: "m.py"}),
		}
		ev := m.DrainEvents()
		var types []contracts.EventType
		for _, e := range ev {
			types = append(types, e.Type)
		}
		return types
	}
	first := order()
	for i := 0; i < 50; i++ {
		got := order()
		if len(got) != len(first) {
			t.Fatalf("length drift: %d vs %d", len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("non-deterministic DrainEvents order at %d: %v vs %v", j, got, first)
			}
		}
	}
}

// HIGH — re-admitted disk content must respect the per-item cap (§3a: per-item
// cap BEFORE budget). Injecting the raw uncapped file defeated the entire
// budget system (a 5MB file injected in full, re-injected every turn).
func TestReadmit_CapsInjectedDiskContent(t *testing.T) {
	huge := strings.Repeat("X", 5_000_000)
	disk := &stubDiskReader{files: map[string]string{"a.py": huge}}
	cfg := DefaultConfig()
	cfg.HotSteps = 0
	cfg.MaxRetainBytes = 100
	m := NewManager(cfg, testModel(), WithDiskReader(disk))

	items := []contracts.ThreadItem{
		readCall(t, 1, "c1", "a.py"),
		readResult(2, "c1", "ORIGINAL"),
		editCall(t, 3, "c2", "a.py"),
		agentMsg(4, "noop"), agentMsg(5, "noop"), agentMsg(6, "noop"),
		cmdCall(t, 7, "c3", "lint"),
		cmdResult(8, "c3", "warning in a.py: unused import"),
	}
	out, err := m.Assemble("t1", items)
	if err != nil {
		t.Fatal(err)
	}
	joined := joinContents(out)
	// The re-admitted content must be capped, not injected raw. Generous slack
	// for wrappers + the other (small) items; without the fix this is ~5MB.
	if len(joined) > cfg.MaxRetainBytes+100_000 {
		t.Fatalf("re-admitted disk content not capped: assembled output %d bytes (MaxRetainBytes=%d)", len(joined), cfg.MaxRetainBytes)
	}
}

// MED — Manager.fsObserved is read by Assemble and written by ApplyFSChange; an
// async fs-watcher calling ApplyFSChange concurrently with an Assemble must not
// data-race (a concurrent map read/write is a fatal Go panic). Run under -race.
func TestManager_ConcurrentApplyFSChangeAndAssemble(t *testing.T) {
	m := NewManager(DefaultConfig(), testModel())
	items := []contracts.ThreadItem{readCall(t, 1, "c1", "a.py"), readResult(2, "c1", "x")}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			m.ApplyFSChange(contracts.FSChange{Path: fmt.Sprintf("f%d.py", i), Kind: "modified", ContentHash: "h"})
		}(i)
		go func() {
			defer wg.Done()
			_, _ = m.Assemble("t1", items)
			_ = m.DrainEvents()
			m.Observe(contracts.Usage{Input: 10})
		}()
	}
	wg.Wait()
}
