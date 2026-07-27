package toolrunner

import (
	"os"
	"path/filepath"
	"strings"
)

// ProtectedDirs are never model-writable even when lexically inside a
// writable root (agora-spec-io.md §3a: ".git" (approval for destructive
// ops), ".agora/", ".cairn/" (the cairn VCS store)). Kept as this package's
// own list (mirrors internal/mcp.IgnoredDirs, agora-spec-mcp.md §5a) rather
// than importing internal/mcp for a three-string constant.
var ProtectedDirs = []string{".git", ".agora", ".cairn"}

// Roots is the session's writable-root set: the working dir plus any
// declared add_dir roots (agora-spec-io.md §3a). v1 resolution note (spec
// ambiguity, resolved): §3a's default execution policy is "write limited to
// wd (+add_dirs), read allowed everywhere"; this unit's brief hard-requires
// "every path resolves inside the configured writable roots" for the fs
// family without carving out a separate read-root set, so v1 contains BOTH
// reads and writes to WorkingDir+AddDirs. A future phase can widen reads
// per §3a once a distinct read-root/credential-exclusion list exists (§3a
// also forbids reading the identity key store/credentials, which a
// read-everywhere policy would have to carve out explicitly).
type Roots struct {
	WorkingDir string
	AddDirs    []string
	// TempDirs is the scratch/tmp half of §3a's writable set. It is
	// populated automatically by NewRoots, not declared by the caller.
	//
	// Temp dirs count for CONTAINMENT (All/ContainingRoot/ContainsLexical
	// and so resolveContained) but are deliberately excluded from
	// SearchRoots, the set a bare glob/grep walks. Walking a shared /tmp
	// would be slow and would surface unrelated processes' files into the
	// model's context; a scratch dir is somewhere to WRITE, not somewhere
	// to search by default. An explicit `grep path=/tmp/...` still works,
	// because that goes through containment, not the walk set.
	TempDirs []string
}

// NewRoots canonicalizes workingDir/addDirs (abs + symlink-resolved, per the
// house convention in internal/mcp/watcher.go that roots are real
// directories, not symlinks) so later containment checks compare resolved
// paths on both sides.
func NewRoots(workingDir string, addDirs ...string) (Roots, error) {
	wd, err := canonDir(workingDir)
	if err != nil {
		return Roots{}, err
	}
	var dirs []string
	for _, d := range addDirs {
		cd, err := canonDir(d)
		if err != nil {
			return Roots{}, err
		}
		dirs = append(dirs, cd)
	}
	return Roots{WorkingDir: wd, AddDirs: dirs, TempDirs: tempDirs()}, nil
}

// tempDirs returns the scratch roots: the process's temp dir (TMPDIR, or
// the platform default) plus "/tmp" when that is a different, real
// directory — macOS resolves TMPDIR to a per-user /var/folders path while
// tools still write to /tmp, and a model asked to scribble a scratch file
// reaches for /tmp by habit either way.
//
// A temp dir that cannot be resolved is SKIPPED rather than failing
// NewRoots: WorkingDir and AddDirs are caller-declared, so a bad one is a
// caller bug worth erroring on, but an absent /tmp must not stop a session
// from starting.
func tempDirs() []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range []string{os.TempDir(), "/tmp"} {
		if d == "" {
			continue
		}
		resolved, err := canonDir(d)
		if err != nil || seen[resolved] {
			continue
		}
		seen[resolved] = true
		out = append(out, resolved)
	}
	return out
}

func canonDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// All returns every root that counts for CONTAINMENT — working dir first,
// then add_dirs, then temp dirs. This is the set a path must fall inside
// to be readable/writable at all. For the narrower set a bare glob/grep
// walks, use SearchRoots.
func (r Roots) All() []string {
	out := make([]string, 0, 1+len(r.AddDirs)+len(r.TempDirs))
	out = append(out, r.WorkingDir)
	out = append(out, r.AddDirs...)
	out = append(out, r.TempDirs...)
	return out
}

