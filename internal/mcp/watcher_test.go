package mcp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestSweeper_FirstSweepReportsCreated(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(dir, "a.txt"), "hello", base)

	clock := NewFakeClock(base.Add(time.Second))
	sw := NewSweeper([]string{dir}, clock)
	changes, err := sw.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != "created" {
		t.Fatalf("changes = %+v", changes)
	}
	if !changes[0].At.Equal(base.Add(time.Second)) {
		t.Fatalf("At = %v, want injected clock time", changes[0].At)
	}
}

func TestSweeper_MtimeChangeReportsModified(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "hello", base)

	clock := NewFakeClock(base)
	sw := NewSweeper([]string{dir}, clock)
	if _, err := sw.Sweep(); err != nil {
		t.Fatal(err)
	}

	// Same bytes, but mtime bumped (e.g. a tool rewrote it unchanged) — the
	// SWEEP fallback over-invalidates on mtime alone (documented, §5a).
	writeFile(t, path, "hello", base.Add(time.Hour))
	clock.Advance(time.Hour)
	changes, err := sw.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != "modified" {
		t.Fatalf("expected over-invalidated modified event, got %+v", changes)
	}
}

func TestSweeper_NoChangeNoEvent(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(dir, "a.txt"), "hello", base)

	clock := NewFakeClock(base)
	sw := NewSweeper([]string{dir}, clock)
	if _, err := sw.Sweep(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	changes, err := sw.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes on unchanged sweep, got %+v", changes)
	}
}

func TestSweeper_DeletedFileEvent(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "hello", base)

	clock := NewFakeClock(base)
	sw := NewSweeper([]string{dir}, clock)
	if _, err := sw.Sweep(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	changes, err := sw.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != "deleted" || changes[0].Path != path {
		t.Fatalf("changes = %+v", changes)
	}

	// A further sweep must not re-report the same deletion.
	clock.Advance(time.Minute)
	changes2, err := sw.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes2) != 0 {
		t.Fatalf("expected no repeat delete event, got %+v", changes2)
	}
}

func TestSweeper_IgnoresProtectedDirs(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, ignored := range IgnoredDirs {
		sub := filepath.Join(dir, ignored)
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(sub, "churn"), "x", base)
	}
	writeFile(t, filepath.Join(dir, "real.txt"), "y", base)

	clock := NewFakeClock(base)
	sw := NewSweeper([]string{dir}, clock)
	changes, err := sw.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != filepath.Join(dir, "real.txt") {
		t.Fatalf("expected only the non-ignored file, got %+v", changes)
	}
}

func TestSweeper_SymlinkNotFollowed(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(outside, "secret.txt"), "s", base)

	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	clock := NewFakeClock(base)
	sw := NewSweeper([]string{dir}, clock)
	changes, err := sw.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected symlink not followed/reported, got %+v", changes)
	}
}

func TestSweeper_ContentHashPopulated(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(dir, "a.txt"), "hello", base)

	clock := NewFakeClock(base)
	sw := NewSweeper([]string{dir}, clock)
	changes, err := sw.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].ContentHash == "" {
		t.Fatalf("expected content hash populated, got %+v", changes)
	}
}
