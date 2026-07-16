package skills

import (
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
			key := sk.Dir
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
			data, err := os.ReadFile(skillPath)
			if err != nil {
				warnings = append(warnings, Warning{Root: root.Path, Path: skillPath, Message: "discovery: " + err.Error()})
				return
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

			if sidecarData, err := os.ReadFile(filepath.Join(dir, "agents", "openai.yaml")); err == nil {
				sk.Sidecar = ParseSidecar(sidecarData)
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
