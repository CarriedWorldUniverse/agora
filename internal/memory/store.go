package memory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// indexFilename is the injected-catalog source file. Spec §1.
const indexFilename = indexBasename + ".md"

// MaxMemoryFileBytes caps a single memory file read (and, via the rendered
// file, a Write). It bounds two review findings at once: a symlink planted in
// the memory dir cannot stream an unbounded external file into the process, and
// — since rebuildIndexLocked re-reads EVERY entry on every mutation — one
// oversized entry cannot inflate the cost of every subsequent write. Review
// (U13): security MED. No legitimate memory approaches this (a memory is a
// short note).
const MaxMemoryFileBytes = 1 << 20 // 1 MiB

// readMemoryFile reads a memory file with the containment a store dir that is
// "operator-readable/writable like any notes dir" needs: it REFUSES a
// non-regular file (a symlink planted in the dir must not exfiltrate an
// external file into a Read or the auto-injected index — the analogous NEX-750
// skills hardening; memory.write never creates a symlink but the dir is
// writable by other tools/the operator), and caps the read. Security review (U13).
func readMemoryFile(path string) ([]byte, error) {
	li, err := os.Lstat(path)
	if err != nil {
		return nil, err // preserves os.IsNotExist for a missing entry
	}
	if !li.Mode().IsRegular() {
		return nil, fmt.Errorf("memory: %s is not a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxMemoryFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxMemoryFileBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, filepath.Base(path), MaxMemoryFileBytes)
	}
	return data, nil
}

// DefaultDir returns the production per-identity memory dir: an identity is
// scoped by NAME (not fingerprint) "for human editability — the dir is
// operator-readable/writable like any notes dir" (§1). home must be an
// absolute path (the caller's resolved user home); identityName is not
// validated here (validateSlug at the file level is what matters — this is
// just a path join, mirroring skills.DefaultRoots's injected-root style).
func DefaultDir(home, identityName string) string {
	return filepath.Join(home, ".agora", "memory", identityName)
}

// Store is a file-backed, identity-scoped memory store rooted at dir:
// dir/MEMORY.md is the index, dir/<slug>.md are the individual memories.
// Spec: agora-spec-memory.md §1, §3.
//
// Store serializes its own Write/Delete (and the index rebuild they
// trigger) with an in-process mutex, and persists both an entry file and
// the rebuilt index via temp-write-then-rename so a reader (this process or
// another) never observes a partially-written file — the "fixing the
// read-before-edit race shadow's harness suffers" (§3). The mutex covers
// the in-process race (proven by TestConcurrentWritesIndexRace under
// -race); cross-process concurrent writers to the same dir are outside the
// single-owner threat model this store targets (same acceptance as
// persistence's TOCTOU note).
type Store struct {
	dir string
	mu  sync.RWMutex
}

// NewStore opens (creating if absent) a Store rooted at dir. dir is
// injected by the caller — production code points it at DefaultDir(...),
// tests point it at a temp dir — so Store never touches the real
// filesystem's home dir unless a caller tells it to (mirrors
// skills.Root's injected-roots discipline).
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("memory: empty store dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("memory: create store dir: %w", err)
	}
	// Sweep any temp files a prior process left behind by crashing between
	// CreateTemp and Rename (scanLocked already ignores non-.md files, so
	// these never corrupt the index, but they'd accumulate). Review (U13) LOW.
	if leftovers, gerr := filepath.Glob(filepath.Join(dir, ".tmp-*")); gerr == nil {
		for _, l := range leftovers {
			_ = os.Remove(l)
		}
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store's root directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) entryPath(slug string) string {
	return filepath.Join(s.dir, slug+".md")
}

// Read implements memory.read(name). Spec §3.
func (s *Store) Read(name string) (Entry, error) {
	if err := validateSlug(name); err != nil {
		return Entry{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := readMemoryFile(s.entryPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return Entry{}, fmt.Errorf("memory: read %s: %w", name, err)
	}
	fm, body, err := parseFrontmatter(data)
	if err != nil {
		return Entry{}, fmt.Errorf("memory: parse %s: %w", name, err)
	}
	return Entry{Slug: name, Frontmatter: fm, Body: body}, nil
}

// Write implements memory.write(name, frontmatter, body): writes
// dir/<name>.md and atomically rebuilds MEMORY.md so the index is never
// observed stale relative to a completed write. Spec §1, §3.
func (s *Store) Write(name string, fm Frontmatter, body string) error {
	if err := validateSlug(name); err != nil {
		return err
	}
	if fm.Name == "" {
		return ErrEmptyName
	}
	if !fm.Type.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidType, fm.Type)
	}

	data, err := renderEntryFile(fm, body)
	if err != nil {
		return err
	}
	// Cap the rendered file so it round-trips through the capped reader and so
	// one oversized entry can't inflate every future index rebuild (U13 review).
	if len(data) > MaxMemoryFileBytes {
		return fmt.Errorf("%w: memory %q is %d bytes", ErrTooLarge, name, len(data))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := atomicWriteFile(s.entryPath(name), data); err != nil {
		return fmt.Errorf("memory: write %s: %w", name, err)
	}
	return s.rebuildIndexLocked()
}

// Delete implements memory.delete(name). Spec §3.
func (s *Store) Delete(name string) error {
	if err := validateSlug(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.entryPath(name)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return fmt.Errorf("memory: stat %s: %w", name, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("memory: delete %s: %w", name, err)
	}
	return s.rebuildIndexLocked()
}

// List implements memory.list(): every memory in the store, newest-first
// (the same order the injected index uses, §2). Spec §3.
func (s *Store) List() ([]IndexEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scanLocked()
}

// scanLocked derives the current index entries by scanning dir for
// `<slug>.md` files (excluding MEMORY.md itself), parsing each one's
// frontmatter. A file that fails to parse as a memory (foreign or corrupt
// .md content) is silently excluded from the index rather than failing the
// whole scan — corruption of one entry must not hide every other memory
// (deviation note: spec doesn't say either way; this is the
// simplest-spec-consistent reading, matching persistence's "corruption is
// an inconvenience, never data loss" posture for the analogous case).
// Sorted newest-first by mtime, slug ascending as a deterministic tiebreak
// (spec §2 "newest-first survives"; ground rule: deterministic output).
func (s *Store) scanLocked() ([]IndexEntry, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("memory: read store dir: %w", err)
	}
	var out []IndexEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Case-fold the index-file exclusion: on a case-insensitive FS a
		// case-variant of MEMORY.md IS the index file (U13 review).
		if !strings.HasSuffix(name, ".md") || strings.EqualFold(name, indexFilename) {
			continue
		}
		slug := strings.TrimSuffix(name, ".md")
		if validateSlug(slug) != nil {
			continue
		}
		path := filepath.Join(s.dir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		// readMemoryFile rejects a symlinked/oversized entry, so it can't
		// exfiltrate an external file into the index (U13 review).
		data, err := readMemoryFile(path)
		if err != nil {
			continue
		}
		fm, _, err := parseFrontmatter(data)
		if err != nil {
			continue
		}
		out = append(out, IndexEntry{
			Slug:    slug,
			Title:   fm.Name,
			Hook:    fm.Description,
			Type:    fm.Type,
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].ModTime.After(out[j].ModTime) // newest first
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

// rebuildIndexLocked regenerates MEMORY.md from a fresh directory scan and
// writes it atomically. Called with s.mu already held (Write/Delete). This
// full-rebuild-on-every-mutation design is what makes the index update
// atomic AND race-free under -race: there is no read-modify-write of the
// existing MEMORY.md content to race on, only a derive-from-disk +
// atomic-replace (§3: "fixing the read-before-edit race").
func (s *Store) rebuildIndexLocked() error {
	entries, err := s.scanLocked()
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(s.dir, indexFilename), []byte(renderIndexFile(entries)))
}

// atomicWriteFile writes data to a temp file in path's directory, fsyncs
// it, and renames it over path — so a concurrent reader of path always
// sees either the old complete content or the new complete content, never
// a partial write (mirrors persistence's createThreadFile durability
// discipline; os.Rename is atomic same-directory on Linux/macOS/Windows,
// satisfying the cross-platform ground rule).
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("memory: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below fails before the rename.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("memory: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("memory: fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("memory: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("memory: rename into place: %w", err)
	}
	success = true
	return nil
}
