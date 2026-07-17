package ctxmgr

import (
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// stubDiskReader is an in-memory DiskReader test double.
type stubDiskReader struct {
	files map[string]string
	reads int
}

func (s *stubDiskReader) ReadFile(path string) ([]byte, string, error) {
	s.reads++
	c, ok := s.files[path]
	if !ok {
		return nil, "", errFileNotFound
	}
	return []byte(c), hashBytes([]byte(c)), nil
}

var errFileNotFound = &notFoundErr{}

type notFoundErr struct{}

func (*notFoundErr) Error() string { return "not found" }

func TestAssemble_ReadmitOnMention_StaleDiskBacked_HarnessReadsDisk(t *testing.T) {
	disk := &stubDiskReader{files: map[string]string{"a.py": "FRESH FROM DISK"}}
	cfg := DefaultConfig()
	cfg.HotSteps = 0
	m := NewManager(cfg, testModel(), WithDiskReader(disk))

	items := []contracts.ThreadItem{
		readCall(t, 1, "c1", "a.py"),
		readResult(2, "c1", "ORIGINAL"),
		editCall(t, 3, "c2", "a.py"), // stales it AND re-admits (RecordMutation)
		agentMsg(4, "noop"),
		agentMsg(5, "noop"),
		agentMsg(6, "noop"),
		// A later diagnostic mentions the path — mention-triggered
		// re-admission of a stale, disk-backed key must read disk itself.
		cmdCall(t, 7, "c3", "lint"),
		cmdResult(8, "c3", "warning in a.py: unused import"),
	}
	out, err := m.Assemble("t1", items)
	if err != nil {
		t.Fatal(err)
	}
	if disk.reads == 0 {
		t.Fatal("expected the harness to read disk itself for the stale, disk-backed mention")
	}
	joined := joinContents(out)
	if !strings.Contains(joined, "FRESH FROM DISK") {
		t.Fatalf("fresh disk content must be delivered somewhere in the assembly:\n%s", joined)
	}

	events := m.DrainEvents()
	found := false
	for _, e := range events {
		if e.Type == contracts.EvCurationReadmitted {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a thread.curation.readmitted event")
	}
}

func TestAssemble_ReadmitOnMention_NoDiskReaderFailsClosed(t *testing.T) {
	// §3b: "the stub never bounces work back to the model that the harness
	// can do itself" — but if no DiskReader is configured at all, Assemble
	// must not panic or silently fabricate content; it degrades to leaving
	// the stale stub in place.
	cfg := DefaultConfig()
	cfg.HotSteps = 0
	m := NewManager(cfg, testModel()) // no DiskReader
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
	if !strings.Contains(joined, "modified since") {
		t.Fatalf("without a DiskReader the stale stub must remain, not fabricate content:\n%s", joined)
	}
}

func TestAssemble_ReadmitOnMentionDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ReadmitOnMention = false
	cfg.HotSteps = 0
	m := NewManager(cfg, testModel())
	items := []contracts.ThreadItem{
		readCall(t, 1, "c1", "a.py"),
		readResult(2, "c1", "CONTENT"),
	}
	if _, err := m.Assemble("t1", items); err != nil {
		t.Fatal(err)
	}
	// Sanity: disabling the flag doesn't break assembly.
}

func TestCompact_HaltedByHookSkipsPostCompact(t *testing.T) {
	h := &haltingHooks{}
	m := NewManager(DefaultConfig(), testModel(), WithHookRunner(h))
	result, err := m.Compact(contracts.CompactManual)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NoOp {
		t.Fatalf("halted compaction must report NoOp: %+v", result)
	}
	if h.postCalled {
		t.Fatal("PostCompact must not fire when PreCompact halts")
	}
}

type haltingHooks struct{ postCalled bool }

func (haltingHooks) RunPreCompact(contracts.CompactionTrigger) bool { return true }
func (h *haltingHooks) RunPostCompact(contracts.CompactionResult)   { h.postCalled = true }
