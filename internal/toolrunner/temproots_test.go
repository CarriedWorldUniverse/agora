package toolrunner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// §3a: "write limited to the working dir (+ scratch/tmp + declared
// add_dirs)". Scratch was in the policy but never in Roots, so every
// scratch write failed ErrPathEscape.
func TestTempRoots_TempIsWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix temp-dir layout")
	}
	fam, _ := newFSFamily(t)
	target := filepath.Join(os.TempDir(), "agora-temproot-write.txt")
	t.Cleanup(func() { os.Remove(target) })

	res, err := fam.Execute(context.Background(), Call{
		Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: target, Content: "scratch"}),
	})
	if err != nil || res.IsError {
		t.Fatalf("write to the temp root: err=%v res=%+v — scratch must be writable per §3a", err, res)
	}
	if b, _ := os.ReadFile(target); string(b) != "scratch" {
		t.Fatalf("temp file content = %q", b)
	}
}

// A scratch write must not demand approval — that is the point of having
// tmp in the writable set rather than relying on escalation.
func TestTempRoots_TempWriteDoesNotEscalate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix temp-dir layout")
	}
	roots := newTestRoots(t)
	kind, payload := Classify(Call{
		Name: ToolWriteFile,
		Args: mustArgs(t, writeFileArgs{Path: filepath.Join(os.TempDir(), "scratch.txt"), Content: "x"}),
	}, roots)
	if kind == contracts.KindEscalation {
		t.Fatalf("a scratch write escalated: %#v", payload)
	}
}

// The whole reason containment and search are separate sets: a bare
// glob/grep must not walk a shared /tmp. Doing so is slow and drags
// unrelated processes' files into the model's context.
func TestTempRoots_ExcludedFromTheSearchSet(t *testing.T) {
	roots := newTestRoots(t)
	if len(roots.TempDirs) == 0 {
		t.Skip("no temp roots resolved on this platform")
	}
	for _, sr := range roots.SearchRoots() {
		for _, td := range roots.TempDirs {
			if sr == td {
				t.Fatalf("SearchRoots() includes the temp root %q — a bare glob/grep would walk all of it", td)
			}
		}
	}
	// ...but they ARE in the containment set.
	for _, td := range roots.TempDirs {
		if _, ok := roots.ContainingRoot(td); !ok {
			t.Errorf("temp root %q is not in the containment set", td)
		}
	}
}

// An explicit grep/glob under a temp path still works — it goes through
// containment, not the walk set. Otherwise excluding temp from the search
// set would have made scratch dirs unsearchable entirely.
func TestTempRoots_ExplicitTempPathIsStillSearchable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix temp-dir layout")
	}
	fam, _ := newFSFamily(t)
	dir, err := os.MkdirTemp("", "agora-grep-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "hay.txt"), []byte("NEEDLE here\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	res, err := fam.Execute(context.Background(), Call{
		Name: ToolGrep, Args: mustArgs(t, grepArgs{Pattern: "NEEDLE", Path: dir}),
	})
	if err != nil || res.IsError {
		t.Fatalf("explicit grep under temp: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.Content, "NEEDLE") {
		t.Fatalf("grep under an explicit temp path found nothing: %q", res.Content)
	}
}

// Adding temp roots must NOT widen the sandbox anywhere else. A genuinely
// outside path is still rejected with production-shaped roots.
func TestTempRoots_RealEscapesStillRejected(t *testing.T) {
	roots := newTestRoots(t) // production shape: temp roots present
	if _, err := resolveContained(roots, absOutsideRoots()); err != ErrPathEscape {
		t.Fatalf("resolveContained(%q) = %v; want ErrPathEscape", absOutsideRoots(), err)
	}
	kind, _ := Classify(Call{
		Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: absOutsideRoots(), Content: "x"}),
	}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("write to %q classified as %v; want KindEscalation", absOutsideRoots(), kind)
	}
}

// A symlink planted in a WRITABLE temp root pointing outside every root
// must still be refused — /tmp is world-writable, so this is the realistic
// attack the symlink check exists for.
func TestTempRoots_SymlinkOutOfTempStillEscapes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix symlink semantics")
	}
	roots := newTestRoots(t)
	dir, err := os.MkdirTemp("", "agora-symlink-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	link := filepath.Join(dir, "escape")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := resolveContained(roots, filepath.Join(link, "passwd")); err != ErrPathEscape {
		t.Fatalf("a symlink from a temp root to /etc resolved to %v; want ErrPathEscape", err)
	}
}

// Protected dirs stay protected inside a temp root — a .git or .cairn
// store under /tmp is no more agent-writable than one in the working dir.
func TestTempRoots_ProtectedDirsStillProtectedUnderTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix temp-dir layout")
	}
	roots := newTestRoots(t)
	for _, name := range ProtectedDirs {
		p := filepath.Join(os.TempDir(), "repo", name, "config")
		if !roots.IsProtected(p) {
			t.Errorf("IsProtected(%q) = false; %s under a temp root must stay protected", p, name)
		}
	}
}
