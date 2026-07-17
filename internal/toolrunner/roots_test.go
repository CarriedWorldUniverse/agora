package toolrunner

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func newTestRoots(t *testing.T) Roots {
	t.Helper()
	wd := t.TempDir()
	roots, err := NewRoots(wd)
	if err != nil {
		t.Fatalf("NewRoots: %v", err)
	}
	return roots
}

// absOutsideRoots returns an absolute path outside any t.TempDir-based root,
// using OS-appropriate syntax: a Unix path like "/etc/passwd" is NOT absolute
// on Windows, so filepath.IsAbs treats it as relative and joins it INTO the
// root — making a would-be "outside" fixture land inside. t.TempDir lives
// under the temp dir on every OS, never under C:\Windows.
func absOutsideRoots() string {
	if runtime.GOOS == "windows" {
		return `C:\Windows\System32\drivers\etc\hosts`
	}
	return "/etc/passwd"
}

func TestContainsLexical(t *testing.T) {
	roots := newTestRoots(t)

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"relative inside", "foo/bar.txt", true},
		{"absolute inside", filepath.Join(roots.WorkingDir, "foo.txt"), true},
		{"dotdot escape", "../escape.txt", false},
		{"nested dotdot escape", "foo/../../escape.txt", false},
		{"absolute outside", absOutsideRoots(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roots.ContainsLexical(tc.path); got != tc.want {
				t.Errorf("ContainsLexical(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsProtected(t *testing.T) {
	roots := newTestRoots(t)

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"git dir", ".git/config", true},
		{"nested git", ".git/objects/pack/x.pack", true},
		{"agora dir", ".agora/state.db", true},
		{"cairn dir", ".cairn/objects.git", true},
		{"ordinary file", "src/main.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roots.IsProtected(tc.path); got != tc.want {
				t.Errorf("IsProtected(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestResolveContainedDotDotEscape(t *testing.T) {
	roots := newTestRoots(t)
	if _, err := resolveContained(roots, "../../etc/passwd"); err == nil {
		t.Fatal("expected ErrPathEscape, got nil")
	} else if got := err; got != ErrPathEscape {
		t.Fatalf("expected ErrPathEscape, got %v", got)
	}
}

func TestResolveContainedSymlinkEscape(t *testing.T) {
	wd := t.TempDir()
	outside := t.TempDir()
	roots, err := NewRoots(wd)
	if err != nil {
		t.Fatalf("NewRoots: %v", err)
	}

	// A symlinked directory inside wd pointing outside every root.
	link := filepath.Join(wd, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if _, err := resolveContained(roots, filepath.Join("escape", "file.txt")); err != ErrPathEscape {
		t.Fatalf("expected ErrPathEscape for symlinked dir escape, got %v", err)
	}

	// A symlinked FILE inside wd pointing outside.
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fileLink := filepath.Join(wd, "linkfile")
	if err := os.Symlink(outsideFile, fileLink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := resolveContained(roots, "linkfile"); err != ErrPathEscape {
		t.Fatalf("expected ErrPathEscape for symlinked file escape, got %v", err)
	}
}

func TestResolveContainedInsideRoot(t *testing.T) {
	roots := newTestRoots(t)
	got, err := resolveContained(roots, filepath.Join("sub", "new.txt"))
	if err != nil {
		t.Fatalf("resolveContained: %v", err)
	}
	want := filepath.Join(roots.WorkingDir, "sub", "new.txt")
	if got != want {
		t.Fatalf("resolveContained = %q, want %q", got, want)
	}
}

func TestResolveContainedAddDir(t *testing.T) {
	wd := t.TempDir()
	extra := t.TempDir()
	roots, err := NewRoots(wd, extra)
	if err != nil {
		t.Fatalf("NewRoots: %v", err)
	}
	got, err := resolveContained(roots, filepath.Join(extra, "x.txt"))
	if err != nil {
		t.Fatalf("resolveContained: %v", err)
	}
	// resolveContained returns the symlink-resolved path; on macOS `extra`
	// (a t.TempDir under /var) resolves to /private/var, so compare against
	// the resolved form (identity on Linux, where /tmp isn't a symlink).
	resolvedExtra, err := filepath.EvalSymlinks(extra)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	want := filepath.Join(resolvedExtra, "x.txt")
	if got != want {
		t.Fatalf("resolveContained = %q, want %q", got, want)
	}
}
