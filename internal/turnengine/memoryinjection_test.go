package turnengine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtureMemory writes a minimal valid memory file at dir/<slug>.md —
// the on-disk shape internal/memory.Store's rebuildIndexLocked/scanLocked
// expect (frontmatter block + body), built by hand here rather than via
// memory.Store so this test stays a pure fixture writer, matching
// skillsinjection_test.go's writeFixtureSkill helper.
func writeFixtureMemory(t *testing.T, dir, slug, name, description, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\ntype: user\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestComposeMemoryIndexFragment_InjectedAfterSkillsBeforeAGENTSMD covers
// the GOAL: a fixture memory dir's entries reach the composed prompt as a
// developer-role <memory_index> fragment, positioned after the skills
// catalog (also developer-role) and before AGENTS.md (user-role) — spec §2
// ("same class as the skills catalog"), agora-spec-prompt.md §1a authority
// order (system > developer > user).
func TestComposeMemoryIndexFragment_InjectedAfterSkillsBeforeAGENTSMD(t *testing.T) {
	wd := isolateSkillsEnv(t)
	home := os.Getenv("HOME")

	writeFixtureSkill(t, filepath.Join(wd, ".agents", "skills", "alpha"), "alpha", "the alpha skill does alpha things")
	if err := os.WriteFile(filepath.Join(wd, "AGENTS.md"), []byte("PROJECT-CONTEXT-MARKER: build with make"), 0o644); err != nil {
		t.Fatal(err)
	}

	memDir := filepath.Join(home, ".agora", "memory", "default")
	writeFixtureMemory(t, memDir, "op-prefs", "Operator preferences", "how the operator likes things done", "prefers terse commit messages")

	const model = "claude-sonnet-5"
	got := composeDevSystemPrompt(model)

	if !strings.Contains(got, "<memory_index>") {
		t.Fatalf("composed prompt missing the memory index wrapper:\n%s", got)
	}
	if !strings.Contains(got, "Operator preferences") {
		t.Fatalf("composed prompt missing the fixture memory's title:\n%s", got)
	}

	idxCatalog := strings.Index(got, "<skills_instructions>")
	idxMemory := strings.Index(got, "<memory_index>")
	idxAgents := strings.Index(got, "AGENTS.md instructions for")
	if idxCatalog < 0 || idxMemory < 0 || idxAgents < 0 {
		t.Fatalf("expected all three fragments present: catalog=%d memory=%d agents=%d\n%s", idxCatalog, idxMemory, idxAgents, got)
	}
	if !(idxCatalog < idxMemory && idxMemory < idxAgents) {
		t.Fatalf("wrong fragment order (want catalog < memory_index < agents): catalog=%d memory=%d agents=%d\n%s", idxCatalog, idxMemory, idxAgents, got)
	}
}

// TestComposeMemoryIndexFragment_AbsentDirByteIdenticalToBaseline pins the
// common case (spec §2's absence): no ~/.agora/memory/default dir at all
// yields NOTHING appended — composeDevSystemPrompt's output is byte-
// identical to the segments-only baseline, exactly like the no-skills-
// fixtures case skillsinjection_test.go already pins.
func TestComposeMemoryIndexFragment_AbsentDirByteIdenticalToBaseline(t *testing.T) {
	wd := isolateSkillsEnv(t)

	const model = "claude-sonnet-5"
	want, err := composeBaseSystemPrompt(model, wd)
	if err != nil {
		t.Fatalf("composeBaseSystemPrompt: %v", err)
	}

	got := composeDevSystemPrompt(model)
	if got != want {
		t.Fatalf("composeDevSystemPrompt with no memory dir diverged from the segments-only baseline:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

// TestComposeMemoryIndexFragment_EmptyDirByteIdenticalToBaseline: an
// EXISTING but empty memory dir (e.g. left over from a prior memory.list
// call, or created by an operator who has not saved anything yet) must
// ALSO yield nothing appended — composeMemoryIndexFragment must not render
// memory.RenderIndex's own "(no saved memories)" shell into the prompt.
func TestComposeMemoryIndexFragment_EmptyDirByteIdenticalToBaseline(t *testing.T) {
	wd := isolateSkillsEnv(t)
	home := os.Getenv("HOME")

	memDir := filepath.Join(home, ".agora", "memory", "default")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const model = "claude-sonnet-5"
	want, err := composeBaseSystemPrompt(model, wd)
	if err != nil {
		t.Fatalf("composeBaseSystemPrompt: %v", err)
	}

	got := composeDevSystemPrompt(model)
	if got != want {
		t.Fatalf("composeDevSystemPrompt with an EMPTY memory dir diverged from the segments-only baseline:\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

// TestComposeMemoryIndexFragment_DoesNotCreateTheDir: composing the system
// prompt is called on every Manager construction (see composeDevSystemPrompt's
// CACHE WARNING) — it must never be the thing that CREATES
// ~/.agora/memory/default as a side effect, only the memory.* tool family
// may do that (and only when the model actually calls memory.write).
func TestComposeMemoryIndexFragment_DoesNotCreateTheDir(t *testing.T) {
	_ = isolateSkillsEnv(t)
	home := os.Getenv("HOME")
	memDir := filepath.Join(home, ".agora", "memory", "default")

	_ = composeDevSystemPrompt("claude-sonnet-5")

	if _, err := os.Stat(memDir); !os.IsNotExist(err) {
		t.Fatalf("memory dir %s exists after prompt composition; want absent (err=%v)", memDir, err)
	}
}
