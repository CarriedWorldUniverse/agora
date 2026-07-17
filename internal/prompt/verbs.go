package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// This file backs the `agora prompt …` verbs (§2a) as callable funcs — CLI
// wiring (cmd/agora) is a later unit; these are the functions it will call.

// NewOptions configures New (`agora prompt new`, §2a).
type NewOptions struct {
	// Name is the package name, stamped into manifest.toml.
	Name string
	// Segments: empty forks the FULL core (a single core.md); non-empty
	// forks only the named sections (segments/<seg>.md each), the rest
	// inherit from the built-in at read time (Resolve fills the gaps).
	Segments []contracts.Segment
	// Source: nil forks from the built-in (`--from built-in`, the default);
	// otherwise forks from an existing named core (`--from <core>`).
	Source *CorePackage
	Notes  string
}

// New scaffolds a core package at destDir by FORKING source text (never a
// blank page): copies the full contract or named segments, stamps
// base_version honestly from builtinVersion.
// Spec: agora-spec-prompt.md §2a (`agora prompt new`).
func New(destDir string, builtin CorePackage, builtinVersion string, opts NewOptions) error {
	destDir, err := sanitizeDestDir(destDir)
	if err != nil {
		return err
	}

	// Refuse to clobber an existing package: silently overwriting hand edits,
	// or leaving a mixed core.md + segments/ layout that LoadPackage then
	// rejects as ErrPackageAmbiguous, is worse than erroring (review, U4).
	for _, marker := range []string{"manifest.toml", "core.md", "segments"} {
		if _, err := os.Stat(filepath.Join(destDir, marker)); err == nil {
			return fmt.Errorf("prompt: refusing to overwrite existing package at %s (already has %s)", destDir, marker)
		}
	}

	source := builtin
	if opts.Source != nil {
		source = *opts.Source
	}
	sections, err := source.sections()
	if err != nil {
		return err
	}

	// Validate ALL requested segment names BEFORE writing anything, so a bad
	// name fails cleanly rather than leaving a partial manifest.toml that then
	// bricks retry via the clobber guard above (delta review, U4).
	known := segmentSet(sections)
	for _, seg := range opts.Segments {
		if !known[seg] {
			return fmt.Errorf("%w: %q", ErrUnknownSegment, seg)
		}
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	// Collapse control chars in free-text manifest fields so the written
	// manifest.toml round-trips (the parser rejects control bytes) (delta, U4).
	manifestKV := map[string]string{
		"name":         collapseControlChars(opts.Name),
		"base_version": builtinVersion,
		"notes":        collapseControlChars(opts.Notes),
	}
	if err := os.WriteFile(filepath.Join(destDir, "manifest.toml"), writeTOMLFlat(manifestKV), 0o644); err != nil {
		return err
	}

	if len(opts.Segments) == 0 {
		var b strings.Builder
		for i, seg := range CoreSectionOrder {
			if i > 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "## %s\n\n%s", seg, sections[seg])
		}
		return os.WriteFile(filepath.Join(destDir, "core.md"), []byte(b.String()+"\n"), 0o644)
	}

	segDir := filepath.Join(destDir, "segments")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		return err
	}
	// Segment names were validated up front (before any write).
	for _, seg := range opts.Segments {
		body := sections[seg] + "\n"
		if err := os.WriteFile(filepath.Join(segDir, string(seg)+".md"), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// sanitizeDestDir cleans destDir and refuses one that still carries a ".."
// path-traversal component after cleaning (U4 leftover, absorbed into
// U15) — defense-in-depth against a caller passing raw, unjoined user
// input straight through as destDir (e.g. New(userSuppliedName, ...)).
// filepath.Clean alone does not reject a traversal that survives to the
// front of the path (Clean("../../etc") is still "../../etc" — there's
// nothing above it to collapse against), so this walks the cleaned path's
// components explicitly and rejects any ".." that remains.
//
// This alone is NOT sufficient containment for the realistic callsite
// (a `agora prompt new <name>` CLI verb building destDir by joining a
// fixed base cores directory with a user-supplied name): filepath.Join
// COLLAPSES "../../etc" against the base's own components before New ever
// sees the result, so no literal ".." survives to catch here even though
// the final path escaped the intended base. See ContainDestDir, the
// containment check that callsite is expected to use BEFORE the base and
// name are joined into one string.
func sanitizeDestDir(destDir string) (string, error) {
	if destDir == "" {
		return "", ErrDestDirEmpty
	}
	clean := filepath.Clean(destDir)
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: %q", ErrDestDirTraversal, destDir)
		}
	}
	return clean, nil
}

