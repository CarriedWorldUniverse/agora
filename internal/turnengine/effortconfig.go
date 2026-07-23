package turnengine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// effortConfig is the on-disk shape of .agora/config.json's effort setting.
// Kept minimal and additive: other settings can grow into this same file
// later without disturbing this key.
type effortConfig struct {
	DefaultEffort string `json:"default_effort,omitempty"`
}

// validEffortTiers is the ladder LoadDefaultEffort accepts — the same five
// tiers contracts.Effort and the TUI's %-override recognize.
var validEffortTiers = map[string]bool{
	string(contracts.EffortLow):    true,
	string(contracts.EffortMedium): true,
	string(contracts.EffortHigh):   true,
	string(contracts.EffortXHigh):  true,
	string(contracts.EffortMax):    true,
}

// effortConfigDirs are the .agora config locations, in increasing
// precedence: user-global home/.agora, then per-project cwd/.agora which
// OVERRIDES it — same shape as internal/tui/modelreg.go's
// modelRegistryDirs, so the two config surfaces behave identically to an
// operator.
func effortConfigDirs(home, cwd string) []string {
	var dirs []string
	for _, d := range []string{home, cwd} {
		if d != "" {
			dirs = append(dirs, filepath.Join(d, ".agora", "config.json"))
		}
	}
	return dirs
}

// LoadDefaultEffort reads default_effort from .agora/config.json — global
// (home/.agora/config.json) then per-project (cwd/.agora/config.json), the
// latter overriding when both set default_effort. Missing files are
// skipped; a corrupt/unparseable file or a value outside the five known
// tiers is skipped (with a stderr warning) rather than fatal, mirroring
// LoadModelRegistry's posture. No config anywhere, or every file skipped,
// returns "" — the caller's own hardcoded fallback (contracts.EffortHigh,
// see Manager.defaultEffort's doc comment) still applies, so zero-config
// behavior is unchanged from before this existed.
func LoadDefaultEffort(home, cwd string) string {
	result := ""
	for _, path := range effortConfigDirs(home, cwd) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg effortConfig
		if uerr := json.Unmarshal(data, &cfg); uerr != nil {
			fmt.Fprintf(os.Stderr, "agora: effort config %s is unreadable and was SKIPPED (%v)\n", path, uerr)
			continue
		}
		if cfg.DefaultEffort == "" {
			continue
		}
		if !validEffortTiers[cfg.DefaultEffort] {
			fmt.Fprintf(os.Stderr, "agora: effort config %s has unknown default_effort %q and was SKIPPED\n", path, cfg.DefaultEffort)
			continue
		}
		result = cfg.DefaultEffort
	}
	return result
}
