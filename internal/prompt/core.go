package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// segmentHeaderRE matches the "## <segment-name>" markers this package uses
// to split a whole-file core.md into its §2 sections.
var segmentHeaderRE = regexp.MustCompile(`(?m)^##\s+([a-z-]+)\s*$`)

// Rendition is one compiled per-model rendering of a core (§2a, §4),
// keyed by model and the core hash it was compiled against.
type Rendition struct {
	Model    string
	CoreHash string
	Text     string
}

// CorePackage is a loaded core — built-in, an override, or a named variant.
// Layout per §2a: manifest.toml, core.md OR segments/<segment>.md (either,
// not both), optional dialects.toml, optional renditions/.
type CorePackage struct {
	Manifest contracts.CoreManifest

	// FullText is core.md's content when the package uses the single-file
	// layout. Mutually exclusive with Segments.
	FullText string
	// Segments holds segments/<segment>.md content when the package uses
	// the per-file layout, OR the built-in's own section split (the
	// built-in ships as a single embedded core.md but is parsed into
	// Segments so per-segment overrides have something to replace).
	Segments map[contracts.Segment]string

	// Dialect holds dialects.toml: model -> knob -> value (§4, per-core
	// adjustments layered under the bridle registry's model-global knobs).
	Dialect map[string]map[string]string

	// Renditions holds renditions/<model>@<hash>.md, keyed "<model>@<hash>".
	Renditions map[string]Rendition
}

// sections returns the package's section map regardless of layout: a
// FullText package is split by its "## <segment>" headers; a Segments
// package is returned as-is.
func (p CorePackage) sections() (map[contracts.Segment]string, error) {
	if p.FullText != "" && len(p.Segments) > 0 {
		return nil, ErrPackageAmbiguous
	}
	if p.Segments != nil {
		return p.Segments, nil
	}
	if p.FullText == "" {
		return nil, ErrPackageEmpty
	}
	return splitCoreMD(p.FullText)
}

// splitCoreMD splits a whole core.md on "## <segment>" headers into a
// section map. Text before the first header is discarded (front matter is
// not a section).
func splitCoreMD(text string) (map[contracts.Segment]string, error) {
	locs := segmentHeaderRE.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return nil, fmt.Errorf("prompt: core.md has no \"## <segment>\" headers")
	}
	out := map[contracts.Segment]string{}
	for i, loc := range locs {
		name := text[loc[2]:loc[3]]
		bodyStart := loc[1]
		bodyEnd := len(text)
		if i+1 < len(locs) {
			bodyEnd = locs[i+1][0]
		}
		out[contracts.Segment(name)] = strings.TrimSpace(text[bodyStart:bodyEnd])
	}
	return out, nil
}

// segmentSet returns the set of segment names present in a package.
func segmentSet(sections map[contracts.Segment]string) map[contracts.Segment]bool {
	set := make(map[contracts.Segment]bool, len(sections))
	for k := range sections {
		set[k] = true
	}
	return set
}

// LoadPackage reads a core package directory: manifest.toml, core.md OR
// segments/*.md, optional dialects.toml, optional renditions/*.md.
// Spec: agora-spec-prompt.md §2a (canonical layout).
func LoadPackage(dir string) (CorePackage, error) {
	var pkg CorePackage

	manifestPath := filepath.Join(dir, "manifest.toml")
	if data, err := os.ReadFile(manifestPath); err == nil {
		kv, perr := parseTOMLFlat(data)
		if perr != nil {
			return CorePackage{}, fmt.Errorf("prompt: %s: %w", manifestPath, perr)
		}
		pkg.Manifest = contracts.CoreManifest{
			Name:        kv["name"],
			BaseVersion: kv["base_version"],
			Notes:       kv["notes"],
		}
	} else if !os.IsNotExist(err) {
		return CorePackage{}, err
	}

	coreMDPath := filepath.Join(dir, "core.md")
	hasFullText := false
	if data, err := os.ReadFile(coreMDPath); err == nil {
		pkg.FullText = string(data)
		hasFullText = true
	} else if !os.IsNotExist(err) {
		return CorePackage{}, err
	}

	segDir := filepath.Join(dir, "segments")
	if entries, err := os.ReadDir(segDir); err == nil {
		if hasFullText {
			return CorePackage{}, ErrPackageAmbiguous
		}
		pkg.Segments = map[contracts.Segment]string{}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			data, rerr := os.ReadFile(filepath.Join(segDir, e.Name()))
			if rerr != nil {
				return CorePackage{}, rerr
			}
			pkg.Segments[contracts.Segment(name)] = strings.TrimSpace(string(data))
		}
	} else if !os.IsNotExist(err) {
		return CorePackage{}, err
	}

	if !hasFullText && len(pkg.Segments) == 0 {
		return CorePackage{}, ErrPackageEmpty
	}

	dialectsPath := filepath.Join(dir, "dialects.toml")
	if data, err := os.ReadFile(dialectsPath); err == nil {
		sections, perr := parseTOMLSections(data)
		if perr != nil {
			return CorePackage{}, fmt.Errorf("prompt: %s: %w", dialectsPath, perr)
		}
		pkg.Dialect = map[string]map[string]string{}
		for section, kv := range sections {
			// dialects.toml sections are "models.<model>".
			model := strings.TrimPrefix(section, "models.")
			pkg.Dialect[model] = kv
		}
	} else if !os.IsNotExist(err) {
		return CorePackage{}, err
	}

	renDir := filepath.Join(dir, "renditions")
	if entries, err := os.ReadDir(renDir); err == nil {
		pkg.Renditions = map[string]Rendition{}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			key := strings.TrimSuffix(e.Name(), ".md")
			model, hash, ok := strings.Cut(key, "@")
			if !ok {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(renDir, e.Name()))
			if rerr != nil {
				return CorePackage{}, rerr
			}
			pkg.Renditions[key] = Rendition{Model: model, CoreHash: hash, Text: string(data)}
		}
	} else if !os.IsNotExist(err) {
		return CorePackage{}, err
	}

	return pkg, nil
}

// Hash returns the deterministic content hash of a resolved section map,
// used to key renditions and to detect drift. Section order is fixed
// (CoreSectionOrder) so the hash does not depend on map iteration order;
// any section not in CoreSectionOrder (forward-compat / unknown) is
// appended in sorted-name order after the known set.
func Hash(sections map[contracts.Segment]string) string {
	h := sha256.New()
	seen := map[contracts.Segment]bool{}
	for _, seg := range CoreSectionOrder {
		seen[seg] = true
		fmt.Fprintf(h, "%s\x00%s\x00", seg, sections[seg])
	}
	extra := make([]string, 0)
	for seg := range sections {
		if !seen[seg] {
			extra = append(extra, string(seg))
		}
	}
	sort.Strings(extra)
	for _, seg := range extra {
		fmt.Fprintf(h, "%s\x00%s\x00", seg, sections[contracts.Segment(seg)])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
