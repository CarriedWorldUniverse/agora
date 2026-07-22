package toolrunner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func newMemoryFamily(t *testing.T) (*MemoryFamily, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "identity")
	return NewMemoryFamily(dir), dir
}

func TestMemoryFamilyName(t *testing.T) {
	fam, _ := newMemoryFamily(t)
	if fam.Name() != contracts.FamilyMemory {
		t.Fatalf("Name() = %q, want %q", fam.Name(), contracts.FamilyMemory)
	}
	for _, n := range []string{contracts.ToolMemoryRead, contracts.ToolMemoryWrite, contracts.ToolMemoryList, contracts.ToolMemoryDelete} {
		if !fam.Handles(n) {
			t.Errorf("Handles(%q) = false, want true", n)
		}
	}
	if fam.Handles(ToolReadFile) {
		t.Error("Handles(read_file) = true, want false")
	}
}

func TestMemoryFamilySpecsHaveSchemas(t *testing.T) {
	fam, _ := newMemoryFamily(t)
	specs := fam.Specs()
	if len(specs) != 4 {
		t.Fatalf("got %d specs, want 4", len(specs))
	}
	for _, s := range specs {
		if len(s.InputSchema) == 0 {
			t.Errorf("spec %q has empty InputSchema", s.Name)
		}
		if !json.Valid(s.InputSchema) {
			t.Errorf("spec %q InputSchema is not valid JSON", s.Name)
		}
	}
}

// TestMemoryFamily_ConstructionDoesNotTouchDisk: NewMemoryFamily itself
// (and Name/Handles/Specs) must never create dir — only an actual
// read/write/list/delete call may (via Store.openStore), matching
// turnengine's composeMemoryIndexFragment posture of never creating the
// dir as a prompt-assembly side effect.
func TestMemoryFamily_ConstructionDoesNotTouchDisk(t *testing.T) {
	fam, dir := newMemoryFamily(t)
	_ = fam.Specs()
	fam.Handles(contracts.ToolMemoryRead)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir %s exists after construction-only calls; want absent (err=%v)", dir, err)
	}
}

func writeArgs(t *testing.T, name, fmName, fmDesc, fmType, body string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"name": name,
		"frontmatter": map[string]string{
			"name":        fmName,
			"description": fmDesc,
			"type":        fmType,
		},
		"body": body,
	})
	if err != nil {
		t.Fatalf("marshal write args: %v", err)
	}
	return b
}

// TestMemoryFamily_WriteThenReadRoundTrip: memory.write persists a file
// under dir AND rebuilds MEMORY.md; memory.read returns the saved content
// (frontmatter + body) as the tool result.
func TestMemoryFamily_WriteThenReadRoundTrip(t *testing.T) {
	fam, dir := newMemoryFamily(t)

	writeRes, err := fam.Execute(context.Background(), Call{
		Name: contracts.ToolMemoryWrite,
		Args: writeArgs(t, "op-prefs", "Operator preferences", "how the operator likes things done", "user", "prefers terse commit messages"),
	})
	if err != nil {
		t.Fatalf("Execute(memory.write) error: %v", err)
	}
	if writeRes.IsError {
		t.Fatalf("Execute(memory.write) IsError, content=%q", writeRes.Content)
	}

	// The entry file landed on disk.
	entryPath := filepath.Join(dir, "op-prefs.md")
	if _, err := os.Stat(entryPath); err != nil {
		t.Fatalf("stat %s: %v (write did not persist to disk)", entryPath, err)
	}

	// The index was atomically rebuilt with a line for the new entry.
	idxData, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	if got := string(idxData); got == "" || !strings.Contains(got, "op-prefs.md") || !strings.Contains(got, "Operator preferences") {
		t.Fatalf("MEMORY.md content = %q; want an index line for op-prefs", got)
	}

	readRes, err := fam.Execute(context.Background(), Call{
		Name: contracts.ToolMemoryRead,
		Args: json.RawMessage(`{"name":"op-prefs"}`),
	})
	if err != nil {
		t.Fatalf("Execute(memory.read) error: %v", err)
	}
	if readRes.IsError {
		t.Fatalf("Execute(memory.read) IsError, content=%q", readRes.Content)
	}
	if !strings.Contains(readRes.Content, "prefers terse commit messages") {
		t.Fatalf("memory.read Content = %q; want the saved body", readRes.Content)
	}
	if !strings.Contains(readRes.Content, "Operator preferences") {
		t.Fatalf("memory.read Content = %q; want the saved frontmatter name", readRes.Content)
	}
}

func TestMemoryFamily_ListAndDelete(t *testing.T) {
	fam, _ := newMemoryFamily(t)

	if _, err := fam.Execute(context.Background(), Call{
		Name: contracts.ToolMemoryWrite,
		Args: writeArgs(t, "a", "A", "hook a", "reference", "body a"),
	}); err != nil {
		t.Fatalf("Execute(memory.write a) error: %v", err)
	}

	listRes, err := fam.Execute(context.Background(), Call{Name: contracts.ToolMemoryList, Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Execute(memory.list) error: %v", err)
	}
	if listRes.IsError || !strings.Contains(listRes.Content, "a") {
		t.Fatalf("memory.list Content = %q, IsError=%v", listRes.Content, listRes.IsError)
	}

	delRes, err := fam.Execute(context.Background(), Call{Name: contracts.ToolMemoryDelete, Args: json.RawMessage(`{"name":"a"}`)})
	if err != nil {
		t.Fatalf("Execute(memory.delete) error: %v", err)
	}
	if delRes.IsError {
		t.Fatalf("Execute(memory.delete) IsError, content=%q", delRes.Content)
	}

	readRes, err := fam.Execute(context.Background(), Call{Name: contracts.ToolMemoryRead, Args: json.RawMessage(`{"name":"a"}`)})
	if err != nil {
		t.Fatalf("Execute(memory.read after delete) error: %v", err)
	}
	if !readRes.IsError {
		t.Fatal("Execute(memory.read after delete) should error, memory was deleted")
	}
}