// SearchRoots is the set a bare glob/grep WALKS: the working dir and
// add_dirs, with any root dropped that is lexically nested under an
// earlier one (review fix 5: a walk over WorkingDir already descends into
// a nested add_dir, so a naive per-root filepath.WalkDir double-lists
// every file under it and pre-trips grepMaxMatches). Earlier entries win —
// WorkingDir is listed first, so a nested add_dir is the one dropped.
//
// TempDirs are excluded on purpose — see the field's comment on Roots.
// This is NOT the containment set; that is All().
func (r Roots) SearchRoots() []string {
	all := make([]string, 0, 1+len(r.AddDirs))
	all = append(all, r.WorkingDir)
	all = append(all, r.AddDirs...)
	out := make([]string, 0, len(all))
	for _, root := range all {
		nested := false
		for _, kept := range out {
			if under(root, kept) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, root)
		}
	}
	return out
}

// under reports whether p equals root or is lexically nested under it.
// Both p and root are assumed already filepath.Clean'd absolute paths.
func under(p, root string) bool {
	if p == root {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(p, strings.TrimSuffix(root, sep)+sep)
}

// ContainingRoot returns the root p (a Clean'd absolute path) resolves
// under, if any.
func (r Roots) ContainingRoot(p string) (string, bool) {
	for _, root := range r.All() {
		if under(p, root) {
			return root, true
		}
	}
	return "", false
}

// ContainsLexical is the pure, disk-free membership check: resolves path
// relative to WorkingDir if not already absolute, filepath.Cleans it (which
// collapses ".." segments), and checks the result is under some root. It
// does NOT resolve symlinks — the classifier (classify.go) uses this to
// build its payload cheaply; fs.go's execution-time resolveContained does
// the additional symlink-aware check that makes containment a hard
// boundary rather than a lexical one.
func (r Roots) ContainsLexical(path string) bool {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(r.WorkingDir, abs)
	}
	clean := filepath.Clean(abs)
	_, ok := r.ContainingRoot(clean)
	return ok
}

// IsProtected reports whether path has a ProtectedDirs segment anywhere in
// it (checked on the lexically-resolved path, same resolution
// ContainsLexical uses) — a write two levels under ".git" is just as
// protected as one directly inside it.
func (r Roots) IsProtected(path string) bool {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(r.WorkingDir, abs)
	}
	clean := filepath.Clean(abs)
	for _, seg := range strings.Split(clean, string(filepath.Separator)) {
		for _, p := range ProtectedDirs {
			if seg == p {
				return true
			}
		}
	}
	return false
}

// resolveContained is the fs family's hard, symlink-aware containment
// check: resolves path (relative paths against roots.WorkingDir), verifies
// the lexical result is under a root, then walks up from the longest
// EXISTING ancestor, symlink-resolves it, and re-checks containment on the
// resolved ancestor plus a symlink-resolved check on the final target
// itself if it already exists — so neither a ".." escape nor a symlink
// pointing out of a root can produce a path outside the writable roots
// (agora-spec-mcp.md §5a "Bound": "never follows symlinks out of the
// sandbox root").
func resolveContained(roots Roots, path string) (string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(roots.WorkingDir, abs)
	}
	clean := filepath.Clean(abs)
	// Containment is judged AFTER symlink resolution below, NOT on the raw
	// `clean` here: a premature lexical check against the (symlink-resolved)
	// roots wrongly rejects a valid absolute path given in UNRESOLVED form
	// when a root's ancestor is a symlink (macOS /var -> /private/var, /tmp ->
	// /private/tmp). filepath.Clean has already collapsed any ".." segments,
	// and the resolved-ancestor check below still catches every escape
	// (../.. , symlinked-dir, and symlinked-file — see roots_test.go).

	// Find the longest existing ancestor directory of clean.
	dir := clean
	var rest []string
	for {
		if _, err := os.Lstat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding an existing ancestor.
			break
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
		dir = parent
	}

	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	resolvedDir = filepath.Clean(resolvedDir)
	if _, ok := roots.ContainingRoot(resolvedDir); !ok {
		return "", ErrPathEscape
	}

	full := resolvedDir
	for _, seg := range rest {
		full = filepath.Join(full, seg)
	}

	// If the final target itself already exists and is a symlink, its
	// resolved destination must also stay inside a root.
	if fi, err := os.Lstat(full); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(full)
		if err != nil {
			return "", err
		}
		if _, ok := roots.ContainingRoot(filepath.Clean(target)); !ok {
			return "", ErrPathEscape
		}
	}

	return full, nil
}
