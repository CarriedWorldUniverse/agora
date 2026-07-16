package skills

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Traversal guards. Spec: agora-spec-skills.md §2.
const (
	MaxDepth          = 6
	MaxDirsPerRoot    = 2000
	MaxEntriesPerRoot = 20000
)

// MaxSkillFileBytes caps how many bytes any single SKILL.md / sidecar file
// is read into memory. Reads are size-limited BEFORE buffering (io.LimitReader),
// so a symlink to /dev/zero or an enormous real file cannot OOM the host — the
// injection caps elsewhere (InvocationBodyCapBytes = 8000, description ≤1024)
// mean no legitimate skill file approaches this. Over-cap files are truncated
// with a warning, not silently fully-buffered. Security review (U5), HIGH.
const MaxSkillFileBytes = 1 << 20 // 1 MiB

// Root is one discovery root. Callers build the list explicitly — real
// production roots via DefaultRoots, tests via synthetic temp-dir roots —
// so discovery never touches the real filesystem unless a caller points
// it there. Spec §2 ("Roots INJECTABLE... tests pass synthetic roots,
// never real FS").
type Root struct {
	// Path is the root directory. A missing Path is not an error — it
	// contributes zero skills (§2 "Missing root = empty, no error").
	Path  string
	Scope Scope
	// FollowSymlinks: symlinked dirs are followed for User/Repo/Admin,
	// ignored for System (§2).
	FollowSymlinks bool
}

// Warning is a non-fatal discovery/rendering condition surfaced to the
// caller (§2 "per-skill parse errors surfaced as warnings"; §3.2
// truncation/omission warnings).
type Warning struct {
	Root    string
	Path    string
	Message string
}

// DefaultRoots builds the production root list in discovery-precedence
// order. cwd and home must be absolute, cleaned paths; projectRoot is the
// nearest ancestor of cwd bearing a root marker (FindProjectRoot).
// Spec §2, numbered list.
func DefaultRoots(projectRoot, cwd, home string) []Root {
	var roots []Root
	// 1. Project .agora/skills (Repo).
	roots = append(roots, Root{Path: filepath.Join(projectRoot, ".agora", "skills"), Scope: ScopeRepo, FollowSymlinks: true})
	// 2. Repo .agents/skills at every level root -> cwd (Repo).
	for _, dir := range ancestorChain(projectRoot, cwd) {
		roots = append(roots, Root{Path: filepath.Join(dir, ".agents", "skills"), Scope: ScopeRepo, FollowSymlinks: true})
	}
	// 3. User stores + Claude Code compat.
	roots = append(roots,
		Root{Path: filepath.Join(home, ".agora", "skills"), Scope: ScopeUser, FollowSymlinks: true},
		Root{Path: filepath.Join(home, ".agents", "skills"), Scope: ScopeUser, FollowSymlinks: true},
		Root{Path: filepath.Join(projectRoot, ".claude", "skills"), Scope: ScopeRepo, FollowSymlinks: true},
		Root{Path: filepath.Join(home, ".claude", "skills"), Scope: ScopeUser, FollowSymlinks: true},
	)
	// 4. System (bundled), symlinks NOT followed.
	roots = append(roots, Root{Path: filepath.Join(home, ".agora", "skills", ".system"), Scope: ScopeSystem, FollowSymlinks: false})
	// 5. Admin, optional.
	roots = append(roots, Root{Path: filepath.Join("/etc", "agora", "skills"), Scope: ScopeAdmin, FollowSymlinks: true})
	return roots
}

// ancestorChain returns the directories from root to cwd inclusive,
// root-first. If cwd is not under root, it returns just root then cwd
// (best-effort — matches AGENTS.md's collection order, §6).
func ancestorChain(root, cwd string) []string {
	root = filepath.Clean(root)
	cwd = filepath.Clean(cwd)
	if root == cwd {
		return []string{root}
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		return []string{root, cwd}
	}
	parts := strings.Split(rel, string(filepath.Separator))
	dirs := []string{root}
	cur := root
	for _, p := range parts {
		if p == "." || p == "" {
			continue
		}
		cur = filepath.Join(cur, p)
		dirs = append(dirs, cur)
	}
	return dirs
}