// ContainDestDir joins name under baseDir and verifies the result stays
// within baseDir, returning the safe destDir to pass to New. This is the
// check a `agora prompt new <name>` CLI verb (not yet wired — see this
// file's header comment) should use: a naive filepath.Join(baseDir, name)
// silently collapses a traversal like "../../etc" against baseDir's own
// path components, so containment must be verified with baseDir and name
// still separate, before New (and its own, weaker literal-".." guard) ever
// sees a single already-joined string.
func ContainDestDir(baseDir, name string) (string, error) {
	if name == "" {
		return "", ErrDestDirEmpty
	}
	baseClean := filepath.Clean(baseDir)
	joined := filepath.Clean(filepath.Join(baseClean, name))
	rel, err := filepath.Rel(baseClean, joined)
	if err != nil {
		return "", fmt.Errorf("%w: %q (%v)", ErrDestDirTraversal, name, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q escapes %q", ErrDestDirTraversal, name, baseDir)
	}
	return joined, nil
}

// collapseControlChars replaces control bytes (except tab) with spaces so a
// free-text field can be written to a TOML value the parser will accept back.
func collapseControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// ShowResult is Show's output (`agora prompt show`, §2a).
type ShowResult struct {
	Effective Effective
	// Diff is a per-section unified-style diff against the built-in,
	// populated only when requested.
	Diff string
}

// Show returns the already-resolved effective core, plus (when requested) a
// diff against the built-in — "what actually runs" (§2a rail 1).
func Show(eff Effective, builtin CorePackage, diff bool) (ShowResult, error) {
	res := ShowResult{Effective: eff}
	if !diff {
		return res, nil
	}
	base, err := builtin.sections()
	if err != nil {
		return ShowResult{}, err
	}
	res.Diff = diffSections(base, eff.Sections)
	return res, nil
}

// diffSections renders a minimal per-section diff: unchanged sections are
// skipped, changed/added/removed sections get a "--- <seg>"/"+++ <seg>"
// header and a naive line-level +/- body. Not a general-purpose diff
// algorithm (no LCS) — sufficient for "what changed" inspection; a real
// diff library was not reachable through this build's module proxy (see
// build report).
func diffSections(base, effective map[contracts.Segment]string) string {
	names := make(map[contracts.Segment]bool)
	for k := range base {
		names[k] = true
	}
	for k := range effective {
		names[k] = true
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, string(k))
	}
	sort.Strings(sorted)

	var b strings.Builder
	for _, name := range sorted {
		seg := contracts.Segment(name)
		oldText, hadOld := base[seg]
		newText, hasNew := effective[seg]
		if hadOld && hasNew && oldText == newText {
			continue
		}
		fmt.Fprintf(&b, "--- %s (built-in)\n+++ %s (effective)\n", seg, seg)
		for _, l := range strings.Split(oldText, "\n") {
			if hadOld {
				fmt.Fprintf(&b, "-%s\n", l)
			}
		}
		for _, l := range strings.Split(newText, "\n") {
			if hasNew {
				fmt.Fprintf(&b, "+%s\n", l)
			}
		}
	}
	return b.String()
}

// CheckResult is Check's output (`agora prompt check`, §2a).
type CheckResult struct {
	ManifestValid   bool
	UnknownSegments []contracts.Segment
	Drift           bool
	StaleRenditions []string
	Errors          []string
}

