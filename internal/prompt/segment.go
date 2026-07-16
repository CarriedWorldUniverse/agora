package prompt

import "github.com/CarriedWorldUniverse/agora/contracts"

// CoreSectionOrder is the fixed rendering order of the core contract's
// per-segment override units (§2). Map iteration order is not
// deterministic in Go, so every place that concatenates core sections
// iterates this slice, never a map, to keep Compose byte-stable.
// Spec: agora-spec-prompt.md §2, §2a.
var CoreSectionOrder = []contracts.Segment{
	contracts.SecToolDiscipline,
	contracts.SecApprovals,
	contracts.SecPlanning,
	contracts.SecQuestions,
	contracts.SecOutput,
	contracts.SecSecurity,
}

// TopSegmentOrder is the §1 system-prompt segment order.
var TopSegmentOrder = []contracts.Segment{
	contracts.SegCore,
	contracts.SegProfile,
	contracts.SegIdentity,
	contracts.SegEnvironment,
}

// ProfileBlock is segment 2 (§1): what this instance is for — active modes,
// register/voice guidance. Rendered content is the profile system's concern;
// this package only places it in the ordered composition.
type ProfileBlock struct {
	Text string
}

// IdentitySegment is segment 3 (§1): id/kind/display_name plus the persona
// prose from the identity dir (persona.md, SOUL.md accepted as an import
// name). Rendering the persona file into Text is the identity system's
// concern.
type IdentitySegment struct {
	Identity contracts.Identity
	Persona  string
}

// EnvironmentSegment is segment 4 (§1): generated per turn, never persisted.
// Date is deliberately the last field rendered (§3: "sit in the environment
// segment last-ish") so the stable prefix stays as long as possible.
type EnvironmentSegment struct {
	WorkingDir  string
	ProjectRoot string
	OS          string
	Arch        string
	Model       string
	Effort      contracts.Effort
	// Modes: active mode badges (planning/orchestrate).
	Modes []string
	// MemoryRoot, SkillsRoots: locations (§1 "locations (memory root, skills
	// roots)").
	MemoryRoot  string
	SkillsRoots []string
	// Date changes every turn; render it last so the rest of the segment
	// (and everything before it) stays a stable cacheable prefix.
	Date string
}
