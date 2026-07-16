package prompt

import (
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// contractLinePrefix marks a line as core contract text rather than
// surrounding presentation prose. Dialect application must reproduce every
// such line byte-for-byte — "a dialect may rephrase, reformat, re-emphasize;
// it may never add or remove contract" (§4) — so the marker is the
// mechanical line between the two.
const contractLinePrefix = "CONTRACT:"

// ResolveDialectKnobs merges the bridle registry's model-global knobs with
// this core package's dialects.toml per-core adjustments. Per §4 resolution
// order ("registry knobs (model-global defaults) ← the core package's
// dialects.toml (per-core adjustments)"), core-level entries win.
func ResolveDialectKnobs(modelID string, registryGlobal map[string]string, coreDialect map[string]map[string]string) map[string]string {
	out := make(map[string]string, len(registryGlobal))
	for k, v := range registryGlobal {
		out[k] = v
	}
	if perCore, ok := coreDialect[modelID]; ok {
		for k, v := range perCore {
			out[k] = v
		}
	}
	return out
}

// ApplyDialect renders one section's text under resolved dialect knobs.
// Presentation-only: CONTRACT: lines are reproduced verbatim, always, in
// original order — dialects can reformat the prose around them (format=flat
// collapses non-contract paragraph text to single lines) and annotate with
// recognized knobs, but they cannot touch contract text.
// Spec: agora-spec-prompt.md §4.
func ApplyDialect(text string, knobs map[string]string) string {
	lines := strings.Split(text, "\n")
	var out []string
	var para []string
	flat := knobs["format"] == "flat"

	flush := func() {
		if len(para) == 0 {
			return
		}
		if flat {
			out = append(out, strings.Join(para, " "))
		} else {
			out = append(out, para...)
		}
		para = nil
	}

	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), contractLinePrefix) {
			flush()
			out = append(out, line)
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		para = append(para, strings.TrimSpace(line))
	}
	flush()

	rendered := strings.Join(out, "\n")

	if notes := dialectNotes(knobs); notes != "" {
		rendered = rendered + "\n" + notes
	}
	return rendered
}

// dialectNotes renders recognized-but-not-reformatting knobs (tool_idiom,
// thinking, verbosity) as an appended, clearly-additive annotation line —
// additive presentation, never a rewrite of existing contract text. Knob
// names are sorted for determinism.
func dialectNotes(knobs map[string]string) string {
	recognized := map[string]bool{"tool_idiom": true, "thinking": true, "verbosity": true}
	names := make([]string, 0, len(knobs))
	for k := range knobs {
		if recognized[k] {
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, k := range names {
		parts = append(parts, "("+k+": "+knobs[k]+")")
	}
	return strings.Join(parts, " ")
}

// SelectRendition returns the compiled rendition to use in place of
// knob-transformed text, when the model's PromptMeta names one AND it is
// hash-current against coreHash (§4: "replaces knob-transforms entirely").
// A stale (hash-mismatched) or missing rendition falls back to dialect knobs.
func SelectRendition(renditions map[string]Rendition, model contracts.ModelInfo, coreHash string) (Rendition, bool) {
	if model.Prompt == nil || model.Prompt.RenditionRef == "" {
		return Rendition{}, false
	}
	key := model.Prompt.RenditionRef
	if !strings.Contains(key, "@") {
		key = key + "@" + coreHash
	}
	r, ok := renditions[key]
	if !ok || r.CoreHash != coreHash {
		return Rendition{}, false
	}
	return r, true
}
