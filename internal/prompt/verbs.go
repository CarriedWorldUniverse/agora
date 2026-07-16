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

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	manifestKV := map[string]string{
		"name":         opts.Name,
		"base_version": builtinVersion,
		"notes":        opts.Notes,
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
	known := segmentSet(sections)
	for _, seg := range opts.Segments {
		if !known[seg] {
			return fmt.Errorf("%w: %q", ErrUnknownSegment, seg)
		}
		body := sections[seg] + "\n"
		if err := os.WriteFile(filepath.Join(segDir, string(seg)+".md"), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
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
// eval-gated step (§2a, §4). NOT IMPLEMENTED in this build unit: compilation
// is LLM-assisted build tooling outside internal/prompt's scope (rendering
// pipeline + package format), tracked as follow-on work.
// Spec: agora-spec-prompt.md §2a (`agora prompt compile`), §4.
func Compile(eff Effective, model contracts.ModelInfo) (Rendition, error) {
	return Rendition{}, ErrNotImplemented
}
