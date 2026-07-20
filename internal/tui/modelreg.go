package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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
// for a default (subscription) entry. For a local/LiteLLM entry it sets
// ANTHROPIC_BASE_URL and ANTHROPIC_API_KEY — the key intentionally OUTRANKS the
// ambient subscription OAuth token in the SDK's auth precedence, so the turn
// goes to the named endpoint. A missing APIKey defaults to "dummy" (LiteLLM
// accepts any bearer).
func (e ModelEntry) ProviderEnv() map[string]string {
	if e.BaseURL == "" {
		return nil
	}
	key := e.APIKey
	if key == "" {
		key = "dummy"
	}
	return map[string]string{
		"ANTHROPIC_BASE_URL": e.BaseURL,
		"ANTHROPIC_API_KEY":  key,
	}
}

// ModelRegistry maps a short name (e.g. "sonnet") to its backing model id.
// Loaded from ~/.agora/models.json (LoadModelRegistry); the zero value is
// not useful on its own — callers needing a ready registry should go
// through LoadModelRegistry or DefaultModelRegistry.
type ModelRegistry map[string]ModelEntry

// DefaultModelRegistry is the built-in fallback used when ~/.agora/models.json
// is missing, unreadable, or corrupt — the TUI must never fail to start over
// a registry file problem.
func DefaultModelRegistry() ModelRegistry {
	// LiteLLM Anthropic-passthrough gateway (robo-dog) for local models.
	const litellm = "http://100.92.111.3:4000"
	return ModelRegistry{
		// Anthropic subscription (default provider — no endpoint).
		"sonnet": {Model: "claude-sonnet-5"},
		"opus":   {Model: "claude-opus-4-8"},
		"haiku":  {Model: "claude-haiku-4-5-20251001"},
		// Local models via LiteLLM (free; routed by BaseURL + a dummy key).
		"kimi":          {Model: "kimi-k3", BaseURL: litellm},
		"glm":           {Model: "glm-4.6", BaseURL: litellm},
		"deepseek":      {Model: "deepseek-v4-flash", BaseURL: litellm},
		"deepseek-fast": {Model: "deepseek-v4-flash-fast", BaseURL: litellm},
	}
}

// LoadModelRegistry loads home/.agora/models.json, creating it with
// DefaultModelRegistry's contents if the file is missing. A corrupt or
// empty file also falls back to the built-in defaults (best-effort write
// back to disk is not attempted in that case — an existing bad file is
// left alone rather than clobbered).
func LoadModelRegistry(home string) ModelRegistry {
	path := filepath.Join(home, ".agora", "models.json")
	data, err := os.ReadFile(path)
	if err != nil {
		reg := DefaultModelRegistry()
		_ = writeModelRegistry(path, reg)
		return reg
	}
	var reg ModelRegistry
	if err := json.Unmarshal(data, &reg); err != nil || len(reg) == 0 {
		return DefaultModelRegistry()
	}
	return reg
}

func writeModelRegistry(path string, reg ModelRegistry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
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