// OK reports whether the package passed every check.
func (r CheckResult) OK() bool {
	return r.ManifestValid && len(r.UnknownSegments) == 0 && !r.Drift && len(r.StaleRenditions) == 0 && len(r.Errors) == 0
}

// Check validates a package standalone: manifest completeness, its declared
// segment names against the built-in's segment set, base_version drift
// against builtinVersion, and stale renditions against eff (the resolved
// effective this package contributes to — rendition hashes are keyed to the
// *effective* core, agora-spec-prompt.md §2a rail 2). Also backs `agora
// doctor`.
// Spec: agora-spec-prompt.md §2a (`agora prompt check`).
func Check(pkg CorePackage, builtin CorePackage, eff Effective, builtinVersion string) CheckResult {
	var res CheckResult
	res.ManifestValid = pkg.Manifest.Name != "" && pkg.Manifest.BaseVersion != ""

	sections, err := pkg.sections()
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
	} else {
		builtinSections, berr := builtin.sections()
		if berr != nil {
			res.Errors = append(res.Errors, berr.Error())
		} else {
			known := segmentSet(builtinSections)
			var unknown []string
			for seg := range sections {
				if !known[seg] {
					unknown = append(unknown, string(seg))
				}
			}
			sort.Strings(unknown)
			for _, s := range unknown {
				res.UnknownSegments = append(res.UnknownSegments, contracts.Segment(s))
			}
		}
	}

	res.Drift = Drift(Effective{Manifest: pkg.Manifest}, builtinVersion)

	staleKeys := make([]string, 0, len(pkg.Renditions))
	for key, r := range pkg.Renditions {
		if r.CoreHash != eff.Hash {
			staleKeys = append(staleKeys, key)
		}
	}
	sort.Strings(staleKeys)
	res.StaleRenditions = staleKeys

	return res
}

// RebaseResult is Rebase's output (`agora prompt rebase`, §2a).
type RebaseResult struct {
	Stale bool
	// Diff shows the package's own text against the CURRENT built-in — the
	// operator's fold-in view. A precise "what changed in the built-in
	// since base_version" diff needs a historical snapshot of the built-in
	// at that version, which this unit does not store (see build report);
	// diffing against the package's declared base_version text is future
	// work once that snapshot exists.
	Diff string
}

// Rebase reports whether pkg has drifted from the running built-in and, if
// so, a diff to fold in before re-stamping base_version.
// Spec: agora-spec-prompt.md §2a (`agora prompt rebase`).
func Rebase(pkg CorePackage, builtin CorePackage, builtinVersion string) (RebaseResult, error) {
	stale := Drift(Effective{Manifest: pkg.Manifest}, builtinVersion)
	if !stale {
		return RebaseResult{Stale: false}, nil
	}
	pkgSections, err := pkg.sections()
	if err != nil {
		return RebaseResult{}, err
	}
	builtinSections, err := builtin.sections()
	if err != nil {
		return RebaseResult{}, err
	}
	return RebaseResult{Stale: true, Diff: diffSections(pkgSections, builtinSections)}, nil
}

// Compile writes a compiled rendition for (core, model) — a build-time,
// eval-gated step (§2a, §4). STILL NOT IMPLEMENTED as of U15: U4 deferred
// it as out of internal/prompt's scope (rendering pipeline + package
// format, not LLM-assisted build tooling); U15 re-examined the deferral
// per its brief ("wire the real rendering path if the TUI needs it") and
// it does not — the TUI (internal/tui) consumes an already-Resolved
// Effective core via the daemon/engine (not built yet, U18) and never
// calls Compile directly; Compile is a `agora prompt compile` CLI/build-
// tooling concern with its own eval gate (§4: "models × core eval" is
// listed as an INTERACTIVE exception in agora-spec-build.md §0 ground rule
// 5, never one-shot). Remains a documented stub; the CLI verb that would
// call it doesn't exist yet either (see this file's header comment).
// Spec: agora-spec-prompt.md §2a (`agora prompt compile`), §4.
func Compile(eff Effective, model contracts.ModelInfo) (Rendition, error) {
	return Rendition{}, ErrNotImplemented
}
