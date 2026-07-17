package toolrunner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
