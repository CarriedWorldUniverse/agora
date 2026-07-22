package turnengine

import (
	"fmt"
	"os"

	"github.com/CarriedWorldUniverse/agora/internal/memory"
)

// devIdentityName is the identity name the dev profile's memory store and
// memory.* tool family are scoped under (agora-spec-memory.md §1: "Per-
// identity: ~/.agora/memory/<identity-name>/"). PROVISIONAL: real identity
// resolution (~/.agora/identity/) is an explicitly later, out-of-scope
// unit — this hardcodes "default" until that wiring exists, mirroring
// devSystemPrompt's identity-segment-left-empty note in profile.go.
const devIdentityName = "default"

// defaultMemoryDir resolves the dev profile's memory store dir
// (memory.DefaultDir(home, devIdentityName)) from the process's real home
// dir. Empty return means home could not be resolved (os.UserHomeDir
// failed) — mirrors devPromptOverrideDir's own fallback; callers treat an
// empty dir as "memory unavailable for this call", never a fatal error.
func defaultMemoryDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return memory.DefaultDir(home, devIdentityName)
}

// composeMemoryIndexFragment renders the developer-role MEMORY.md index
// fragment (agora-spec-memory.md §2) for injection alongside the skills
// catalog — see composeSkillsAndAgentsFragments in profile.go for the fold
// point and role ordering this is called from.
//
// Absent memory dir (the common case: no operator has ever run memory.write
// for this identity) and an existing-but-empty dir must both yield ""
// (nothing appended, byte-identical to the no-fixtures baseline,
// memoryinjection_test.go's regression pin) — this function never calls
// memory.NewStore on a dir that doesn't already exist, specifically so
// composing a system prompt (which happens on every Manager construction,
// see composeDevSystemPrompt's CACHE WARNING) can never be the thing that
// CREATES ~/.agora/memory/<identity>/ as a side effect; the memory.* tool
// family (toolrunner/memory.go) is what's allowed to create it, and only
// when the model actually calls memory.write.
//
// Discovery/read errors are never fatal to the turn (mirrors
// composeSkillsAndAgentsFragments's own posture) — every warning goes to
// stderr.
func composeMemoryIndexFragment(home string) string {
	if home == "" {
		return ""
	}
	dir := memory.DefaultDir(home, devIdentityName)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}

	store, err := memory.NewStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "turnengine: memory store at %s failed to open: %v (skipping memory index injection)\n", dir, err)
		return ""
	}
	entries, err := store.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "turnengine: memory index list at %s failed: %v (skipping memory index injection)\n", dir, err)
		return ""
	}
	if len(entries) == 0 {
		return ""
	}

	// Budget: no model-registry context_window is wired into this
	// profile-structuring unit (same rationale as
	// composeSkillsAndAgentsFragments's skills.Budget(0) call) — spec §2:
	// "same 2%-class budgeting as the skills catalog".
	frag := memory.RenderIndex(entries, memory.Budget(0))
	for _, w := range frag.Warnings {
		fmt.Fprintf(os.Stderr, "turnengine: %s\n", w)
	}
	return frag.Text
}
