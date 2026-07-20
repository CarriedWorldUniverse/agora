package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// ModelEntry is one named model in the registry. BaseURL/APIKey are optional:
// when BaseURL is set the model is served by a non-default endpoint (a LiteLLM
// gateway / local model), and selecting it routes that turn through bridle's
// OpenAI-compatible provider pointed at that endpoint (ProviderSpec) instead of
// the default Anthropic subscription. Both empty = the subscription model named
// by Model.
type ModelEntry struct {
	Model   string `json:"model"`
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	// Pricing is the optional USD-per-million-tokens price table for models
	// whose provider reports no cost (the subscription claudesdk path). When
	// the turn's usage carries a provider-reported cost (OpenRouter via the
	// openai provider), that EXACT figure wins and this table is ignored —
	// ccusage-style notional pricing is the fallback, not the truth.
	Pricing *ModelPricing `json:"pricing,omitempty"`
}

// ModelPricing is USD per 1M tokens by class. CachedInput is the discounted
// cache-read rate (e.g. 0.1× input on Anthropic models); cached tokens are a
// SUBSET of input tokens, so cost = (input-cached)·Input + cached·CachedInput
// + output·Output, all /1e6.
type ModelPricing struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CachedInput float64 `json:"cached_input,omitempty"`
}

// Cost prices a turn's usage against this table (USD).
func (p *ModelPricing) Cost(input, cached, output int64) float64 {
	fresh := input - cached
	if fresh < 0 {
		fresh = 0
	}
	return (float64(fresh)*p.Input + float64(cached)*p.CachedInput + float64(output)*p.Output) / 1e6
}

// ProviderSpec returns the per-turn provider selection for this entry, or nil
// for a default (subscription) entry. A BaseURL means the model lives behind an
// OpenAI-compatible endpoint (a LiteLLM gateway / local model), so the turn runs
// on bridle's "openai" provider aimed at that BaseURL — a NATIVE provider route,
// not an Anthropic-API workaround. The key is resolved via resolveSecret (a
// `$ENV` or `@file` reference keeps a real key out of the world-readable
// models.json); an empty/unresolved key becomes "dummy" (the turn engine also
// defaults it, for gateways that accept any bearer).
func (e ModelEntry) ProviderSpec() *contracts.ProviderSpec {
	if e.BaseURL == "" {
		return nil
	}
	return &contracts.ProviderSpec{
		Name:    "openai",
		BaseURL: e.BaseURL,
		APIKey:  resolveSecret(e.APIKey),
	}
}

// resolveSecret expands an api_key value so real credentials need not live in
// models.json: "$NAME" reads env var NAME, "@path" reads the file at path
// (~ expanded, trimmed), anything else is a literal. A missing env var / file
// yields "" (ProviderEnv then falls back to "dummy").
func resolveSecret(v string) string {
	switch {
	case strings.HasPrefix(v, "$"):
		return os.Getenv(v[1:])
	case strings.HasPrefix(v, "@"):
		p := v[1:]
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(userHomeOrDot(), p[2:])
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	default:
		return v
	}
}

// ModelRegistry maps a short name (e.g. "sonnet") to its backing model. It is
// CONFIGURATION, not built into the binary: it comes entirely from
// `.agora/models.json` files (LoadModelRegistry). An empty registry is valid —
// agora still runs on the engine's default model; `/model` just has nothing to
// list until the operator configures it.
type ModelRegistry map[string]ModelEntry

// modelRegistryDirs are the .agora config locations, in increasing precedence:
// the user-global home/.agora, then the per-project cwd/.agora which OVERRIDES
// it (a repo can pin its own models). Both files are optional.
func modelRegistryDirs(home, cwd string) []string {
	var dirs []string
	for _, d := range []string{home, cwd} {
		if d != "" {
			dirs = append(dirs, filepath.Join(d, ".agora", "models.json"))
		}
	}
	return dirs
}

// LoadModelRegistry reads and MERGES the model registry from the .agora config
// files — global (home/.agora/models.json) then per-project
// (cwd/.agora/models.json), the latter overriding by name. Missing files are
// skipped; a corrupt/unparseable file is skipped (its models ignored) rather
// than being fatal or clobbered. No models are hardcoded — the config files are
// the sole source.
func LoadModelRegistry(home, cwd string) ModelRegistry {
	reg := ModelRegistry{}
	for _, path := range modelRegistryDirs(home, cwd) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m ModelRegistry
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		for name, entry := range m {
			reg[name] = entry
		}
	}
	return reg
}

// Names returns the registry's names sorted alphabetically — the order
// `/model` (no arg) lists them in.
func (r ModelRegistry) Names() []string {
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// userHomeOrDot returns the user's home directory, or "." if it can't be
// determined (mirrors cmd/agora's userHomeOrDot — kept package-local so
// internal/tui doesn't need to import cmd/agora).
func userHomeOrDot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return home
}

// cwdOrDot returns the current working directory, or "." if it can't be
// determined — the per-project half of the .agora model config lookup.
func cwdOrDot() string {
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		return "."
	}
	return cwd
}
