package memory

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentWritesIndexRace drives many goroutines writing distinct
// memories at the same Store concurrently. Run with `go test -race`: the
// index rebuild (read-current-dir-state, write-temp, rename) must never
// data-race, and — functionally — every entry must survive into the final
// MEMORY.md with no corruption or loss (spec §3: "fixing the
// read-before-edit race shadow's harness suffers").
func TestConcurrentWritesIndexRace(t *testing.T) {
	s := newTestStore(t)
	const n = 64

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			slug := fmt.Sprintf("slug_%03d", i)
			fm := Frontmatter{Name: fmt.Sprintf("Title %d", i), Description: "hook", Type: TypeUser}
			if err := s.Write(slug, fm, fmt.Sprintf("body %d", i)); err != nil {
				t.Errorf("Write(%s): %v", slug, err)
			}
		}()
	}
	wg.Wait()

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != n {
		t.Fatalf("List after %d concurrent writes = %d entries, want %d", n, len(entries), n)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.Slug] {
			t.Fatalf("duplicate index entry for %s — index corrupted", e.Slug)
		}
		seen[e.Slug] = true
	}
	for i := 0; i < n; i++ {
		slug := fmt.Sprintf("slug_%03d", i)
		if !seen[slug] {
			t.Errorf("missing index entry for %s — lost under concurrent write", slug)
		}
		entry, err := s.Read(slug)
		if err != nil {
			t.Errorf("Read(%s) after concurrent write: %v", slug, err)
			continue
		}
		if entry.Body != fmt.Sprintf("body %d", i) {
			t.Errorf("Read(%s).Body = %q, want %q", slug, entry.Body, fmt.Sprintf("body %d", i))
		}
	}
}

// TestConcurrentWriteAndDeleteIndexRace mixes writes and deletes across
// goroutines to exercise both index-rebuild paths racing together.
func TestConcurrentWriteAndDeleteIndexRace(t *testing.T) {
	s := newTestStore(t)
	const n = 32

	// Seed entries that the delete goroutines will remove.
	for i := 0; i < n; i++ {
		slug := fmt.Sprintf("del_%03d", i)
		if err := s.Write(slug, Frontmatter{Name: "d", Description: "h", Type: TypeUser}, "b"); err != nil {
			t.Fatalf("seed Write(%s): %v", slug, err)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Delete(fmt.Sprintf("del_%03d", i)); err != nil {
				t.Errorf("Delete(del_%03d): %v", i, err)
			}
		}()
	}
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			slug := fmt.Sprintf("keep_%03d", i)
			if err := s.Write(slug, Frontmatter{Name: "k", Description: "h", Type: TypeUser}, "b"); err != nil {
				t.Errorf("Write(%s): %v", slug, err)
			}
		}()
	}
	wg.Wait()

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != n {
		t.Fatalf("List after mixed concurrent write/delete = %d entries, want %d (only the keep_* set)", len(entries), n)
	}
	for _, e := range entries {
		if e.Slug[:5] != "keep_" {
			t.Errorf("unexpected surviving entry %s, want only keep_*", e.Slug)
		}
	}
}