// FindProjectRoot walks up from start looking for a directory containing
// one of markers (default [".git"] when markers is empty). Falls back to
// start itself if no marker is found.
// Spec: agora-spec-subagents.md §6.
func FindProjectRoot(start string, markers []string) string {
	if len(markers) == 0 {
		markers = []string{".git"}
	}
	dir := filepath.Clean(start)
	for {
		for _, m := range markers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.Clean(start)
}

// Discover walks roots in order (highest discovery-precedence first),
// dedups by canonical skill directory path (first-seen wins, which is
// the highest-precedence occurrence since roots are already ordered),
// and returns the merged, catalog-ready skill list plus any warnings.
// Spec: agora-spec-skills.md §2.
func Discover(roots []Root) ([]*Skill, []Warning) {
	var all []*Skill
	var warnings []Warning
	seen := map[string]bool{}

	for _, root := range roots {
		found, warns := scanRoot(root)
		warnings = append(warnings, warns...)
		for _, sk := range found {
			key := canonicalDir(sk.Dir)
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, sk)
		}
	}

	sort.SliceStable(all, func(i, j int) bool {
		ri, rj := renderRank(all[i].Scope), renderRank(all[j].Scope)
		if ri != rj {
			return ri < rj
		}
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].Path < all[j].Path
	})
	return all, warnings
}

// canonicalDir resolves symlinks in a directory path for use as a dedup key,
// so a skill reachable via a symlinked directory alias in a second root is
// counted once (spec §2 "deduped by canonicalized path"; review finding F3).
// Falls back to a lexical clean when the path can't be resolved.
func canonicalDir(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	return filepath.Clean(dir)
}

// pathWithinRoot reports whether resolved (an already symlink-resolved path)
// stays inside root. Used as the containment check for a symlinked skill/
// sidecar file so it cannot point outside its discovery root.
func pathWithinRoot(resolved, root string) bool {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		rootReal = filepath.Clean(root)
	}
	rel, err := filepath.Rel(rootReal, resolved)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// safeReadUnder reads a file that must live under root, enforcing the symlink
