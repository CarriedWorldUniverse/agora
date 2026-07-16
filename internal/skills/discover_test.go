package skills_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/skills"
)

func writeSkill(t *testing.T, dir, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\ndescription: %s\n---\nbody\n", description)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover_FixtureTreePrecedenceAndDedup(t *testing.T) {
	tmp := t.TempDir()
	repoRoot := filepath.Join(tmp, "repo")
	userRoot := filepath.Join(tmp, "user-store")
	systemRoot := filepath.Join(tmp, "system-store")

	writeSkill(t, filepath.Join(repoRoot, "alpha"), "repo alpha")
	writeSkill(t, filepath.Join(userRoot, "alpha"), "user alpha") // same name, different dir: NOT a dup
	writeSkill(t, filepath.Join(userRoot, "beta"), "user beta")
	writeSkill(t, filepath.Join(systemRoot, "gamma"), "system gamma")

	roots := []skills.Root{
		{Path: repoRoot, Scope: skills.ScopeRepo, FollowSymlinks: true},
		{Path: userRoot, Scope: skills.ScopeUser, FollowSymlinks: true},
		{Path: systemRoot, Scope: skills.ScopeSystem, FollowSymlinks: false},
	}
	found, warnings := skills.Discover(roots)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(found) != 4 {
		t.Fatalf("got %d skills, want 4: %+v", len(found), found)
	}

	// Render order: System < Admin < Repo < User, then name, then path.
	// Two Repo/User skills share the name "alpha" but differ by dir/path,
	// so both must be present (order beyond scope tier is by path).
	wantScopeOrder := []skills.Scope{skills.ScopeSystem, skills.ScopeRepo, skills.ScopeUser, skills.ScopeUser}
	for i, sk := range found {
		if sk.Scope != wantScopeOrder[i] {
			t.Errorf("found[%d].Scope = %v, want %v (full order: %+v)", i, sk.Scope, wantScopeOrder[i], found)
		}
	}
}

func TestDiscover_DedupByCanonicalPath(t *testing.T) {
	tmp := t.TempDir()
	shared := filepath.Join(tmp, "shared")
	writeSkill(t, filepath.Join(shared, "dup"), "the one")

	// Two roots pointing at the exact same directory tree (e.g. project
	// root == cwd, so the .agents/skills root appears twice in the
	// discovery list) must dedup to a single skill.
	roots := []skills.Root{
		{Path: shared, Scope: skills.ScopeRepo, FollowSymlinks: true},
		{Path: shared, Scope: skills.ScopeUser, FollowSymlinks: true},
	}
	found, _ := skills.Discover(roots)
	if len(found) != 1 {
		t.Fatalf("got %d skills, want 1 (deduped): %+v", len(found), found)
	}
	if found[0].Scope != skills.ScopeRepo {
		t.Errorf("Scope = %v, want ScopeRepo (first-seen wins)", found[0].Scope)
	}
}

func TestDiscover_MissingRootIsEmptyNoError(t *testing.T) {
	roots := []skills.Root{
		{Path: filepath.Join(t.TempDir(), "does-not-exist"), Scope: skills.ScopeUser, FollowSymlinks: true},
	}
	found, warnings := skills.Discover(roots)
	if len(found) != 0 {
		t.Errorf("got %d skills, want 0", len(found))
	}
	if len(warnings) != 0 {
		t.Errorf("got warnings for a missing root, want none: %v", warnings)
	}
}

func TestDiscover_HiddenDirsSkippedBelowRoot(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, ".agents", "skills") // root itself is hidden — fine
	writeSkill(t, filepath.Join(root, ".hidden-skill"), "should be skipped")
	writeSkill(t, filepath.Join(root, "visible-skill"), "should be found")

	found, _ := skills.Discover([]skills.Root{{Path: root, Scope: skills.ScopeRepo, FollowSymlinks: true}})
	if len(found) != 1 {
		t.Fatalf("got %d skills, want 1 (hidden dir skipped): %+v", len(found), found)
	}
	if found[0].Name != "visible-skill" {
		t.Errorf("Name = %q, want visible-skill", found[0].Name)
	}
}

