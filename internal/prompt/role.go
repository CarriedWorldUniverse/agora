package prompt

import "github.com/CarriedWorldUniverse/agora/contracts"

// FragmentClass names a class of prompt-adjacent fragment by WHO produces it
// and what kind of content it is — the vocabulary the §1a role map is keyed
// on. Segments (core/profile/identity/environment) are one class; everything
// else agora ever puts in front of a model belongs to one of the others.
// Spec: agora-spec-prompt.md §1a.
type FragmentClass string

const (
	// FragSegments covers the four ordered system-role segments (§1): core
	// contract, profile block, identity+persona, environment.
	FragSegments FragmentClass = "segments"
	// FragSkillsCatalog is the harness-generated skills inventory
	// (agora-spec-skills.md §3.1).
	FragSkillsCatalog FragmentClass = "skills_catalog"
	// FragMemoryIndex is the harness-generated MEMORY.md index
	// (agora-spec-memory.md §2).
	FragMemoryIndex FragmentClass = "memory_index"
	// FragProjectDocs is project-authored prose (AGENTS.md/CLAUDE.md,
	// subagents §6) — content, never authority (§5).
	FragProjectDocs FragmentClass = "project_docs"
	// FragSkillBody is an invoked skill's body text (skills §3.3).
	FragSkillBody FragmentClass = "skill_body"
	// FragWorkingSet is the curated working set: tool results and other
	// content-curation output (context-curation).
	FragWorkingSet FragmentClass = "working_set"
)

// RoleMap is the §1a fragment role map exposed as data: WHO is speaking —
// system > developer > user = constitution > harness-generated state >
// content. Callers assembling non-segment fragments (skills catalog, memory
// index, project docs, skill bodies, working set) look up the right Role
// here rather than re-deriving the table.
//
// Segments 1-4 (§1) are the ONLY system-role fragments; nothing else gets
// the constitution's authority.
//
// Spec: agora-spec-prompt.md §1a.
var RoleMap = map[FragmentClass]contracts.Role{
	FragSegments:      contracts.RoleSystem,
	FragSkillsCatalog: contracts.RoleDeveloper,
	FragMemoryIndex:   contracts.RoleDeveloper,
	FragProjectDocs:   contracts.RoleUser,
	FragSkillBody:     contracts.RoleUser,
	FragWorkingSet:    contracts.RoleUser,
}
