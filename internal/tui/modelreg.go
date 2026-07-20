package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ModelEntry is one named model in the registry. BaseURL/APIKey are optional:
// when BaseURL is set the model is served by a non-default endpoint (a LiteLLM
// gateway / local model), and selecting it routes that turn there (via
// ProviderEnv: ANTHROPIC_BASE_URL + ANTHROPIC_API_KEY) instead of the default
// Anthropic subscription. Both empty = the subscription model named by Model.
type ModelEntry struct {
	Model   string `json:"model"`
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
}

// ProviderEnv returns the per-turn provider routing env for this entry, or nil
// for a default (subscription) entry. For a local/gateway entry it sets
// ANTHROPIC_BASE_URL and ANTHROPIC_API_KEY — the key intentionally OUTRANKS the
// ambient subscription OAuth token in the SDK's auth precedence, so the turn
// goes to the named endpoint. The key is resolved via resolveSecret (a `$ENV`
// or `@file` reference keeps a real key out of the world-readable models.json);
// an empty/unresolved key defaults to "dummy" (gateways that accept any bearer).
func (e ModelEntry) ProviderEnv() map[string]string {
	if e.BaseURL == "" {
		return nil
	}
	key := resolveSecret(e.APIKey)
	if key == "" {
		key = "dummy"
	}
	return map[string]string{
		"ANTHROPIC_BASE_URL": e.BaseURL,
		"ANTHROPIC_API_KEY":  key,
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