func TestDiscover_DoesNotDescendIntoSkillSubtree(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "skills")
	skillDir := filepath.Join(root, "outer")
	writeSkill(t, skillDir, "outer skill")
	// A nested SKILL.md under scripts/ should NOT be discovered as a
	// second skill: once a SKILL.md is found we stop descending.
	writeSkill(t, filepath.Join(skillDir, "scripts", "nested"), "should not surface")

	found, _ := skills.Discover([]skills.Root{{Path: root, Scope: skills.ScopeRepo, FollowSymlinks: true}})
	if len(found) != 1 {
		t.Fatalf("got %d skills, want 1: %+v", len(found), found)
	}
}

func TestDiscover_DepthGuardStopsDeepTree(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "skills")
	dir := root
	// Nest a skill 8 levels deep — beyond MaxDepth (6).
	for i := 0; i < 8; i++ {
		dir = filepath.Join(dir, fmt.Sprintf("d%d", i))
	}
	writeSkill(t, dir, "too deep")

	found, _ := skills.Discover([]skills.Root{{Path: root, Scope: skills.ScopeRepo, FollowSymlinks: true}})
	if len(found) != 0 {
		t.Fatalf("got %d skills, want 0 (beyond max depth): %+v", len(found), found)
	}
}

func TestDiscover_DepthGuardAllowsWithinLimit(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "skills")
	dir := root
	for i := 0; i < 5; i++ {
		dir = filepath.Join(dir, fmt.Sprintf("d%d", i))
	}
	writeSkill(t, dir, "within depth")

	found, _ := skills.Discover([]skills.Root{{Path: root, Scope: skills.ScopeRepo, FollowSymlinks: true}})
	if len(found) != 1 {
		t.Fatalf("got %d skills, want 1 (within max depth): %+v", len(found), found)
	}
}

func TestDiscover_MaxDirsPerRootGuard(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create more sibling dirs than the guard allows, each one level
	// deep (no SKILL.md), so the walker must stop and warn rather than
	// scan forever.
	for i := 0; i < skills.MaxDirsPerRoot+50; i++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("d%d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, warnings := skills.Discover([]skills.Root{{Path: root, Scope: skills.ScopeRepo, FollowSymlinks: true}})
	if len(warnings) == 0 {
		t.Fatal("expected a max-dirs-per-root warning")
	}
}

func TestDiscover_SymlinksFollowedForUserIgnoredForSystem(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real-skills")
	writeSkill(t, filepath.Join(realDir, "s1"), "via symlink")

	userRoot := filepath.Join(tmp, "user-root")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(userRoot, "linked")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	found, _ := skills.Discover([]skills.Root{{Path: userRoot, Scope: skills.ScopeUser, FollowSymlinks: true}})
	if len(found) != 1 {
		t.Fatalf("User root: got %d skills, want 1 (symlink followed): %+v", len(found), found)
	}

	sysRoot := filepath.Join(tmp, "system-root")
	if err := os.MkdirAll(sysRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, filepath.Join(sysRoot, "linked")); err != nil {
		t.Fatal(err)
	}
	found2, _ := skills.Discover([]skills.Root{{Path: sysRoot, Scope: skills.ScopeSystem, FollowSymlinks: false}})
	if len(found2) != 0 {
		t.Fatalf("System root: got %d skills, want 0 (symlink ignored): %+v", len(found2), found2)
	}
}

func TestFindProjectRoot_WalksUpToMarker(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := skills.FindProjectRoot(sub, nil)
	if got != root {
		t.Errorf("FindProjectRoot = %q, want %q", got, root)
	}
}

func TestFindProjectRoot_FallsBackToStart(t *testing.T) {
	tmp := t.TempDir()
	got := skills.FindProjectRoot(tmp, []string{".no-such-marker"})
	if got != filepath.Clean(tmp) {
		t.Errorf("FindProjectRoot = %q, want %q (fallback)", got, tmp)
	}
}
