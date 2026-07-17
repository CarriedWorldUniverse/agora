package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newFSFamily(t *testing.T) (*FSFamily, Roots) {
	t.Helper()
	roots := newTestRoots(t)
	return NewFSFamily(roots), roots
}

func mustArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return b
}

func TestFSFamilyName(t *testing.T) {
	fam, _ := newFSFamily(t)
	if fam.Name() != "fs" {
		t.Fatalf("Name() = %q", fam.Name())
	}
	for _, n := range []string{ToolReadFile, ToolWriteFile, ToolEditFile, ToolListDir, ToolGlob, ToolGrep} {
		if !fam.Handles(n) {
			t.Errorf("Handles(%q) = false, want true", n)
		}
	}
	if fam.Handles("run_command") {
		t.Error("Handles(run_command) = true, want false")
	}
}

func TestFSFamilySpecsHaveSchemas(t *testing.T) {
	fam, _ := newFSFamily(t)
	specs := fam.Specs()
	if len(specs) != 6 {
		t.Fatalf("got %d specs, want 6", len(specs))
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

func TestReadWriteRoundTrip(t *testing.T) {
	fam, roots := newFSFamily(t)
	ctx := context.Background()

	res, err := fam.Execute(ctx, Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: "greeting.txt", Content: "hello\nworld"})})
	if err != nil || res.IsError {
		t.Fatalf("write_file: err=%v res=%+v", err, res)
	}

	res, err = fam.Execute(ctx, Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: "greeting.txt"})})
	if err != nil || res.IsError {
		t.Fatalf("read_file: err=%v res=%+v", err, res)
	}
	if res.Content != "hello\nworld" {
		t.Fatalf("read_file content = %q", res.Content)
	}

	data, err := os.ReadFile(filepath.Join(roots.WorkingDir, "greeting.txt"))
	if err != nil || string(data) != "hello\nworld" {
		t.Fatalf("on-disk content = %q, err=%v", data, err)
	}
}

func TestReadFileOffsetLimit(t *testing.T) {
	fam, roots := newFSFamily(t)
	ctx := context.Background()
	path := filepath.Join(roots.WorkingDir, "lines.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\ne"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	res, err := fam.Execute(ctx, Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: "lines.txt", Offset: 1, Limit: 2})})
	if err != nil || res.IsError {
		t.Fatalf("read_file: err=%v res=%+v", err, res)
	}
	if res.Content != "b\nc" {
		t.Fatalf("read_file content = %q, want %q", res.Content, "b\nc")
	}
}

func TestWriteFileRejectsPathEscape(t *testing.T) {
	fam, _ := newFSFamily(t)
	res, err := fam.Execute(context.Background(), Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: "../escape.txt", Content: "x"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for path escape")
	}
}

