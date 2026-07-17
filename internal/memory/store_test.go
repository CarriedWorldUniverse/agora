package memory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestWriteReadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	fm := Frontmatter{Name: "Operator identity", Description: "who jacinta is", Type: TypeUser}
	if err := s.Write("operator_identity", fm, "jacinta runs shadow.\n"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := s.Read("operator_identity")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Slug != "operator_identity" {
		t.Errorf("Slug = %q, want operator_identity", got.Slug)
	}
	if got.Frontmatter != fm {
		t.Errorf("Frontmatter = %+v, want %+v", got.Frontmatter, fm)
	}
	if got.Body != "jacinta runs shadow.\n" {
		t.Errorf("Body = %q", got.Body)
	}
}

func TestReadMissingReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Read("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read(missing) err = %v, want ErrNotFound", err)
	}
}

func TestDeleteMissingReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(missing) err = %v, want ErrNotFound", err)
	}
}

func TestDeleteRemovesEntryAndIndexLine(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "keep", "Keep me", "stays", TypeReference, time.Time{})
	mustWrite(t, s, "gone", "Delete me", "goes", TypeReference, time.Time{})

	if err := s.Delete("gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Read("gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read after Delete err = %v, want ErrNotFound", err)
	}

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Slug != "keep" {
		t.Fatalf("List after Delete = %+v, want just 'keep'", entries)
	}

	indexPath := filepath.Join(s.Dir(), "MEMORY.md")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	if got := string(data); !contains(got, "keep.md") || contains(got, "gone.md") {
		t.Fatalf("MEMORY.md after Delete = %q, want to contain keep.md and not gone.md", got)
	}
}

func TestWriteRejectsInvalidNameFrontmatter(t *testing.T) {
	s := newTestStore(t)
	validFM := Frontmatter{Name: "x", Description: "y", Type: TypeUser}

	cases := map[string]struct {
		name string
		fm   Frontmatter
		want error
	}{
		"empty name":       {name: "", fm: validFM, want: ErrInvalidName},
		"path traversal":   {name: "../escape", fm: validFM, want: ErrInvalidName},
		"nested separator": {name: "a/b", fm: validFM, want: ErrInvalidName},
		"reserved MEMORY":  {name: "MEMORY", fm: validFM, want: ErrReservedName},
		"empty fm name":    {name: "ok", fm: Frontmatter{Name: "", Description: "y", Type: TypeUser}, want: ErrEmptyName},
		"bad type":         {name: "ok2", fm: Frontmatter{Name: "x", Description: "y", Type: "bogus"}, want: ErrInvalidType},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.Write(tc.name, tc.fm, "body")
			if !errors.Is(err, tc.want) {
				t.Fatalf("Write(%q) err = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

func TestWriteOverwritesExistingEntry(t *testing.T) {
	s := newTestStore(t)
	fm1 := Frontmatter{Name: "V1", Description: "first", Type: TypeUser}
	if err := s.Write("slug", fm1, "v1 body"); err != nil {
		t.Fatalf("Write v1: %v", err)
	}
	fm2 := Frontmatter{Name: "V2", Description: "second", Type: TypeFeedback}
	if err := s.Write("slug", fm2, "v2 body"); err != nil {
		t.Fatalf("Write v2: %v", err)
	}

	got, err := s.Read("slug")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Frontmatter != fm2 || got.Body != "v2 body" {
		t.Fatalf("Read after overwrite = %+v, want fm2/v2 body", got)
	}

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("List len = %d, want 1 (overwrite must not duplicate the index line)", len(entries))
	}
}

func TestListNewestFirstOrder(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	mustWrite(t, s, "oldest", "Oldest", "h1", TypeReference, base)
	mustWrite(t, s, "middle", "Middle", "h2", TypeReference, base.Add(time.Hour))
	mustWrite(t, s, "newest", "Newest", "h3", TypeReference, base.Add(2*time.Hour))

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("List len = %d, want 3", len(entries))
	}
	want := []string{"newest", "middle", "oldest"}
	for i, w := range want {
		if entries[i].Slug != w {
			t.Fatalf("entries[%d].Slug = %q, want %q (full: %+v)", i, entries[i].Slug, w, entries)
		}
	}
}

func TestListDeterministicTiebreakOnEqualModTime(t *testing.T) {
	s := newTestStore(t)
	same := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	mustWrite(t, s, "bravo", "Bravo", "h", TypeReference, same)
	mustWrite(t, s, "alpha", "Alpha", "h", TypeReference, same)

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 || entries[0].Slug != "alpha" || entries[1].Slug != "bravo" {
		t.Fatalf("List with equal ModTime = %+v, want [alpha bravo] (slug ascending tiebreak)", entries)
	}
}

func TestScanSkipsUnparsableMDFiles(t *testing.T) {
	s := newTestStore(t)
	mustWrite(t, s, "good", "Good", "h", TypeReference, time.Time{})
	if err := os.WriteFile(filepath.Join(s.Dir(), "foreign.md"), []byte("not a memory file\n"), 0o644); err != nil {
		t.Fatalf("write foreign.md: %v", err)
	}

	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Slug != "good" {
		t.Fatalf("List with a foreign .md present = %+v, want just 'good'", entries)
	}
}

func TestReadWriteDeleteRejectInvalidSlug(t *testing.T) {
	s := newTestStore(t)
	for _, bad := range []string{"", "..", "../x", "a/b", "MEMORY"} {
		if _, err := s.Read(bad); err == nil {
			t.Errorf("Read(%q) err = nil, want error", bad)
		}
		if err := s.Delete(bad); err == nil {
			t.Errorf("Delete(%q) err = nil, want error", bad)
		}
	}
}

func TestNewStoreCreatesDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "nested", "identity")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if info, err := os.Stat(s.Dir()); err != nil || !info.IsDir() {
		t.Fatalf("store dir not created: %v", err)
	}
}

func TestDefaultDir(t *testing.T) {
	got := DefaultDir("/home/jacinta", "shadow")
	want := filepath.Join("/home/jacinta", ".agora", "memory", "shadow")
	if got != want {
		t.Fatalf("DefaultDir = %q, want %q", got, want)
	}
}

// mustWrite writes a memory and, when at is non-zero, pins the file's
// mtime with os.Chtimes so index-ordering tests are deterministic without
// depending on wall-clock write timing (ground rule: no wall-clock in
// tests).
func mustWrite(t *testing.T, s *Store, slug, title, hook string, typ Type, at time.Time) {
	t.Helper()
	if err := s.Write(slug, Frontmatter{Name: title, Description: hook, Type: typ}, "body of "+slug); err != nil {
		t.Fatalf("Write(%s): %v", slug, err)
	}
	if !at.IsZero() {
		path := filepath.Join(s.Dir(), slug+".md")
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatalf("Chtimes(%s): %v", slug, err)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
