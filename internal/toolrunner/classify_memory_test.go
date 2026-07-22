package toolrunner

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestClassifyMemoryRead/List: memory.read/list are read-only, classified
// the same KindRead way fs's read_file/list_dir are (NEX-782) — auto-
// allowed under every non-strict preset.
func TestClassifyMemoryRead(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: contracts.ToolMemoryRead, Args: mustArgs(t, memoryReadArgs{Name: "op-prefs"})}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindRead)
	}
	assertJSONExact(t, payload, `{"detail":"op-prefs"}`)
}

func TestClassifyMemoryList(t *testing.T) {
	roots := newTestRoots(t)
	kind, _ := Classify(Call{Name: contracts.ToolMemoryList}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindRead)
	}
}

// TestClassifyMemoryWrite: mirrors write_file's mutating classification
// (KindPatch) — spec §3's "carries its own grant" note, so unlike
// write_file this does NOT check roots at all (the memory dir is outside
// the fs sandbox by design).
func TestClassifyMemoryWrite(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: contracts.ToolMemoryWrite, Args: mustArgs(t, memoryWriteArgs{
		Name: "op-prefs",
		Body: "hello",
	})}, roots)
	if kind != contracts.KindPatch {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindPatch)
	}
	pp, ok := payload.(PatchPayload)
	if !ok {
		t.Fatalf("payload = %#v, want PatchPayload", payload)
	}
	if pp.Path != "op-prefs.md" {
		t.Fatalf("PatchPayload.Path = %q, want op-prefs.md", pp.Path)
	}
}

func TestClassifyMemoryDelete(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: contracts.ToolMemoryDelete, Args: mustArgs(t, memoryDeleteArgs{Name: "op-prefs"})}, roots)
	if kind != contracts.KindPatch {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindPatch)
	}
	pp, ok := payload.(PatchPayload)
	if !ok || pp.Path != "op-prefs.md" {
		t.Fatalf("payload = %#v, want PatchPayload{Path: op-prefs.md}", payload)
	}
}

func TestClassifyMemoryWriteMalformedArgs(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: contracts.ToolMemoryWrite, Args: []byte("not json")}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
	p, ok := payload.(EscalationPayload)
	if !ok || p.Detail == "" {
		t.Fatalf("payload = %#v, want non-empty EscalationPayload", payload)
	}
}
