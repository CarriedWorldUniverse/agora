package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMemJournalStore_SaveRead_RoundTrip(t *testing.T) {
	s := NewMemJournalStore()
	if got, err := s.Read("nope"); err != nil || got != nil {
		t.Fatalf("Read of never-saved run = %v, %v; want nil, nil", got, err)
	}
	want := []Entry{
		{Seq: 0, Branch: "", LocalSeq: 0, Kind: EntryAgent, Hash: "h0", Result: json.RawMessage(`"r0"`)},
		{Seq: 1, Branch: "", LocalSeq: 1, Kind: EntryLog, Message: "hi"},
	}
	if err := s.Save("r1", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Read("r1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read = %+v; want %+v", got, want)
	}

	// Save mutates the caller's slice defensively (copy-on-write) — mutating
	// the original after Save must not affect what's stored.
	want[0].Hash = "mutated"
	got2, err := s.Read("r1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got2[0].Hash != "h0" {
		t.Fatalf("Read after external mutation = %q; want the store's own copy unaffected (h0)", got2[0].Hash)
	}
}

func TestFileJournalStore_SaveRead_RoundTripAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	s := NewFileJournalStore(dir)

	if got, err := s.Read("nope"); err != nil || got != nil {
		t.Fatalf("Read of never-saved run = %v, %v; want nil, nil", got, err)
	}

	entries := []Entry{
		{Seq: 0, Branch: "", LocalSeq: 0, Kind: EntryAgent, Hash: "h0", Result: json.RawMessage(`"r0"`)},
		{Seq: 1, Branch: "/p0.0", LocalSeq: 0, Kind: EntryQuestion, Hash: "h1", QuestionID: "q1", Args: json.RawMessage(`{"text":"?"}`)},
	}
	if err := s.Save("r2", entries); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(dir, "r2", "journal.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Fatalf("journal.jsonl should be LF-terminated, got: %q", b)
	}

	got, err := s.Read("r2")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, entries) {
		t.Fatalf("Read = %+v; want %+v", got, entries)
	}

	// Save again with fewer entries must fully replace the file, not append.
	shorter := entries[:1]
	if err := s.Save("r2", shorter); err != nil {
		t.Fatalf("Save (overwrite): %v", err)
	}
	got2, err := s.Read("r2")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got2) != 1 {
		t.Fatalf("Read after overwrite = %d entries; want 1 (overwrite, not append)", len(got2))
	}
}

func TestIndexEntries_KeyedByBranchLocalSeqKind(t *testing.T) {
	entries := []Entry{
		{Branch: "", LocalSeq: 0, Kind: EntryAgent, Hash: "a"},
		{Branch: "", LocalSeq: 0, Kind: EntryQuestion, Hash: "b"},
		{Branch: "/p0.0", LocalSeq: 0, Kind: EntryAgent, Hash: "c"},
	}
	idx := indexEntries(entries)
	if len(idx) != 3 {
		t.Fatalf("index has %d entries; want 3 (same Branch+LocalSeq but different Kind must not collide)", len(idx))
	}
	if e := idx[key{Branch: "", LocalSeq: 0, Kind: EntryAgent}]; e.Hash != "a" {
		t.Fatalf("agent entry hash = %q; want a", e.Hash)
	}
	if e := idx[key{Branch: "", LocalSeq: 0, Kind: EntryQuestion}]; e.Hash != "b" {
		t.Fatalf("question entry hash = %q; want b", e.Hash)
	}
}
