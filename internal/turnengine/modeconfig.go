package turnengine

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// modeconfig.go is the inverse of permissionmode.go: that maps a PolicySet
// to the name hooks are told, this maps an operator-chosen NAME back to the
// PolicySet the session runs under.
//
// Until now the policy was whatever DevProfile() supplied and nothing in
// cmd/agora ever passed WithPolicy, so an operator could not choose one at
// all — agora always ran sandbox-auto. That makes unattended work
// impossible (everything outside the sandbox prompts, with nobody to
// answer) and equally makes a locked-down posture unavailable to anyone who
// wants one.
//
// Naming matches what hooks already report, so `permission_mode` in a hook
// payload and the name the operator typed are the same string.

// modeConfig is the on-disk shape of .agora/config.json's mode setting.
// Additive alongside default_effort in the same file.
type modeConfig struct {
	PermissionMode string `json:"permission_mode,omitempty"`
}

// SandboxAutoMode is the engine's zero-config posture: in-sandbox
// exec/patch/read run, anything outside prompts. Not one of the four
// builtin presets — it is defaultPolicy(), which is why it needs naming
// here as well as in permissionModeName.
const SandboxAutoMode = "sandbox-auto"

// PolicyForMode resolves a mode name to its PolicySet. ok is false for an
// unknown name; callers report that rather than silently falling back,
// since quietly running a DIFFERENT approval posture than the operator
// asked for is exactly the failure this whole area must not have.
func PolicyForMode(name string) (contracts.PolicySet, bool) {
	if name == SandboxAutoMode {
		return defaultPolicy(), true
	}
	if p, ok := contracts.BuiltinPresets()[name]; ok {
		return p, true
	}
	return nil, false
}

// KnownModes lists every selectable mode name, sorted, for help text and
// error messages. Derived from the same sources PolicyForMode consults so
// the two cannot drift.
func KnownModes() []string {
	names := []string{SandboxAutoMode}
	for name := range contracts.BuiltinPresets() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DescribeMode is a one-line summary for `/mode` and `-mode` help.
func DescribeMode(name string) string {
	switch name {
	case SandboxAutoMode:
		return "run inside the working dir, prompt for anything outside (default)"
	case contracts.PresetPrompt:
		return "prompt before running commands; file writes inside the sandbox are automatic"
	case contracts.PresetAutoSafe:
		return "run commands and writes without prompting; still prompt to leave the sandbox"
	case contracts.PresetStrict:
		return "prompt for everything, including reads"
	case contracts.PresetNeverEscalate:
		return "never prompt — deny anything that would escalate (headless/unattended)"
	default:
		return ""
	}
}

// LoadPermissionMode reads permission_mode from .agora/config.json — global
// (home) then per-project (cwd), the latter winning, exactly as
// LoadDefaultEffort does for default_effort.
//
// Unreadable files and unknown mode names are SKIPPED with a stderr warning
// rather than being fatal or silently substituting a default: a config typo
// must not quietly hand the session a different approval posture than the
// operator wrote. Returns "" when nothing valid is configured, leaving the
// engine's own default in place.
func LoadPermissionMode(home, cwd string) string {
	result := ""
	for _, path := range effortConfigDirs(home, cwd) { // same config.json files
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg modeConfig
		if uerr := json.Unmarshal(data, &cfg); uerr != nil {
			fmt.Fprintf(os.Stderr, "agora: config %s is unreadable and was SKIPPED (%v)\n", path, uerr)
			continue
		}
		if cfg.PermissionMode == "" {
			continue
		}
		if _, ok := PolicyForMode(cfg.PermissionMode); !ok {
			fmt.Fprintf(os.Stderr, "agora: config %s has unknown permission_mode %q and was SKIPPED (known: %v)\n",
				path, cfg.PermissionMode, KnownModes())
			continue
		}
		result = cfg.PermissionMode
	}
	return result
}

// WithPermissionMode sets the Manager's policy from a mode NAME. An unknown
// name is a no-op, leaving the existing policy untouched — callers should
// validate with PolicyForMode first so they can report the problem; this
// exists so a bad name can never silently loosen the posture.
func WithPermissionMode(name string) Option {
	return func(m *Manager) {
		if p, ok := PolicyForMode(name); ok {
			m.policy = p
		}
	}
}

// modelConfig is the on-disk shape of .agora/config.json's default_model
// setting — additive alongside default_effort and permission_mode in the
// same file.
type modelConfig struct {
	DefaultModel string `json:"default_model,omitempty"`
}

// LoadDefaultModel reads default_model from .agora/config.json — global
// (home) then per-project (cwd), the latter winning, exactly as
// LoadDefaultEffort and LoadPermissionMode do for their own keys.
//
// The value is returned VERBATIM and deliberately not validated here: it
// may name a models.json registry key ("kimi") or a raw upstream model id
// ("claude-sonnet-5"), and this package has no access to the registry that
// would distinguish them — resolution is the caller's job (cmd/agora),
// which owns that lookup. An unreadable config file is skipped with a
// warning rather than being fatal, matching the two loaders above.
func LoadDefaultModel(home, cwd string) string {
	result := ""
	for _, path := range effortConfigDirs(home, cwd) { // same config.json files
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg modelConfig
		if uerr := json.Unmarshal(data, &cfg); uerr != nil {
			fmt.Fprintf(os.Stderr, "agora: config %s is unreadable and was SKIPPED (%v)\n", path, uerr)
			continue
		}
		if cfg.DefaultModel != "" {
			result = cfg.DefaultModel
		}
	}
	return result
}
