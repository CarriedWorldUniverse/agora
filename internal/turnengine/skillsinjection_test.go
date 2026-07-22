package turnengine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtureSkill writes a minimal valid SKILL.md at dir/SKILL.md — mirrors
// internal/skills' own writeSkill test helper (unexported there, so this
// package needs its own copy for integration-level fixtures).
func writeFixtureSkill(t *testing.T, dir, name, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\nbody\n", name, description)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// isolateSkillsEnv chdirs into a fresh temp working dir and points HOME at a
// fresh temp dir too, so skills/AGENTS.md discovery never picks up the real
// operator machine's ~/.claude or ~/.agora content (hermeticity — without
// this, tests in this file would be sensitive to whatever the CI/dev box
// happens to have under $HOME). Returns the working dir.
func isolateSkillsEnv(t *testing.T) string {
	t.Helper()
	wd := t.TempDir()
	home := t.TempDir()
	t.Chdir(wd)
	t.Setenv("HOME", home)
	// Windows resolves os.UserHomeDir from USERPROFILE, not HOME —
	// without this the isolation silently fails on Windows CI.
	t.Setenv("USERPROFILE", home)
	return wd
}

// TestComposeSkillsAndAgentsFragments_CatalogAndAGENTSMDInjected covers the
// GOAL: a fixture .agents/skills root (two SKILL.md files) plus a fixture
// AGENTS.md in the working dir both reach the composed prompt, in the §1a
// role order (skills catalog = developer role, before AGENTS.md = user
// role — agora-spec-prompt.md §1a).
func TestComposeSkillsAndAgentsFragments_CatalogAndAGENTSMDInjected(t *testing.T) {
	wd := isolateSkillsEnv(t)

	writeFixtureSkill(t, filepath.Join(wd, ".agents", "skills", "alpha"), "alpha", "the alpha skill does alpha things")
	writeFixtureSkill(t, filepath.Join(wd, ".agents", "skills", "beta"), "beta", "the beta skill does beta things")

	if err := os.WriteFile(filepath.Join(wd, "AGENTS.md"), []byte("PROJECT-CONTEXT-MARKER: build with make"), 0o644); err != nil {
		t.Fatal(err)
	}

	const model = "claude-sonnet-5"
	got := composeDevSystemPrompt(model)

	if !strings.Contains(got, "<skills_instructions>") {
		t.Fatalf("composed prompt missing the skills catalog wrapper:\n%s", got)
	}
	if !strings.Contains(got, "alpha: the alpha skill does alpha things") {
		t.Fatalf("composed prompt missing the alpha catalog entry:\n%s", got)
	}
	if !strings.Contains(got, "beta: the beta skill does beta things") {
		t.Fatalf("composed prompt missing the beta catalog entry:\n%s", got)
	}
	if !strings.Contains(got, "PROJECT-CONTEXT-MARKER: build with make") {
		t.Fatalf("composed prompt missing the AGENTS.md content:\n%s", got)
	}
	if !strings.Contains(got, "AGENTS.md instructions for") {
		t.Fatalf("composed prompt missing the AGENTS.md fragment wrapper:\n%s", got)
	}

	idxCatalog := strings.Index(got, "<skills_instructions>")
	idxAgents := strings.Index(got, "AGENTS.md instructions for")
	if idxCatalog < 0 || idxAgents < 0 || idxCatalog > idxAgents {
		t.Fatalf("skills catalog (developer role) must precede AGENTS.md (user role) per §1a: catalog=%d agents=%d\n%s", idxCatalog, idxAgents, got)
	}

	// Both fragments must land after the §1 segments (core/profile/environment).
	idxProfile := strings.Index(got, devSystemPrompt)
	if idxProfile < 0 || idxProfile > idxCatalog {
		t.Fatalf("skills catalog must come after the composed §1 segments: profile=%d catalog=%d\n%s", idxProfile, idxCatalog, got)
	}
}

// TestComposeDevSystemPrompt_NoFixturesByteIdenticalToBaseline pins the
// zero-change default (agora-spec-skills.md §2 "Missing root = empty, no
// error"; this unit's SCOPE item 3): with no skills dirs and no AGENTS.md,
// composeDevSystemPrompt's output must be byte-identical to
// composeBaseSystemPrompt's segments-only output — nothing is appended.
func TestComposeDevSystemPrompt_NoFixturesByteIdenticalToBaseline(t *testing.T) {
	wd := isolateSkillsEnv(t)

	const model = "claude-sonnet-5"
	want, err := composeBaseSystemPrompt(model, wd)
	if err != nil {
		t.Fatalf("composeBaseSystemPrompt: %v", err)
	}

	got := composeDevSystemPrompt(model)
	if got != want {
		t.Fatalf("composeDevSystemPrompt with no skills/AGENTS.md fixtures diverged from the segments-only baseline:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

// TestComposeSkillsAndAgentsFragments_SymlinkEscapeNotDiscovered mirrors
// internal/skills' own NEX-750 containment tests (discover.go) at this
// package's integration seam: a Repo-scope skill reached only via a
// directory symlink escaping the project root must not appear in the
// composed prompt's catalog.
func TestComposeSkillsAndAgentsFragments_SymlinkEscapeNotDiscovered(t *testing.T) {
	wd := isolateSkillsEnv(t)

	// A real skill inside the project, so the catalog is non-empty (and
	// therefore actually rendered) while the escaped one must be absent from
	// it.
	writeFixtureSkill(t, filepath.Join(wd, ".agents", "skills", "legit"), "legit", "a legitimately discoverable skill")

	outside := t.TempDir()
	writeFixtureSkill(t, filepath.Join(outside, "evil"), "evil", "DATA-EXFILTRATED-VIA-SYMLINK")

	skillsRoot := filepath.Join(wd, ".agents", "skills")
	if err := os.Symlink(outside, filepath.Join(skillsRoot, "escape")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	got := composeSkillsAndAgentsFragments(wd)
	if !strings.Contains(got, "legit: a legitimately discoverable skill") {
		t.Fatalf("expected the legitimate in-project skill to be discovered:\n%s", got)
	}
	if strings.Contains(got, "DATA-EXFILTRATED-VIA-SYMLINK") {
		t.Fatalf("a skill reached only via a project-root-escaping symlink was injected into the prompt:\n%s", got)
	}
}

// TestComposeDevSystemPrompt_FixturesStableAcrossCalls is the fixture-
// present counterpart to promptassembly_test.go's cache-stability pin
// (NEX-793): composeDevSystemPrompt must be byte-stable across repeated
// calls with the same wd/HOME even once skills/AGENTS.md discovery is
// wired in — a per-call churn here would bust the claudesdk/anthropic
// prompt cache exactly like a per-turn "date" recompute would.
func TestComposeDevSystemPrompt_FixturesStableAcrossCalls(t *testing.T) {
	wd := isolateSkillsEnv(t)

	writeFixtureSkill(t, filepath.Join(wd, ".agents", "skills", "alpha"), "alpha", "the alpha skill")
	if err := os.WriteFile(filepath.Join(wd, "AGENTS.md"), []byte("stable project context"), 0o644); err != nil {
		t.Fatal(err)
	}

	const model = "claude-sonnet-5"
	a := composeDevSystemPrompt(model)
	b := composeDevSystemPrompt(model)
	if a != b {
		t.Fatalf("composeDevSystemPrompt is not stable across calls with fixtures present:\nfirst:\n%s\n\nsecond:\n%s", a, b)
	}
}
