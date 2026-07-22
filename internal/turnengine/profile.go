package turnengine

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
	"github.com/CarriedWorldUniverse/agora/internal/prompt"
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

// devSystemPrompt is DevProfile's segment-2 profile block (agora-spec-
// prompt.md §1: "what this instance is for" — register/voice guidance,
// not core contract): a minimal note that this is the agora coding harness
// and tool calls are approval-gated. It is composed alongside the core
// contract (segment 1) and the environment (segment 4) by
// composeDevSystemPrompt — see that function's doc comment. The identity
// segment (§1 segment 3: id/kind/display_name + persona) is left out
// entirely for this unit: wiring the identity dir (~/.agora/identity/) is
// explicitly a LATER unit; Compose omits an empty segment cleanly.
const devSystemPrompt = "You are running inside agora, a coding harness. " +
	"Tool calls (file reads/writes, shell commands) are gated by an " +
	"approval policy — some may prompt the operator before they execute."

// fallbackDevSystemPrompt is what DevProfile falls back to if
// prompt.Compose itself errors (should not happen — resolveDevCore
// already degrades a broken override to the built-in core, and the
// built-in core is parsed/validated at package-init time). A composition
// failure must never fail the turn, so this mirrors the pre-U-prompt-
// assembly hardcoded note as the last-resort floor.
const fallbackDevSystemPrompt = devSystemPrompt

// devPromptOverrideDir is the user-layer core-override location a dev-
// profile composition honors, per agora-spec-prompt.md §2a: a package dir
// carrying manifest.toml / core.md (or segments/<segment>.md) / optional
// dialects.toml / renditions/. Its absence is the common case — DevProfile
// then composes purely from the built-in core (prompt.Builtin()).
func devPromptOverrideDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agora", "prompt")
}

// resolveDevCore resolves the effective core contract (§2a) for the dev
// profile: the built-in, folded with a user-layer override at
// devPromptOverrideDir() if one is present and well-formed. A missing
// override directory is silent (the normal case); a present-but-malformed
// override is logged to stderr and skipped — a broken override must never
// fail a turn (this unit's GOAL: never fail the turn over prompt assembly).
func resolveDevCore() prompt.Effective {
	builtin := prompt.Builtin()

	var overrides []prompt.Source
	if dir := devPromptOverrideDir(); dir != "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			pkg, err := prompt.LoadPackage(dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "turnengine: prompt override at %s failed to load: %v (using the built-in core)\n", dir, err)
			} else {
				overrides = append(overrides, prompt.Source{Layer: prompt.LayerUser, Pkg: pkg})
			}
		}
	}

	eff, err := prompt.Resolve(builtin, overrides, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "turnengine: prompt core resolve with override failed: %v (using the built-in core alone)\n", err)
		eff, err = prompt.Resolve(builtin, nil, nil)
		if err != nil {
			// The built-in core is a build-time embedded constant, already
			// parsed by prompt.Builtin(); resolving it with no overrides at
			// all cannot fail. A panic here means the embedded core.md
			// itself is broken, a programmer error, not a runtime fault.
			panic("turnengine: resolving the built-in core alone failed: " + err.Error())
		}
	}
	return eff
}

// composeDevSystemPrompt renders DevProfile's AppendSystemPrompt via
// internal/prompt.Compose: segment 1 (core contract, resolveDevCore),
// segment 2 (the dev profile block, devSystemPrompt), segment 4
// (environment — wd/OS/arch/model/date; identity, segment 3, is left
// empty, see devSystemPrompt's doc comment).
//
// CACHE WARNING (NEX-793): the claudesdk/anthropic lane treats the system
// prompt as cache position-0 — per-turn churn there busts the prompt
// cache. This function is called exactly ONCE per Manager, from
// DevProfile() at NewManager construction time (see manager.go's
// NewManager: `profile := DevProfile()` runs once, before opts, and its
// AppendSystemPrompt is cached into m.appendSystemPrompt for the Manager's
// whole lifetime — runOneTurn never recomputes it). So the environment
// segment's "date" field is session-granularity, not per-turn, by
// construction: computing it once here IS computing it once per Manager
// lifetime. Do not move this call into the per-turn path.
//
// Render target: DevProfile is the claudesdk lane, which owns only an
// APPEND slot onto the claude-code CLI's own base system prompt
// (agora-spec-prompt.md §4, §1 note 3) — Model.Capabilities.SystemPromptMode
// is hardcoded to SystemPromptAppend below rather than resolved from a
// model registry (no registry lookup is wired into this profile-
// structuring unit; DevProfile's model is always the claudesdk lane).
func composeDevSystemPrompt(model string) string {
	wd, err := os.Getwd()
	if err != nil {
		wd = ""
	}

	in := prompt.ComposeInput{
		Core:    resolveDevCore(),
		Profile: prompt.ProfileBlock{Text: devSystemPrompt},
		Environment: prompt.EnvironmentSegment{
			WorkingDir: wd,
			OS:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			Model:      model,
			// Date is rendered last-ish inside the environment segment by
			// prompt.Compose itself (segment.go) so the stable prefix stays
			// long; computed once here per the CACHE WARNING above.
			Date: time.Now().UTC().Format("2006-01-02"),
		},
		Model: contracts.ModelInfo{
			ID: model,
			Capabilities: contracts.Capabilities{
				SystemPromptMode: contracts.SystemPromptAppend,
			},
		},
	}

	out, composeErr := prompt.Compose(in)
	if composeErr != nil {
		fmt.Fprintf(os.Stderr, "turnengine: prompt.Compose failed: %v (falling back to the minimal dev note)\n", composeErr)
		return fallbackDevSystemPrompt
	}
	return string(out)
}

// DevProfile returns the dev (codex/Claude-Code-replacement) ProfileConfig
// — the only profile this unit builds. NewManager applies it as the BASE
// default for a Manager built with no options (see manager.go); a real
// profile registry/loader picking between multiple named profiles is a
// later unit.
func DevProfile() ProfileConfig {
	// Model: PROVISIONAL default. "claude-sonnet-5" is a plausible
	// coding-capable model id, not a confirmed one — the real
	// subscription-accepted model id is confirmed by the operator at
	// U-F1 (agora-engine-blueprint.md Phase 5: first live-turn smoke,
	// operator watches) once real billing/credentials are in the loop.
	// Overridable via WithModel. On the claude-sdk lane this string is
	// passed to the sidecar as sidecarInit.Model; on the fake provider
	// (every test in this package) it's an opaque pass-through string —
	// bridle.ErrModelRequired just needs non-empty — the fake never
	// validates it against a real catalog.
	model := "claude-sonnet-5"
	return ProfileConfig{
		Name:  "dev",
		Model: model,
		// AppendSystemPrompt: composed via internal/prompt (§1's four
		// ordered segments; see composeDevSystemPrompt's doc comment for
		// the identity-segment scope note and the cache-stability rule).
		AppendSystemPrompt: composeDevSystemPrompt(model),
		// Policy: the existing fail-closed-except-KindRead default — see
		// approval.go's defaultPolicy doc comment for why this is NOT
		// contracts.BuiltinPresets()[contracts.PresetPrompt] (that preset's
		// patch=Auto is a deliberate LATER operator decision, out of this
		// unit's scope).
		Policy:     defaultPolicy(),
		ScopeStore: approval.NewMemScopeStore(),
	}
}
