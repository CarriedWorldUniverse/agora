package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDef drops one agent-def markdown file into dir/name.md.
func writeDef(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

const goodDef = `---
name: reviewer
description: reviews code for correctness
tools: read_file, grep
model: sonnet
---
You review code.
`

func TestDiscoverAgentDefs_ReadsProjectAndUserRoots(t *testing.T) {
	proj, home := t.TempDir(), t.TempDir()
	writeDef(t, filepath.Join(proj, ".agora", "agents"), "reviewer", goodDef)
	writeDef(t, filepath.Join(home, ".agora", "agents"), "builder", strings.Replace(goodDef, "reviewer", "builder", 1))

	defs, warns := DiscoverAgentDefs(DefaultAgentRoots(proj, home))
	if len(warns) != 0 {
		t.Fatalf("warnings = %v; want none", warns)
	}
	got := map[string]bool{}
	for _, d := range defs {
		got[d.Name] = true
	}
	if !got["reviewer"] || !got["builder"] {
		t.Fatalf("defs = %v; want both reviewer and builder", got)
	}
}

// The precedence contract: project scope wins over user scope on a name
// collision, because DefaultAgentRoots orders project roots first and
// DiscoverAgentDefs is first-seen-wins.
func TestDiscoverAgentDefs_ProjectBeatsUserOnNameCollision(t *testing.T) {
	proj, home := t.TempDir(), t.TempDir()
	writeDef(t, filepath.Join(proj, ".agora", "agents"), "reviewer",
		strings.Replace(goodDef, "You review code.", "PROJECT VERSION", 1))
	writeDef(t, filepath.Join(home, ".agora", "agents"), "reviewer",
		strings.Replace(goodDef, "You review code.", "USER VERSION", 1))

	defs, _ := DiscoverAgentDefs(DefaultAgentRoots(proj, home))

	var reviewers int
	var prompt string
	for _, d := range defs {
		if d.Name == "reviewer" {
			reviewers++
			prompt = d.Prompt
		}
	}
	if reviewers != 1 {
		t.Fatalf("reviewer appeared %d times; want exactly 1 (deduped by name)", reviewers)
	}
	if prompt != "PROJECT VERSION" {
		t.Fatalf("prompt = %q; want the PROJECT VERSION (project scope must win)", prompt)
	}
}

// .agora beats .claude within the same scope — the compat lane must not
// shadow a project's own definition.
func TestDiscoverAgentDefs_AgoraBeatsClaudeCompat(t *testing.T) {
	proj, home := t.TempDir(), t.TempDir()
	writeDef(t, filepath.Join(proj, ".agora", "agents"), "reviewer",
		strings.Replace(goodDef, "You review code.", "AGORA", 1))
	writeDef(t, filepath.Join(proj, ".claude", "agents"), "reviewer",
		strings.Replace(goodDef, "You review code.", "CLAUDE", 1))

	defs, _ := DiscoverAgentDefs(DefaultAgentRoots(proj, home))
	for _, d := range defs {
		if d.Name == "reviewer" && d.Prompt != "AGORA" {
			t.Fatalf("prompt = %q; want AGORA (.agora must beat .claude compat)", d.Prompt)
		}
	}
}

// A typo'd def must not stop the harness starting: it warns and the other
// defs in the same directory still load.
func TestDiscoverAgentDefs_MalformedDefWarnsButDoesNotBlockOthers(t *testing.T) {
	proj, home := t.TempDir(), t.TempDir()
	dir := filepath.Join(proj, ".agora", "agents")
	writeDef(t, dir, "good", goodDef)
	writeDef(t, dir, "bad", "---\nname: bad\n---\nno description, so this fails to parse\n")

	defs, warns := DiscoverAgentDefs(DefaultAgentRoots(proj, home))
	if len(warns) != 1 {
		t.Fatalf("warnings = %v; want exactly 1 (the malformed def)", warns)
	}
	if !strings.Contains(warns[0].Path, "bad.md") {
		t.Fatalf("warning path = %q; want it to name bad.md", warns[0].Path)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	if len(names) != 1 || names[0] != "reviewer" {
		t.Fatalf("defs = %v; want just the good one to survive", names)
	}
}

// Missing directories are the common case (most projects have no
// .agora/agents) — silence, not warnings.
func TestDiscoverAgentDefs_MissingDirsAreSilent(t *testing.T) {
	defs, warns := DiscoverAgentDefs(DefaultAgentRoots(t.TempDir(), t.TempDir()))
	if len(defs) != 0 || len(warns) != 0 {
		t.Fatalf("defs=%v warns=%v; want both empty for a project with no agent dirs", defs, warns)
	}
}

// Non-.md files are ignored — a README or a stray .yaml in the directory
// is not an agent def.
func TestDiscoverAgentDefs_IgnoresNonMarkdown(t *testing.T) {
	proj, home := t.TempDir(), t.TempDir()
	dir := filepath.Join(proj, ".agora", "agents")
	writeDef(t, dir, "reviewer", goodDef)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a def"), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, warns := DiscoverAgentDefs(DefaultAgentRoots(proj, home))
	if len(defs) != 1 || len(warns) != 0 {
		t.Fatalf("defs=%d warns=%v; want exactly 1 def and no warnings", len(defs), warns)
	}
}

// The whole point of the unit: discovered defs reach the Registry, and a
// discovered def overrides a builtin of the same name.
func TestDiscoverAgentDefs_FeedsRegistryAndOverridesBuiltin(t *testing.T) {
	proj, home := t.TempDir(), t.TempDir()
	writeDef(t, filepath.Join(proj, ".agora", "agents"), "reviewer", goodDef)
	writeDef(t, filepath.Join(proj, ".agora", "agents"), BuiltinExplore,
		strings.Replace(goodDef, "name: reviewer", "name: "+BuiltinExplore, 1))

	defs, _ := DiscoverAgentDefs(DefaultAgentRoots(proj, home))
	reg := NewRegistry(defs)

	if _, ok := reg.Get("reviewer"); !ok {
		t.Fatal("discovered def 'reviewer' did not reach the registry")
	}
	got, ok := reg.Get(BuiltinExplore)
	if !ok {
		t.Fatalf("builtin %q vanished from the registry", BuiltinExplore)
	}
	if got.Path == "" {
		t.Fatalf("%q is still the builtin; a discovered def of the same name must override it", BuiltinExplore)
	}
}

// Path is what /agents and warnings report — it must be the real file.
func TestDiscoverAgentDefs_SetsPath(t *testing.T) {
	proj, home := t.TempDir(), t.TempDir()
	want := writeDef(t, filepath.Join(proj, ".agora", "agents"), "reviewer", goodDef)

	defs, _ := DiscoverAgentDefs(DefaultAgentRoots(proj, home))
	if len(defs) != 1 {
		t.Fatalf("defs = %d; want 1", len(defs))
	}
	if defs[0].Path != want {
		t.Fatalf("Path = %q; want %q", defs[0].Path, want)
	}
}