func TestWriteFileRejectsSymlinkEscape(t *testing.T) {
	fam, roots := newFSFamily(t)
	outside := t.TempDir()
	link := filepath.Join(roots.WorkingDir, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	res, err := fam.Execute(context.Background(), Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: "out/x.txt", Content: "x"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for symlink escape")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "x.txt")); statErr == nil {
		t.Fatal("write escaped onto the symlink target on disk")
	}
}

func TestWriteFileRejectsProtectedPath(t *testing.T) {
	fam, roots := newFSFamily(t)
	if err := os.MkdirAll(filepath.Join(roots.WorkingDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	res, err := fam.Execute(context.Background(), Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: ".git/config", Content: "x"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for protected path write")
	}
}

func TestEditFileStalenessGuard(t *testing.T) {
	fam, roots := newFSFamily(t)
	ctx := context.Background()
	path := filepath.Join(roots.WorkingDir, "f.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Edit without a prior read -> ErrNotRead.
	res, err := fam.Execute(ctx, Call{Name: ToolEditFile, Args: mustArgs(t, editFileArgs{Path: "f.txt", OldString: "original", NewString: "changed"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError || res.Content != ErrNotRead.Error() {
		t.Fatalf("expected ErrNotRead, got %+v", res)
	}

	// Read it, then let the on-disk content change externally.
	if _, err := fam.Execute(ctx, Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: "f.txt"})}); err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if err := os.WriteFile(path, []byte("externally modified"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	res, err = fam.Execute(ctx, Call{Name: ToolEditFile, Args: mustArgs(t, editFileArgs{Path: "f.txt", OldString: "externally", NewString: "changed"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError || res.Content != ErrStale.Error() {
		t.Fatalf("expected ErrStale, got %+v", res)
	}

	// Re-read, then a clean edit succeeds.
	if _, err := fam.Execute(ctx, Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: "f.txt"})}); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	res, err = fam.Execute(ctx, Call{Name: ToolEditFile, Args: mustArgs(t, editFileArgs{Path: "f.txt", OldString: "externally modified", NewString: "final"})})
	if err != nil || res.IsError {
		t.Fatalf("edit_file after re-read: err=%v res=%+v", err, res)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "final" {
		t.Fatalf("on-disk content = %q, err=%v", data, err)
	}
}

func TestEditFileNotUnique(t *testing.T) {
	fam, roots := newFSFamily(t)
	ctx := context.Background()
	path := filepath.Join(roots.WorkingDir, "dup.txt")
	if err := os.WriteFile(path, []byte("foo foo"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := fam.Execute(ctx, Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: "dup.txt"})}); err != nil {
		t.Fatalf("read: %v", err)
	}

	res, err := fam.Execute(ctx, Call{Name: ToolEditFile, Args: mustArgs(t, editFileArgs{Path: "dup.txt", OldString: "foo", NewString: "bar"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError || res.Content != ErrOldStringNotUnique.Error() {
		t.Fatalf("expected ErrOldStringNotUnique, got %+v", res)
	}

	res, err = fam.Execute(ctx, Call{Name: ToolEditFile, Args: mustArgs(t, editFileArgs{Path: "dup.txt", OldString: "foo", NewString: "bar", ReplaceAll: true})})
	if err != nil || res.IsError {
		t.Fatalf("replace_all edit: err=%v res=%+v", err, res)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "bar bar" {
		t.Fatalf("on-disk = %q", data)
	}
}

func TestListDirSkipsProtected(t *testing.T) {
	fam, roots := newFSFamily(t)
	if err := os.MkdirAll(filepath.Join(roots.WorkingDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Mkdir(filepath.Join(roots.WorkingDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	res, err := fam.Execute(context.Background(), Call{Name: ToolListDir, Args: mustArgs(t, listDirArgs{Path: "."})})
	if err != nil || res.IsError {
		t.Fatalf("list_dir: err=%v res=%+v", err, res)
	}
	if got := res.Content; got != "a.txt\nsub/" {
		t.Fatalf("list_dir content = %q", got)
	}
}

func TestGlobRecursive(t *testing.T) {
	fam, roots := newFSFamily(t)
	for _, p := range []string{"a.go", "sub/b.go", "sub/deep/c.go", "sub/d.txt"} {
		full := filepath.Join(roots.WorkingDir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	res, err := fam.Execute(context.Background(), Call{Name: ToolGlob, Args: mustArgs(t, globArgs{Pattern: "**/*.go"})})
	if err != nil || res.IsError {
		t.Fatalf("glob: err=%v res=%+v", err, res)
	}
	want := []string{
		filepath.Join(roots.WorkingDir, "a.go"),
		filepath.Join(roots.WorkingDir, "sub/b.go"),
		filepath.Join(roots.WorkingDir, "sub/deep/c.go"),
	}
	got := res.Content
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("glob result missing %q, got:\n%s", w, got)
		}
	}
	if strings.Contains(got, "d.txt") {
		t.Errorf("glob matched a non-.go file: %s", got)
	}
}

func TestGrepFindsMatches(t *testing.T) {
	fam, roots := newFSFamily(t)
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, "f.go"), []byte("package main\n\nfunc TODO() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := fam.Execute(context.Background(), Call{Name: ToolGrep, Args: mustArgs(t, grepArgs{Pattern: "TODO"})})
	if err != nil || res.IsError {
		t.Fatalf("grep: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.Content, "f.go:3:func TODO() {}") {
		t.Fatalf("grep content = %q", res.Content)
	}
}

func TestGrepBadPatternIsErrorResult(t *testing.T) {
	fam, _ := newFSFamily(t)
	res, err := fam.Execute(context.Background(), Call{Name: ToolGrep, Args: mustArgs(t, grepArgs{Pattern: "("})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for invalid regexp")
	}
}

// --- review fix 2: protected dirs readable via read_file/list_dir/grep ---

func seedProtectedGitConfig(t *testing.T, roots Roots) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(roots.WorkingDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, ".git", "config"), []byte("[credential]\n\thelper=secret\n"), 0o644); err != nil {
		t.Fatalf("seed .git/config: %v", err)
	}
}

func TestReadFileRejectsProtectedPath(t *testing.T) {
	fam, roots := newFSFamily(t)
	seedProtectedGitConfig(t, roots)

	res, err := fam.Execute(context.Background(), Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: ".git/config"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError || strings.Contains(res.Content, "secret") {
		t.Fatalf("expected ErrProtectedPath and no leaked content, got %+v", res)
	}

	// A normal in-root path still works.
	if _, err := fam.Execute(context.Background(), Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: "ok.txt", Content: "fine"})}); err != nil {
		t.Fatalf("write ok.txt: %v", err)
	}
	res, err = fam.Execute(context.Background(), Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: "ok.txt"})})
	if err != nil || res.IsError || res.Content != "fine" {
		t.Fatalf("normal read: err=%v res=%+v", err, res)
	}
}

func TestListDirRejectsProtectedPath(t *testing.T) {
	fam, roots := newFSFamily(t)
	seedProtectedGitConfig(t, roots)

	res, err := fam.Execute(context.Background(), Call{Name: ToolListDir, Args: mustArgs(t, listDirArgs{Path: ".git"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected ErrProtectedPath for list_dir(.git)")
	}

	res, err = fam.Execute(context.Background(), Call{Name: ToolListDir, Args: mustArgs(t, listDirArgs{Path: "."})})
	if err != nil || res.IsError {
		t.Fatalf("normal list_dir: err=%v res=%+v", err, res)
	}
}

func TestGrepRejectsProtectedPathRoot(t *testing.T) {
	fam, roots := newFSFamily(t)
	seedProtectedGitConfig(t, roots)
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, "app.go"), []byte("package main\n// helper=public\n"), 0o644); err != nil {
		t.Fatalf("seed app.go: %v", err)
	}

	res, err := fam.Execute(context.Background(), Call{Name: ToolGrep, Args: mustArgs(t, grepArgs{Pattern: "helper", Path: ".git"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError || strings.Contains(res.Content, "secret") {
		t.Fatalf("expected ErrProtectedPath and no leaked content, got %+v", res)
	}

	// grep with no path (searches all writable roots) must still find the
	// legitimate match while silently skipping .git as a descendant (this
	// half already worked; regression guard).
	res, err = fam.Execute(context.Background(), Call{Name: ToolGrep, Args: mustArgs(t, grepArgs{Pattern: "helper"})})
	if err != nil || res.IsError {
		t.Fatalf("grep whole-root: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.Content, "app.go") || strings.Contains(res.Content, "secret") {
		t.Fatalf("grep whole-root content = %q", res.Content)
	}
}

// --- review fix 3: non-atomic write_file staleness under concurrency ---

func TestWriteFileConcurrentNoSilentLoss(t *testing.T) {
	fam, roots := newFSFamily(t)
	path := filepath.Join(roots.WorkingDir, "shared.txt")
	if err := os.WriteFile(path, []byte("v0"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := context.Background()
	if _, err := fam.Execute(ctx, Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: "shared.txt"})}); err != nil {
		t.Fatalf("read: %v", err)
	}

	const n = 20
	results := make([]Result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = fam.Execute(ctx, Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: "shared.txt", Content: fmt.Sprintf("v%d", i+1)})})
		}(i)
	}
	wg.Wait()

	// The brief accepts either resolution: "exactly one wins cleanly and
	// the other gets a clear error (OR both serialize correctly)". What
	// must NEVER happen is the actual defect: an UNSYNCHRONIZED
	// read+checkStale+write sequence lets two goroutines both pass the
	// stale check against the same on-disk snapshot and then both
	// os.WriteFile concurrently — producing either a torn/corrupted file
	// (bytes from two different writes interleaved) or a "successful"
	// response whose content never actually lands on disk. A per-path
	// lock across the whole sequence fixes this by construction: every
	// write_file call that reports success must have fully landed,
	// so the FINAL on-disk content must be byte-for-byte one of the N
	// attempted contents, never a mix/corruption of two.
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	valid := false
	for i := 0; i < n; i++ {
		if string(final) == fmt.Sprintf("v%d", i+1) {
			valid = true
			break
		}
	}
	if !valid {
		t.Fatalf("final content %q is not any single attempted write — torn/corrupted write", final)
	}

	// Every Result claiming success must be internally consistent: no
	// response says "wrote shared.txt" while ALSO claiming a stale/error
	// condition, and there must be at least one clean success (the whole
	// burst didn't just error out).
	successes := 0
	for _, r := range results {
		if !r.IsError {
			successes++
			if r.Content != "wrote shared.txt" {
				t.Errorf("success result had unexpected content: %+v", r)
			}
		}
	}
	if successes == 0 {
		t.Fatal("no writer succeeded at all")
	}
	t.Logf("%d/%d writers succeeded (both all-serialize and exactly-one-wins are acceptable per the brief)", successes, n)
}

// --- review fix 4 (fs half): unbounded file-size caps on read_file/grep ---

func TestReadFileRejectsOversizedFile(t *testing.T) {
	fam, roots := newFSFamily(t)
	path := filepath.Join(roots.WorkingDir, "big.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Truncate(path, fsMaxFileSize+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	res, err := fam.Execute(context.Background(), Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: "big.txt"})})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for an oversized file")
	}
}

func TestGrepSkipsOversizedFile(t *testing.T) {
	fam, roots := newFSFamily(t)
	big := filepath.Join(roots.WorkingDir, "big.txt")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Truncate(big, fsMaxFileSize+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(roots.WorkingDir, "small.txt"), []byte("NEEDLE here"), 0o644); err != nil {
		t.Fatalf("seed small: %v", err)
	}

	res, err := fam.Execute(context.Background(), Call{Name: ToolGrep, Args: mustArgs(t, grepArgs{Pattern: "NEEDLE"})})
	if err != nil || res.IsError {
		t.Fatalf("grep: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.Content, "small.txt") {
		t.Fatalf("expected match in small.txt, got %q", res.Content)
	}
}

// --- review fix 5: glob/grep double-list a nested add_dir root ---

func TestGlobDedupsNestedAddDirRoot(t *testing.T) {
	wd := t.TempDir()
	nested := filepath.Join(wd, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "f.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	roots, err := NewRoots(wd, nested) // nested add_dir, already under wd
	if err != nil {
		t.Fatalf("NewRoots: %v", err)
	}
	fam := NewFSFamily(roots)

	res, err := fam.Execute(context.Background(), Call{Name: ToolGlob, Args: mustArgs(t, globArgs{Pattern: "**/*.go"})})
	if err != nil || res.IsError {
		t.Fatalf("glob: err=%v res=%+v", err, res)
	}
	got := strings.Split(strings.TrimSpace(res.Content), "\n")
	count := 0
	for _, l := range got {
		if strings.HasSuffix(l, "f.go") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("f.go listed %d times, want 1: %q", count, res.Content)
	}
}
