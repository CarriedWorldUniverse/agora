package contracts

// Role is agora's abstract message role — the authority gradient:
// system > developer > user = constitution > harness-generated state > content.
// bridle translates per provider (developer → post-core system block on
// Anthropic-shaped APIs).
// Spec: agora-spec-prompt.md §1a.
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Segment names the ordered system-role segments and the per-segment override
// units of a core package.
// Spec: agora-spec-prompt.md §1 (order), §2 (core sections), §2a (overrides).
type Segment string

const (
	// System-prompt composition order (§1):
	SegCore        Segment = "core"
	SegProfile     Segment = "profile"
	SegIdentity    Segment = "identity"
	SegEnvironment Segment = "environment"

	// Core-contract sections (§2) — the per-segment override units:
	SecToolDiscipline Segment = "tool-discipline"
	SecApprovals      Segment = "approvals"
	SecPlanning       Segment = "planning"
	SecQuestions      Segment = "questions"
	SecOutput         Segment = "output"
	SecSecurity       Segment = "security"
)

// CoreManifest is manifest.toml of a core package (built-in, override, or
// named variant — one format everywhere; single .md file = degenerate case).
// Spec: agora-spec-prompt.md §2a.
type CoreManifest struct {
	Name string `json:"name"`
	// BaseVersion names the built-in core this package forked from — the
	// drift rail: built-in > base ⇒ loud warning; `agora prompt rebase` is
	// the exit.
	BaseVersion string `json:"base_version"`
	Notes       string `json:"notes,omitempty"`
}

// The compose contract (§3): prompt bytes are a pure function of
// (core version/hash, profile, identity, environment, resolved model) —
// byte-stable when inputs are stable, regenerated every turn, dialect applied
// at the point bridle resolves the model. Overrides are user-layer-and-above
// ONLY: never the project layer, never the dispatch envelope (§2a, and the
// config security asymmetry in the index).
