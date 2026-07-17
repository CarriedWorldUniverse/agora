package memory

// Regression tests for the U13 review gate (security-validator + DeepSeek-v4-pro).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// HIGH (security) — a symlink planted in the memory dir must not exfiltrate an
// external file into a Read or the auto-injected index (the analogous NEX-750
// skills hardening). Read/scanLocked must reject non-regular files.
func TestMemory_RejectsSymlinkedEntry(t *testing.T) {
	tmp := t.TempDir()
	secret := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(secret, []byte("---\nname: fake\ndescription: exfil\ntype: reference\n---\nTOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "mem")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "evil.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if e, err := s.Read("evil"); err == nil && strings.Contains(e.Body, "TOP SECRET") {
		t.Fatalf("Read followed a symlink and returned an external file's content")
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, ie := range list {
		if ie.Slug == "evil" {
			t.Fatalf("List indexed a symlinked entry (exfil into the injected index)")
		}
	}
}

// MED (security) — a memory body larger than the cap must be rejected on Write
// (the full-rebuild scan re-reads every entry on each mutation, so one giant
// file inflates all writes).
func TestMemory_WriteRejectsOversizedBody(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", MaxMemoryFileBytes+1024)
	if err := s.Write("big", Frontmatter{Name: "Big", Type: TypeReference}, big); err == nil {
		t.Fatal("Write accepted an oversized body, want a size-cap error")
	}
}

// MED (both) — the reserved index name must be rejected case-insensitively
// (memory/Memory == MEMORY on a case-insensitive FS).
func TestMemory_ReservedNameCaseInsensitive(t *testing.T) {
	for _, n := range []string{"memory", "Memory", "MeMoRy"} {
		if err := validateSlug(n); err == nil {
			t.Fatalf("validateSlug(%q) accepted a case-variant of the reserved index name", n)
		}
	}
}

// LOW (DeepSeek) — a BOM-prefixed memory file must still parse.
func TestMemory_ParsesBOMPrefixedFile(t *testing.T) {
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("---\nname: T\ndescription: d\ntype: user\n---\nbody")...)
	fm, body, err := parseFrontmatter(data)
	if err != nil {
		t.Fatalf("BOM-prefixed file failed to parse: %v", err)
	}
	if fm.Name != "T" || strings.TrimSpace(body) != "body" {
		t.Fatalf("BOM parse wrong: fm=%+v body=%q", fm, body)
	}
}

// LOW (DeepSeek) — a CRLF-authored file's body must not keep a leading \r.
func TestMemory_CRLFBodyNoLeadingCR(t *testing.T) {
	data := []byte("---\r\nname: T\r\ndescription: d\r\ntype: user\r\n---\r\nbody line\r\n")
	_, body, err := parseFrontmatter(data)
	if err != nil {
		t.Fatalf("CRLF file failed to parse: %v", err)
	}
	if strings.HasPrefix(body, "\r") || strings.HasPrefix(body, "\n") {
		t.Fatalf("CRLF body kept a leading CR/LF artifact: %q", body)
	}
}
