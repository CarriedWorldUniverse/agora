package prompt

import "errors"

// Sentinel errors. Spec: agora-spec-prompt.md §2a.
var (
	// ErrOverrideLayerNotAllowed is returned when an override source claims
	// to originate from Layer Project or Layer Dispatch. Overrides are
	// user-layer-and-above only.
	// Spec: agora-spec-prompt.md §2a ("Never from the project layer or the
	// dispatch envelope"), §5 (security asymmetry).
	ErrOverrideLayerNotAllowed = errors.New("prompt: core override not allowed from this layer (user-layer-and-above only)")

	// ErrPackageAmbiguous is returned when a core package directory carries
	// both core.md and segments/*.md — the layout is "either, not both".
	ErrPackageAmbiguous = errors.New("prompt: core package has both core.md and segments/ (either, not both)")

	// ErrPackageEmpty is returned when a core package directory carries
	// neither core.md nor any segments/*.md.
	ErrPackageEmpty = errors.New("prompt: core package has neither core.md nor segments/")

	// ErrUnknownSegment is returned when a segment override file names a
	// segment outside the built-in's segment set.
	ErrUnknownSegment = errors.New("prompt: segment name not in the built-in core's segment set")

	// ErrVariantNotFound is returned when Resolve is asked for a named
	// variant that does not exist under the user-layer cores/ directory.
	ErrVariantNotFound = errors.New("prompt: named core variant not found")

	// ErrNotImplemented marks a documented stub (Compile, §2a — build-time
	// only, out of scope for this unit).
	ErrNotImplemented = errors.New("prompt: not implemented")

	// ErrDestDirTraversal is returned by New when destDir still contains a
	// ".." path component after filepath.Clean — e.g. a caller building
	// destDir by joining cores/<name> with a hostile, CLI-supplied name
	// like "../../etc" (U4 deferred this hardening to U15).
	ErrDestDirTraversal = errors.New("prompt: destDir contains a path-traversal component")

	// ErrDestDirEmpty is returned by New when destDir is empty.
	ErrDestDirEmpty = errors.New("prompt: destDir must not be empty")
)