// trust boundary and a hard size cap BEFORE buffering:
//   - If the file itself is a symlink, it is only followed when the root
//     permits it (followSymlinks) AND its resolved target stays under root —
//     otherwise the read is refused. This closes the confused-deputy hole
//     where a symlinked SKILL.md/openai.yaml pointed at an arbitrary host file
//     (e.g. ~/.ssh/id_rsa), bypassing the FollowSymlinks flag entirely
//     (review finding S1). A regular file reached through an already-vetted
//     symlinked *directory* is unaffected (only the final component is checked).
//   - At most cap bytes are read via io.LimitReader; a larger file is
//     truncated (truncated=true) rather than fully loaded, so a symlink to
//     /dev/zero or a huge real file cannot OOM the host (review finding S2).
func safeReadUnder(path string, root string, followSymlinks bool, capBytes int) (data []byte, truncated bool, err error) {
	li, err := os.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	if li.Mode()&os.ModeSymlink != 0 {
		if !followSymlinks {
			return nil, false, fmt.Errorf("refusing symlinked %s (symlinks disabled for this root)", filepath.Base(path))
		}
		resolved, rerr := filepath.EvalSymlinks(path)
		if rerr != nil {
			return nil, false, fmt.Errorf("refusing unresolvable symlink %s: %w", filepath.Base(path), rerr)
		}
		if !pathWithinRoot(resolved, root) {
			return nil, false, fmt.Errorf("refusing symlinked %s escaping discovery root", filepath.Base(path))
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, int64(capBytes)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > capBytes {
		return data[:capBytes], true, nil
	}
	return data, false, nil
}

// scanRoot walks one root applying the depth/dir/entry guards and
// returns every directory containing a SKILL.md as a parsed Skill.
func scanRoot(root Root) ([]*Skill, []Warning) {
	var skills []*Skill
	var warnings []Warning

	info, err := os.Stat(root.Path)
	if err != nil || !info.IsDir() {
		return nil, nil // missing root = empty, no error (§2)
	}

	dirCount := 0
	entryCount := 0
	guardHit := false

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if guardHit {
			return
		}
		dirCount++
		if dirCount > MaxDirsPerRoot {
			guardHit = true
			warnings = append(warnings, Warning{Root: root.Path, Message: "discovery: max dirs per root exceeded, truncated"})
			return
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			warnings = append(warnings, Warning{Root: root.Path, Path: dir, Message: "discovery: " + err.Error()})
			return
		}
		entryCount += len(entries)
		if entryCount > MaxEntriesPerRoot {
			guardHit = true
			warnings = append(warnings, Warning{Root: root.Path, Message: "discovery: max entries per root exceeded, truncated"})
			return
		}

		hasSkillMD := false
		for _, e := range entries {
			if !e.IsDir() && e.Name() == "SKILL.md" {
				hasSkillMD = true
				break
			}
		}
		if hasSkillMD {
			skillPath := filepath.Join(dir, "SKILL.md")
			data, truncated, err := safeReadUnder(skillPath, root.Path, root.FollowSymlinks, MaxSkillFileBytes)
			if err != nil {
				warnings = append(warnings, Warning{Root: root.Path, Path: skillPath, Message: "discovery: " + err.Error()})
				return
			}
			if truncated {
				warnings = append(warnings, Warning{Root: root.Path, Path: skillPath, Message: fmt.Sprintf("discovery: SKILL.md too large, truncated to %d bytes", MaxSkillFileBytes)})
			}
			sk, err := ParseSkillMD(data, filepath.Base(dir))
			if err != nil {
				warnings = append(warnings, Warning{Root: root.Path, Path: skillPath, Message: "discovery: " + err.Error()})
				return
			}
			sk.Dir = dir
			sk.Path = skillPath
			sk.Scope = root.Scope
			sk.RootPath = root.Path

			// Sidecar is optional and never blocks the skill; a rejected
			// (symlink-escaping / oversized) sidecar is surfaced as a warning
			// but simply leaves the zero-value Sidecar (§1.2).
			sidecarPath := filepath.Join(dir, "agents", "openai.yaml")
			if sidecarData, _, serr := safeReadUnder(sidecarPath, root.Path, root.FollowSymlinks, MaxSkillFileBytes); serr == nil {
				sk.Sidecar = ParseSidecar(sidecarData)
			} else if !os.IsNotExist(serr) {
				warnings = append(warnings, Warning{Root: root.Path, Path: sidecarPath, Message: "discovery: sidecar skipped: " + serr.Error()})
			}
			skills = append(skills, sk)
			return // don't descend into a skill's own subtree
		}

		if depth >= MaxDepth {
			return
		}
		for _, e := range entries {
			if guardHit {
				return
			}
			name := e.Name()
			isDir := e.IsDir()
			childPath := filepath.Join(dir, name)

			if e.Type()&os.ModeSymlink != 0 {
				if !root.FollowSymlinks {
					continue
				}
				st, err := os.Stat(childPath)
				if err != nil || !st.IsDir() {
					continue
				}
				isDir = true
			}
			if !isDir {
				continue
			}
			// Hidden dirs skipped BELOW the root (the root itself may be
			// hidden — §2).
			if strings.HasPrefix(name, ".") {
				continue
			}
			walk(childPath, depth+1)
		}
	}

	walk(root.Path, 0)
	return skills, warnings
}
