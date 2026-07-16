package prompt

import (
	_ "embed"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// builtinCoreMD is the built-in core contract, versioned with the binary
// (§2). Deliberately small: the real core TEXT (full contract prose, tuned
// and eval-gated per §6) is a later interactive/eval task — this is a
// structurally-complete placeholder that exercises every §2 section so
// composition, override, and dialect machinery has real text to operate on.
//
//go:embed builtin/core.md
var builtinCoreMD string

// BuiltinVersion is the built-in core's version, bumped whenever
// builtin/core.md's content changes. This is what override packages'
// manifest.toml base_version is checked against for drift (§2a rail 1).
const BuiltinVersion = "0.1.0"

// Builtin returns the built-in core package, parsed into its §2 sections.
func Builtin() CorePackage {
	sections, err := splitCoreMD(builtinCoreMD)
	if err != nil {
		// The embedded file is a build-time constant; a parse failure here
		// is a programmer error, not a runtime condition callers can handle.
		panic("prompt: built-in core.md failed to parse: " + err.Error())
	}
	return CorePackage{
		Manifest: contracts.CoreManifest{
			Name:        "built-in",
			BaseVersion: BuiltinVersion,
		},
		Segments: sections,
	}
}
