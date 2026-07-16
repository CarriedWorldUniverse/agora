package prompt

import (
	"fmt"
	"strings"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// ComposeInput bundles every source Compose needs: the resolved core (§2a),
// the profile/identity/environment segments (§1, segments 2-4), and the
// resolved model (dialect/rendition selection + render target, §4).
type ComposeInput struct {
	Core        Effective
	Profile     ProfileBlock
	Identity    IdentitySegment
	Environment EnvironmentSegment
	Model       contracts.ModelInfo
}

// Compose renders the ordered §1 segments into the system-role prompt bytes:
//
//	compose(core_version, profile, identity, env, resolved_model) → prompt bytes
//
// Pure function; byte-stable for stable inputs (§3) — no caching lives in
// this type, callers regenerate every turn. Dialect is applied to the core
// segment at the point the resolved model is known (§3, §4); render target
// (full vs append) branches on Model.Capabilities.SystemPromptMode (§4): in
// append mode the core segment drops its tool-discipline section (restated
// tool mechanics the host CLI already states) and adds only the rest of the
// agora contract.
//
// Spec: agora-spec-prompt.md §3.
func Compose(in ComposeInput) ([]byte, error) {
	appendMode := in.Model.Capabilities.SystemPromptMode == contracts.SystemPromptAppend

	coreText, err := renderCoreSegment(in.Core, in.Model, appendMode)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	writeSegment(&b, coreText)
	writeSegment(&b, in.Profile.Text)
	writeSegment(&b, renderIdentitySegment(in.Identity))
	writeSegment(&b, renderEnvironmentSegment(in.Environment))

	return []byte(strings.TrimRight(b.String(), "\n")), nil
}

func writeSegment(b *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(text)
}

// renderCoreSegment produces segment 1 (§1): the core contract, after
// dialect/rendition selection (§4) and, in append mode, dropping the
// tool-discipline section.
func renderCoreSegment(core Effective, model contracts.ModelInfo, appendMode bool) (string, error) {
	if r, ok := SelectRendition(core.Renditions, model, core.Hash); ok {
		// A rendition replaces knob-transforms entirely (§4). Append mode
		// still drops tool-discipline; a rendition is a whole-core text, so
		// that trim is the rendition author's responsibility once a rendition
		// targets an append-mode model — documented, not enforced here.
		return r.Text, nil
	}

	registryGlobal := map[string]string{}
	if model.Prompt != nil {
		registryGlobal = model.Prompt.Dialect
	}
	knobs := ResolveDialectKnobs(model.ID, registryGlobal, core.Dialect)

	var parts []string
	for _, seg := range CoreSectionOrder {
		if appendMode && seg == contracts.SecToolDiscipline {
			continue
		}
		body, ok := core.Sections[seg]
		if !ok {
			continue
		}
		rendered := ApplyDialect(body, knobs)
		parts = append(parts, fmt.Sprintf("## %s\n\n%s", seg, rendered))
	}
	return strings.Join(parts, "\n\n"), nil
}

// renderIdentitySegment produces segment 3 (§1): id/kind/display_name plus
// persona prose.
func renderIdentitySegment(id IdentitySegment) string {
	var lines []string
	if id.Identity.ID != "" || id.Identity.Kind != "" {
		lines = append(lines, fmt.Sprintf("id: %s", id.Identity.ID))
		if id.Identity.Kind != "" {
			lines = append(lines, fmt.Sprintf("kind: %s", id.Identity.Kind))
		}
		if id.Identity.DisplayName != "" {
			lines = append(lines, fmt.Sprintf("display_name: %s", id.Identity.DisplayName))
		}
	}
	if id.Persona != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.TrimSpace(id.Persona))
	}
	return strings.Join(lines, "\n")
}

// renderEnvironmentSegment produces segment 4 (§1). Date is rendered last so
// the stable prefix (wd/root/os/arch/model/effort/modes/locations) stays as
// long as possible across turns (§3).
func renderEnvironmentSegment(env EnvironmentSegment) string {
	var lines []string
	add := func(k, v string) {
		if v != "" {
			lines = append(lines, fmt.Sprintf("%s: %s", k, v))
		}
	}
	add("working_dir", env.WorkingDir)
	add("project_root", env.ProjectRoot)
	add("os", env.OS)
	add("arch", env.Arch)
	add("model", env.Model)
	add("effort", string(env.Effort))
	if len(env.Modes) > 0 {
		add("modes", strings.Join(env.Modes, ","))
	}
	add("memory_root", env.MemoryRoot)
	if len(env.SkillsRoots) > 0 {
		add("skills_roots", strings.Join(env.SkillsRoots, ","))
	}
	add("date", env.Date)
	return strings.Join(lines, "\n")
}
