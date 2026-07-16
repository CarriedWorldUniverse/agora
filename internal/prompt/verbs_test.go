package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestNew_ForksFullCore(t *testing.T) {
	dir := t.TempDir() + "/mycore"
	builtin := testBuiltin()
	if err := New(dir, builtin, "1.0.0", NewOptions{Name: "mycore"}); err != nil {
		t.Fatalf("New: %v", err)
	}
	pkg, err := LoadPackage(dir)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if pkg.Manifest.Name != "mycore" || pkg.Manifest.BaseVersion != "1.0.0" {
		t.Fatalf("manifest = %+v", pkg.Manifest)
	}
	sections, err := pkg.sections()
	if err != nil {
		t.Fatalf("sections: %v", err)
	}
	for seg, want := range builtin.Segments {
		if sections[seg] != want {
			t.Errorf("section %q = %q, want %q (fork should copy source text verbatim)", seg, sections[seg], want)
		}
	}
}

func TestNew_ForksNamedSegmentsOnly(t *testing.T) {
	dir := t.TempDir() + "/mycore"
	builtin := testBuiltin()
	opts := NewOptions{Name: "mycore", Segments: []contracts.Segment{contracts.SecOutput, contracts.SecPlanning}}
	if err := New(dir, builtin, "1.0.0", opts); err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "core.md")); !os.IsNotExist(err) {
		t.Fatalf("named-segment fork should not write core.md")
	}
	pkg, err := LoadPackage(dir)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if len(pkg.Segments) != 2 {
		t.Fatalf("Segments = %v, want exactly the 2 forked segments", pkg.Segments)
	}
	if pkg.Segments[contracts.SecOutput] != builtin.Segments[contracts.SecOutput] {
		t.Errorf("output segment not forked correctly")
	}

	// Resolve fills the rest in from the built-in.
	eff, err := Resolve(builtin, []Source{{Layer: LayerUser, Pkg: pkg}}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Sections[contracts.SecApprovals] != builtin.Segments[contracts.SecApprovals] {
		t.Errorf("non-forked segment should inherit from built-in")
	}
}

func TestNew_UnknownSegmentRejected(t *testing.T) {
	dir := t.TempDir() + "/mycore"
	builtin := testBuiltin()
	opts := NewOptions{Name: "mycore", Segments: []contracts.Segment{"not-a-segment"}}
	if err := New(dir, builtin, "1.0.0", opts); err == nil {
		t.Fatalf("New with unknown segment should have failed")
	}
}

func TestShow_Diff(t *testing.T) {
	builtin := testBuiltin()
	override := Source{
		Layer: LayerUser,
		Pkg: CorePackage{
			Segments: map[contracts.Segment]string{
				contracts.SecOutput: "CONTRACT: final message carries everything. Also: be terse.",
			},
		},
	}
	eff, err := Resolve(builtin, []Source{override}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	res, err := Show(eff, builtin, true)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !strings.Contains(res.Diff, string(contracts.SecOutput)) {
		t.Errorf("diff should mention the changed section %q, got:\n%s", contracts.SecOutput, res.Diff)
	}
	if strings.Contains(res.Diff, string(contracts.SecApprovals)) {
		t.Errorf("diff should not mention an unchanged section, got:\n%s", res.Diff)
	}

	noDiff, err := Show(eff, builtin, false)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if noDiff.Diff != "" {
		t.Errorf("Show(diff=false) should not populate Diff")
	}
}

func TestCheck_ManifestAndSegments(t *testing.T) {
	builtin := testBuiltin()

	valid := CorePackage{
		Manifest: contracts.CoreManifest{Name: "ok", BaseVersion: "1.0.0"},
		Segments: map[contracts.Segment]string{contracts.SecOutput: "CONTRACT: x"},
	}
	eff, _ := Resolve(builtin, []Source{{Layer: LayerUser, Pkg: valid}}, nil)
	res := Check(valid, builtin, eff, "1.0.0")
	if !res.ManifestValid {
		t.Errorf("ManifestValid = false, want true")
	}
	if len(res.UnknownSegments) != 0 {
		t.Errorf("UnknownSegments = %v, want none", res.UnknownSegments)
	}
	if res.Drift {
		t.Errorf("Drift = true, want false (base_version matches)")
	}

	invalid := CorePackage{
		Manifest: contracts.CoreManifest{}, // missing name/base_version
		Segments: map[contracts.Segment]string{"bogus-segment": "x"},
	}
	res2 := Check(invalid, builtin, eff, "1.0.0")
	if res2.ManifestValid {
		t.Errorf("ManifestValid = true, want false (empty manifest)")
	}
	if len(res2.UnknownSegments) != 1 || res2.UnknownSegments[0] != "bogus-segment" {
		t.Errorf("UnknownSegments = %v, want [bogus-segment]", res2.UnknownSegments)
	}
}

func TestCheck_DriftAndStaleRenditions(t *testing.T) {
	builtin := testBuiltin()
	pkg := CorePackage{
		Manifest: contracts.CoreManifest{Name: "mycore", BaseVersion: "0.9.0"},
		Segments: map[contracts.Segment]string{contracts.SecOutput: "CONTRACT: x"},
	}
	eff, err := Resolve(builtin, []Source{{Layer: LayerUser, Pkg: pkg}}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	pkg.Renditions = map[string]Rendition{
		"model-a@" + eff.Hash:      {Model: "model-a", CoreHash: eff.Hash},
		"model-b@stale-hash-value": {Model: "model-b", CoreHash: "stale-hash-value"},
	}

	res := Check(pkg, builtin, eff, "1.0.0")
	if !res.Drift {
		t.Errorf("Drift = false, want true (base_version 0.9.0 vs built-in 1.0.0)")
	}
	if len(res.StaleRenditions) != 1 || res.StaleRenditions[0] != "model-b@stale-hash-value" {
		t.Errorf("StaleRenditions = %v, want exactly [model-b@stale-hash-value]", res.StaleRenditions)
	}
}

func TestRebase_ReportsStaleness(t *testing.T) {
	builtin := testBuiltin()
	current := CorePackage{Manifest: contracts.CoreManifest{Name: "mycore", BaseVersion: "1.0.0"}, Segments: builtin.Segments}
	res, err := Rebase(current, builtin, "1.0.0")
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if res.Stale {
		t.Errorf("Stale = true, want false (base_version matches)")
	}

	stale := CorePackage{Manifest: contracts.CoreManifest{Name: "mycore", BaseVersion: "0.9.0"}, Segments: builtin.Segments}
	res2, err := Rebase(stale, builtin, "1.0.0")
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if !res2.Stale {
		t.Errorf("Stale = false, want true (base_version 0.9.0 vs built-in 1.0.0)")
	}
}

func TestCompile_NotImplemented(t *testing.T) {
	eff := testEffective(t)
	_, err := Compile(eff, contracts.ModelInfo{ID: "ornith"})
	if err == nil {
		t.Fatalf("Compile should return ErrNotImplemented (documented stub)")
	}
}
