package main

import (
	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/tui"
	"github.com/CarriedWorldUniverse/agora/internal/turnengine"
)

// model.go resolves which model (and therefore which bridle provider) a
// session runs on, for every lane.
//
// The registry itself (models.json) and its ProviderSpec lowering already
// existed and are well tested — a registry entry with a base_url routes
// that turn through bridle's OpenAI-compatible provider at that endpoint,
// which is how agora reaches LiteLLM-gateway and self-hosted models rather
// than forcing everything down the Anthropic subscription lane. What was
// missing was any way to make one of those entries the SESSION DEFAULT:
// the TUI honoured -model but had no config key, and `agora pipe` had
// neither, so headless runs — where a local model is most wanted — were
// pinned to the built-in provider no matter what models.json said.

// resolveModel picks the session's model name from, in precedence order:
// an explicit flag, then default_model in .agora/config.json (project over
// user), then the models.json entry flagged `"default": true`, then ""
// meaning the engine's own default.
//
// The registry flag sits BELOW config.json deliberately: models.json is a
// shared catalog (often checked in), config.json is where a given machine
// or repo states its preference, so the more specific file wins. It sits
// here rather than in the TUI so `agora pipe` honours it too — the TUI
// used to fall back to a hardcoded "sonnet" lookup and pipe had no default
// at all, which is the asymmetry this closes.
func resolveModel(flagValue, workingDir string) string {
	if flagValue != "" {
		return flagValue
	}
	if fromConfig := turnengine.LoadDefaultModel(userHomeOrDot(), workingDir); fromConfig != "" {
		return fromConfig
	}
	return tui.LoadModelRegistry(userHomeOrDot(), workingDir).Default()
}

// resolveModelSpec turns a model NAME into the (id, provider) pair a turn
// carries, via the same registry lookup the TUI's /model command uses — so
// a name means the same thing in every lane. A name that isn't a registry
// key is passed through as a raw upstream model id on the default
// provider, which is what lets `-model claude-sonnet-5` work without a
// registry entry.
func resolveModelSpec(name, workingDir string) (string, *contracts.ProviderSpec) {
	if name == "" {
		return "", nil
	}
	reg := tui.LoadModelRegistry(userHomeOrDot(), workingDir)
	if entry, ok := reg[name]; ok {
		return entry.Model, entry.ProviderSpec()
	}
	return name, nil
}
