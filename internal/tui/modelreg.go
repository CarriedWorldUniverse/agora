package tui

import (
	"encoding/json"
	"fmt"
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
	// Default marks this entry as the session default when neither the
	// -model flag nor default_model in .agora/config.json names one. It
	// replaces a hardcoded "sonnet" lookup in the TUI (and gives `agora
	// pipe`, which had no default at all, the same behaviour). At most
	// one entry survives loading with this set — see LoadModelRegistry.
	Default bool `json:"default,omitempty"`
}

// ModelPricing is USD per 1M tokens by class. The usage counts it prices
// are DISJOINT (contracts.Usage): input = uncached full-rate tokens,
// cached = cache re-reads at CachedInput (e.g. 0.1× input on Anthropic),
// cacheWrite = tokens newly written into the cache at CacheWrite. When
// cache_write is not configured, writes are priced at 1.25× Input —
// Anthropic's 5-minute-cache write premium, and only the Anthropic lane
// reports cache writes at all (OpenAI-shape backends have no write count).
type ModelPricing struct {
	Input       float64 `json:"input"`
	Output      float64 `json:"output"`
	CachedInput float64 `json:"cached_input,omitempty"`
	CacheWrite  float64 `json:"cache_write,omitempty"`
}

// Cost prices a turn's usage against this table (USD). The four counts are
// disjoint — no subtraction: earlier revisions took OpenAI-inclusive input
// (cached ⊆ input) and subtracted, which mispriced the Anthropic lane's
// natively-disjoint counts (and broke the status row's cache%%).
func (p *ModelPricing) Cost(input, cached, cacheWrite, output int64) float64 {
	writeRate := p.CacheWrite
	if writeRate == 0 {
		writeRate = p.Input * 1.25
	}
	return (float64(input)*p.Input + float64(cached)*p.CachedInput +
		float64(cacheWrite)*writeRate + float64(output)*p.Output) / 1e6
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
		if uerr := json.Unmarshal(data, &m); uerr != nil {
			// A PRESENT-but-unparseable file is skipped, not fatal — but say
			// so: a stray byte from a hand edit silently emptied the whole
			// registry once (every /model name gone, no explanation).
			fmt.Fprintf(os.Stderr, "agora: models config %s is unreadable and was SKIPPED (%v)\n", path, uerr)
			continue
		}
		// A default declared by a LATER file supersedes an earlier one,
		// matching the by-name override rule above: a repo pinning its own
		// default must not have to un-flag the user-global one.
		if fileDefault := soleDefault(m, path); fileDefault != "" {
			clearDefaults(reg, "")
			clearDefaults(m, fileDefault)
		}
		for name, entry := range m {
			reg[name] = entry
		}
	}
	return reg
}

// soleDefault returns the one entry in m flagged as default, warning when
// several are. Ambiguity is resolved deterministically (alphabetically
// first) rather than by map order, which would make the session's model
// vary run to run. Warn-not-fatal matches the unparseable-file handling
// above: a bad models.json should not stop agora from starting.
func soleDefault(m ModelRegistry, path string) string {
	var flagged []string
	for name, entry := range m {
		if entry.Default {
			flagged = append(flagged, name)
		}
	}
	if len(flagged) == 0 {
		return ""
	}
	sort.Strings(flagged)
	if len(flagged) > 1 {
		fmt.Fprintf(os.Stderr, "agora: models config %s flags %d models as default (%s) — using %q\n",
			path, len(flagged), strings.Join(flagged, ", "), flagged[0])
	}
	return flagged[0]
}

// clearDefaults un-flags every entry in r except keep, so at most one
// entry in a loaded registry carries Default. Resolving this at LOAD time
// keeps Default() a straight lookup and means callers can never observe
// two defaults.
func clearDefaults(r ModelRegistry, keep string) {
	for name, entry := range r {
		if entry.Default && name != keep {
			entry.Default = false
			r[name] = entry
		}
	}
}

// Default returns the name of the entry flagged as the session default,
// or "" when none is. Loading guarantees at most one flagged entry; the
// sort is for hand-constructed registries (tests) so the result is still
// deterministic there.
func (r ModelRegistry) Default() string {
	var flagged []string
	for name, entry := range r {
		if entry.Default {
			flagged = append(flagged, name)
		}
	}
	if len(flagged) == 0 {
		return ""
	}
	sort.Strings(flagged)
	return flagged[0]
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
