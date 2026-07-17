package skills_test

// NEX-750: trust-scoped directory-symlink containment. A parent-directory
// symlink must not let a Repo-scope root (an untrusted clone) escape its
// containment boundary to read an arbitrary host file, while User/Admin roots
// (a trusted machine) still roam and a within-project (monorepo) symlink store
// still works.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/skills"
)

// The core NEX-750 repro: a Repo root with a directory symlink escaping the
// containment boundary (here the root itself, since ContainWithin is unset)
// must NOT discover skills under the escaped target. Regular files reached
// through the escaping dir symlink were previously read unchecked (only the
// final SKILL.md component was symlink-checked).
func TestScanRoot_RepoParentDirSymlinkEscapeRejected(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside")
	writeSkill(t, filepath.Join(outside, "evil"), "DATA-EXFILTRATED")
	root := filepath.Join(tmp, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	found, _ := skills.Discover([]skills.Root{{Path: root, Scope: skills.ScopeRepo, FollowSymlinks: true}})
	for _, sk := range found {
		if strings.Contains(sk.Description, "DATA-EXFILTRATED") {
			t.Fatalf("Repo dir-symlink escape read a skill from outside the containment boundary")
		}
	}
}

// A Repo root whose ContainWithin is the PROJECT root must still discover a
// symlinked store that resolves WITHIN the project but outside the narrow
// skills root — the monorepo shared-skills pattern the spec preserves.
func TestScanRoot_RepoWithinProjectSymlinkAllowed(t *testing.T) {
	proj := t.TempDir()
	writeSkill(t, filepath.Join(proj, "shared", "myskill"), "shared skill")
	skillsRoot := filepath.Join(proj, ".agents", "skills")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(proj, "shared"), filepath.Join(skillsRoot, "shared")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	found, _ := skills.Discover([]skills.Root{{Path: skillsRoot, Scope: skills.ScopeRepo, FollowSymlinks: true, ContainWithin: proj}})
	for _, sk := range found {
		if sk.Description == "shared skill" {
			return
		}
	}
	t.Fatalf("Repo within-project symlink store was not discovered (monorepo shared skills broken): %+v", found)
}

// A User-scope root roams (trusted machine): a symlinked store escaping the
// root IS discovered — containment applies to Repo scope only.
func TestScanRoot_UserScopeSymlinkRoams(t *testing.T) {
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside")
	writeSkill(t, filepath.Join(outside, "roamer"), "user roams")
	root := filepath.Join(tmp, "userroot")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	found, _ := skills.Discover([]skills.Root{{Path: root, Scope: skills.ScopeUser, FollowSymlinks: true}})
	for _, sk := range found {
		if sk.Description == "user roams" {
			return
		}
	}
	t.Fatalf("User-scope symlinked store should roam (be discovered): %+v", found)
}

// AGENTS.md collection has the same parent-dir-symlink surface: a directory in
// the ancestor chain that symlinks outside the project root must not have its
// AGENTS.md read into the prompt.
func TestDiscoverAGENTSMD_ParentDirSymlinkEscapeRejected(t *testing.T) {
	tmp := t.TempDir()
	secret := filepath.Join(tmp, "secret")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secret, "AGENTS.md"), []byte("SECRET-AGENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".git"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(proj, "sub")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	docs := skills.DiscoverAGENTSMD(filepath.Join(proj, "sub"), nil, nil, 32*1024)
	for _, d := range docs.Docs {
		if strings.Contains(d.Content, "SECRET-AGENTS") {
			t.Fatalf("AGENTS.md via a symlinked ancestor escaping the project was read into the prompt")
		}
	}
}
