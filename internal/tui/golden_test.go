package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertGolden compares got against testdata/<name>.golden. Set
// UPDATE_GOLDEN=1 to (re)write the fixture. Goldens are LF-only and
// checked in (conventions: "Golden fixtures LF, under testdata/, checked
// in").
func assertGolden(t *testing.T, name string, got []string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	gotText := strings.Join(got, "\n") + "\n"

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(gotText), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with UPDATE_GOLDEN=1 to create it)", path, err)
	}
	if gotText != string(want) {
		t.Fatalf("golden mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s", name, gotText, string(want))
	}
}
