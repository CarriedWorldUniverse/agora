package turnengine

import (
	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
)

// ProfileConfig bundles the per-Manager knobs that used to be scattered
// across NewManager's ad-hoc defaults (placeholderModel, defaultPolicy(),
// a bare approval.NewMemScopeStore()) into one resolvable unit: the model/
// system-prompt/policy/scope-store a Manager runs with. U-C4
// (agora-engine-blueprint.md Phase 2) only structures this type + a single
// hardcoded DevProfile() default and wires it into NewManager as the BASE
// default (see WithProfile/NewManager in manager.go) — a real TOML-loaded,
// multi-profile config (agent/workflow tool families, a real dev-profile
// PolicySet incl. patch=auto, MaxSteps/model per profile) is explicitly a
// LATER unit (U-E2 in the blueprint's Phase 4 decomposition); this type's
// shape is deliberately the minimal set NewManager's existing options
// already needed values for, not a speculative superset.
type ProfileConfig struct {
	// Name identifies the profile (e.g. "dev"). Informational only in this
	// unit — nothing keys off it yet (no profile registry/loader exists).
	Name string

	// Model is TurnRequest.Model (bridle.ErrModelRequired if empty — see
	// manager.go's runOneTurn). Overridable per-Manager via WithModel.
	Model string

	// AppendSystemPrompt is TurnRequest.AppendSystemPrompt. Overridable
	// per-Manager via WithAppendSystemPrompt.
	AppendSystemPrompt string

	// Policy is the Manager's approval.Decide policy set (see approval.go's
	// defaultPolicy doc comment for the fail-closed-except-KindRead
	// rationale this unit's DevProfile still uses). Overridable per-Manager
	// via WithPolicy.
	Policy contracts.PolicySet

	// ScopeStore is the Manager's approval.ScopeStore. Overridable
	// per-Manager via WithScopeStore.
	ScopeStore approval.ScopeStore
}

// devSystemPrompt is DevProfile's AppendSystemPrompt: a minimal note that
// this is the agora coding harness and tool calls are approval-gated. Kept
// deliberately short — a real internal/prompt CorePackage assembly (project
// context, ctxmap facts, tool-usage guidance) is explicitly a LATER unit
// (agora-engine-blueprint.md Phase 3/U-D1 and beyond), not this profile-
// structuring unit's job.
const devSystemPrompt = "You are running inside agora, a coding harness. " +
	"Tool calls (file reads/writes, shell commands) are gated by an " +
	"approval policy — some may prompt the operator before they execute."

// DevProfile returns the dev (codex/Claude-Code-replacement) ProfileConfig
// — the only profile this unit builds. NewManager applies it as the BASE
// default for a Manager built with no options (see manager.go); a real
// profile registry/loader picking between multiple named profiles is a
// later unit.
func DevProfile() ProfileConfig {
	return ProfileConfig{
		Name: "dev",
		// Model: PROVISIONAL default. "claude-sonnet-5" is a plausible
		// coding-capable model id, not a confirmed one — the real
		// subscription-accepted model id is confirmed by the operator at
		// U-F1 (agora-engine-blueprint.md Phase 5: first live-turn smoke,
		// operator watches) once real billing/credentials are in the loop.
		// Overridable via WithModel. On the claude-sdk lane this string is
		// passed to the sidecar as sidecarInit.Model; on the fake provider
		// (every test in this package) it's an opaque pass-through string
		// bridle.ErrModelRequired just needs non-empty — the fake never
		// validates it against a real catalog.
		Model:              "claude-sonnet-5",
		AppendSystemPrompt: devSystemPrompt,
		// Policy: the existing fail-closed-except-KindRead default — see
		// approval.go's defaultPolicy doc comment for why this is NOT
		// contracts.BuiltinPresets()[contracts.PresetPrompt] (that preset's
		// patch=Auto is a deliberate LATER operator decision, out of this
		// unit's scope).
		Policy:     defaultPolicy(),
		ScopeStore: approval.NewMemScopeStore(),
	}
}
