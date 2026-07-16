package prompt

import (
	"fmt"
	"sort"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// fillMissingSections returns builtinSections with sections overlaid on top, so
// a variant or full override that omits a core contract section inherits the
// built-in's text for it rather than dropping it from the composed prompt —
// contract sections are mandatory (§4; review finding, U4). Present sections
// win; missing ones gap-fill from the built-in.
func fillMissingSections(sections, builtinSections map[contracts.Segment]string) map[contracts.Segment]string {
	out := make(map[contracts.Segment]string, len(builtinSections)+len(sections))
	for k, v := range builtinSections {
		out[k] = v
	}
	for k, v := range sections {
		out[k] = v
	}
	return out
}

// Layer identifies where a core package originates. Ordering matters:
// overrides apply low-to-high, and only Layer User and above may define an
// override or a named variant (§2a, §5).
type Layer int

const (
	LayerBuiltin  Layer = iota
	LayerSystem         // /etc/agora/prompt/ — beneath the user layer (§2a)
	LayerUser           // ~/.agora/prompt/ — the normal override home
	LayerProject        // a cloned repo's .agora/ — REFUSED for core overrides
	LayerDispatch       // the dispatch envelope — REFUSED for core overrides
)

// String names a Layer for diagnostics (Show/Check output).
func (l Layer) String() string {
	switch l {
	case LayerBuiltin:
		return "built-in"
	case LayerSystem:
		return "system"
	case LayerUser:
		return "user"
	case LayerProject:
		return "project"
	case LayerDispatch:
		return "dispatch"
	default:
		return fmt.Sprintf("layer(%d)", int(l))
	}
}

// allowedOverrideLayer reports whether Layer may supply a core override or
// named variant — user-layer-and-above only (§2a, §5).
func allowedOverrideLayer(l Layer) bool {
	return l == LayerSystem || l == LayerUser
}

// Source is one candidate core package with the layer it claims to come
// from. Resolve refuses any Source whose Layer is Project or Dispatch.
type Source struct {
	Layer Layer
	Pkg   CorePackage
}

// Effective is the resolved, effective core: the result of folding built-in
// < system override < user override, or (when a variant is selected) the
// variant standing alone. Everything downstream — dialect, rendition
// selection, the §6 eval matrix — keys off Effective.Hash, never off a
// specific source layer (§2a rail 2: "overriding changes whose text is
// tested, not whether it's tested").
type Effective struct {
	Manifest contracts.CoreManifest
	// Layer is the winning layer: the variant's, or the highest override
	// layer that contributed, or LayerBuiltin if there was none.
	Layer    Layer
	Sections map[contracts.Segment]string
	Hash     string
	// Dialect carries the winning package's dialects.toml (§4 per-core
	// adjustments); Renditions carries its renditions/ (§4, keyed to Hash).
	// Both come from whichever package won — a variant stands alone with
	// its own; an override chain takes the highest layer that set one.
	Dialect    map[string]map[string]string
	Renditions map[string]Rendition
}

// Resolve computes the effective core per §2a precedence: variant (if
// selected) > user override > system override > built-in. Per-segment
// overrides merge onto the running base; a full override (FullText) replaces
// it outright. Overrides is expected pre-sorted ascending by Layer
// (LayerSystem before LayerUser) by the caller — Resolve does not
// second-guess ordering beyond refusing disallowed layers, since the layers
// in play (system, user) are exactly two and the caller (agora's config
// loader) owns their order.
//
// Spec: agora-spec-prompt.md §2a, §3, §5.
func Resolve(builtin CorePackage, overrides []Source, variant *Source) (Effective, error) {
	if variant != nil {
		if !allowedOverrideLayer(variant.Layer) {
			return Effective{}, fmt.Errorf("%w: variant from layer %s", ErrOverrideLayerNotAllowed, variant.Layer)
		}
		sections, err := variant.Pkg.sections()
		if err != nil {
			return Effective{}, err
		}
		if err := checkKnownSegments(builtin, sections); err != nil {
			return Effective{}, err
		}
		builtinSections, err := builtin.sections()
		if err != nil {
			return Effective{}, err
		}
		sections = fillMissingSections(sections, builtinSections)
		return Effective{
			Manifest:   variant.Pkg.Manifest,
			Layer:      variant.Layer,
			Sections:   sections,
			Hash:       Hash(sections),
			Dialect:    variant.Pkg.Dialect,
			Renditions: variant.Pkg.Renditions,
		}, nil
	}

	base, err := builtin.sections()
	if err != nil {
		return Effective{}, err
	}
	// Copy so folding overrides never mutates the caller's builtin map.
	sections := make(map[contracts.Segment]string, len(base))
	for k, v := range base {
		sections[k] = v
	}

	manifest := builtin.Manifest
	winningLayer := LayerBuiltin
	dialect := builtin.Dialect
	renditions := builtin.Renditions

	// Defensive ordering: fold overrides low-to-high by Layer regardless of the
	// caller's slice order, so a misordered caller can't silently invert the
	// documented precedence (system < user). The layer-ORIGIN gate below still
	// refuses Project/Dispatch outright (review finding, U4).
	ordered := append([]Source(nil), overrides...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Layer < ordered[j].Layer })

	for _, ov := range ordered {
		if !allowedOverrideLayer(ov.Layer) {
			return Effective{}, fmt.Errorf("%w: override from layer %s", ErrOverrideLayerNotAllowed, ov.Layer)
		}
		if ov.Pkg.FullText != "" {
			full, err := splitCoreMD(ov.Pkg.FullText)
			if err != nil {
				return Effective{}, err
			}
			if err := checkKnownSegments(builtin, full); err != nil {
				return Effective{}, err
			}
			sections = fillMissingSections(full, base)
		} else if len(ov.Pkg.Segments) > 0 {
			if err := checkKnownSegments(builtin, ov.Pkg.Segments); err != nil {
				return Effective{}, err
			}
			for k, v := range ov.Pkg.Segments {
				sections[k] = v
			}
		} else {
			continue // an override entry with nothing to say
		}
		if ov.Pkg.Manifest.Name != "" || ov.Pkg.Manifest.BaseVersion != "" {
			manifest = ov.Pkg.Manifest
		}
		if ov.Pkg.Dialect != nil {
			dialect = ov.Pkg.Dialect
		}
		if ov.Pkg.Renditions != nil {
			renditions = ov.Pkg.Renditions
		}
		winningLayer = ov.Layer
	}

	return Effective{
		Manifest:   manifest,
		Layer:      winningLayer,
		Sections:   sections,
		Hash:       Hash(sections),
		Dialect:    dialect,
		Renditions: renditions,
	}, nil
}

// checkKnownSegments enforces "segment names ∈ built-in's segment set"
// (§2a `agora prompt check`) at resolve time for override/variant sections.
func checkKnownSegments(builtin CorePackage, sections map[contracts.Segment]string) error {
	builtinSections, err := builtin.sections()
	if err != nil {
		return err
	}
	known := segmentSet(builtinSections)
	for seg := range sections {
		if !known[seg] {
			return fmt.Errorf("%w: %q", ErrUnknownSegment, seg)
		}
	}
	return nil
}

// Drift reports whether the running built-in has moved past the base
// version an override/variant recorded in its manifest (§2a rail 1). An
// empty BaseVersion (built-in itself, or a hand-authored package that never
// stamped one) never drifts.
func Drift(eff Effective, builtinVersion string) bool {
	if eff.Manifest.BaseVersion == "" {
		return false
	}
	return eff.Manifest.BaseVersion != builtinVersion
}
