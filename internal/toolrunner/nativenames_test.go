package toolrunner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// The advertised surface must be exactly Claude's native spelling — that
// is the whole point of the rename, and a drift here silently restores the
// retry-on-unknown-tool behaviour it was meant to remove.
func TestFS_AdvertisesNativeToolNames(t *testing.T) {
	fam, _ := newFSFamily(t)
	got := map[string]bool{}
	for _, spec := range fam.Specs() {
		got[spec.Name] = true
	}
	for _, want := range []string{"Read", "Write", "Edit", "Glob", "Grep"} {
		if !got[want] {
			t.Errorf("Specs() does not advertise %q; got %v", want, got)
		}
	}
	// Legacy names are ACCEPTED but must not be advertised, or the model
	// sees two ways to do the same thing.
	for _, legacy := range []string{"read_file", "write_file", "edit_file", "glob", "grep"} {
		if got[legacy] {
			t.Errorf("Specs() still advertises the legacy name %q", legacy)
		}
	}
}

// Read/Write/Edit must advertise file_path, matching the native argument.
func TestFS_AdvertisesFilePathArg(t *testing.T) {
	fam, _ := newFSFamily(t)
	for _, spec := range fam.Specs() {
		switch spec.Name {
		case ToolReadFile, ToolWriteFile, ToolEditFile, ToolListDir:
		default:
			continue
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			t.Fatalf("%s: decode schema: %v", spec.Name, err)
		}
		if _, ok := schema.Properties["file_path"]; !ok {
			t.Errorf("%s: schema has no file_path property", spec.Name)
		}
		if _, ok := schema.Properties["path"]; ok {
			t.Errorf("%s: schema still advertises the legacy path property", spec.Name)
		}
		if len(schema.Required) == 0 || schema.Required[0] != "file_path" {
			t.Errorf("%s: required = %v; want file_path first", spec.Name, schema.Required)
		}
	}
}

// A native-shaped call must actually work end to end.
func TestFS_NativeNameAndArgExecute(t *testing.T) {
	fam, roots := newFSFamily(t)
	target := filepath.Join(roots.WorkingDir, "native.txt")

	res, err := fam.Execute(context.Background(), Call{
		Name: "Write", Args: mustArgs(t, map[string]string{"file_path": target, "content": "hello native"}),
	})
	if err != nil || res.IsError {
		t.Fatalf("Write with file_path: err=%v res=%+v", err, res)
	}
	if b, _ := os.ReadFile(target); string(b) != "hello native" {
		t.Fatalf("file content = %q", b)
	}
	res, err = fam.Execute(context.Background(), Call{
		Name: "Read", Args: mustArgs(t, map[string]string{"file_path": target}),
	})
	if err != nil || res.IsError || !strings.Contains(res.Content, "hello native") {
		t.Fatalf("Read with file_path: err=%v res=%+v", err, res)
	}
}

// Legacy name + legacy arg must keep working: a resumed pre-rename thread
// replays exactly this shape.
func TestFS_LegacyNameAndArgStillExecute(t *testing.T) {
	fam, roots := newFSFamily(t)
	target := filepath.Join(roots.WorkingDir, "legacy.txt")

	res, err := fam.Execute(context.Background(), Call{
		Name: "write_file", Args: mustArgs(t, map[string]string{"path": target, "content": "hello legacy"}),
	})
	if err != nil || res.IsError {
		t.Fatalf("legacy write_file: err=%v res=%+v", err, res)
	}
	res, err = fam.Execute(context.Background(), Call{
		Name: "read_file", Args: mustArgs(t, map[string]string{"path": target}),
	})
	if err != nil || res.IsError || !strings.Contains(res.Content, "hello legacy") {
		t.Fatalf("legacy read_file: err=%v res=%+v", err, res)
	}
}

// The MIXED shapes are the ones a real model produces while the two
// spellings coexist, and the ones a naive rename breaks.
func TestFS_MixedNameAndArgSpellings(t *testing.T) {
	fam, roots := newFSFamily(t)
	for _, tc := range []struct{ name, tool, arg string }{
		{"native name, legacy arg", "Write", "path"},
		{"legacy name, native arg", "write_file", "file_path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(roots.WorkingDir, strings.ReplaceAll(tc.name, " ", "_")+".txt")
			res, err := fam.Execute(context.Background(), Call{
				Name: tc.tool, Args: mustArgs(t, map[string]string{tc.arg: target, "content": "x"}),
			})
			if err != nil || res.IsError {
				t.Fatalf("%s: err=%v res=%+v", tc.name, err, res)
			}
			if _, statErr := os.Stat(target); statErr != nil {
				t.Fatalf("%s: file not written: %v", tc.name, statErr)
			}
		})
	}
}

// Classification drives APPROVALS. A native-shaped Write that classified
// with an empty path would be approved against the wrong target, so this
// is the security-relevant half of the rename.
func TestFS_ClassifyNativeShapeCarriesThePath(t *testing.T) {
	roots := newTestRoots(t)
	target := filepath.Join(roots.WorkingDir, "approve.txt")

	kind, payload := Classify(Call{
		Name: "Write", Args: mustArgs(t, map[string]string{"file_path": target, "content": "x"}),
	}, roots)
	if kind == contracts.KindRead {
		t.Fatalf("a Write classified as a read: kind=%v", kind)
	}
	if !strings.Contains(mustJSON(t, payload), target) {
		t.Fatalf("classification payload does not name the target %q: %s", target, mustJSON(t, payload))
	}

	kind, payload = Classify(Call{
		Name: "Read", Args: mustArgs(t, map[string]string{"file_path": target}),
	}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("Read classified as %v; want KindRead", kind)
	}
	if !strings.Contains(mustJSON(t, payload), target) {
		t.Fatalf("read payload does not name the target: %s", mustJSON(t, payload))
	}
}

// The sharp edge: if classification reads the WRONG argument it gets an
// empty path, and an empty path joins to the working dir — which IS inside
// the roots, so the escape check silently passes and an out-of-root write
// is approved without escalation. Asserting on an in-root write cannot
// catch that (the payload stays populated from the other field), so this
// drives an OUT-of-root target and demands escalation.
func TestFS_ClassifyNativeWriteOutsideRootsStillEscalates(t *testing.T) {
	roots := newTestRoots(t)
	outside := filepath.Join(t.TempDir(), "escape.txt")

	for _, tool := range []string{"Write", "write_file"} {
		kind, payload := Classify(Call{
			Name: tool, Args: mustArgs(t, map[string]string{"file_path": outside, "content": "x"}),
		}, roots)
		if kind != contracts.KindEscalation {
			t.Fatalf("%s to %q classified as %v; want KindEscalation — the containment check read the wrong argument",
				tool, outside, kind)
		}
		if !strings.Contains(mustJSON(t, payload), "outside the writable roots") {
			t.Fatalf("%s: escalation payload = %s; want it to name the escape", tool, mustJSON(t, payload))
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
