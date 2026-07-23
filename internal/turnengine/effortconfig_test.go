package turnengine

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEffortConfig(t *testing.T, dir, json string) {
	t.Helper()
	p := filepath.Join(dir, ".agora", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaultEffort_EmptyWhenNoConfig(t *testing.T) {
	if got := LoadDefaultEffort(t.TempDir(), t.TempDir()); got != "" {
		t.Fatalf("got %q, want empty (no config anywhere)", got)
	}
}

func TestLoadDefaultEffort_CorruptFileSkipped(t *testing.T) {
	home := t.TempDir()
	writeEffortConfig(t, home, "not json")
	if got := LoadDefaultEffort(home, t.TempDir()); got != "" {
		t.Fatalf("got %q, want empty (corrupt file skipped, not fatal)", got)
	}
}

func TestLoadDefaultEffort_UnknownTierSkipped(t *testing.T) {
	home := t.TempDir()
	writeEffortConfig(t, home, `{"default_effort":"ludicrous"}`)
	if got := LoadDefaultEffort(home, t.TempDir()); got != "" {
		t.Fatalf("got %q, want empty (unknown tier skipped, not fatal)", got)
	}
}

func TestLoadDefaultEffort_ReadsGlobalFile(t *testing.T) {
	home := t.TempDir()
	writeEffortConfig(t, home, `{"default_effort":"medium"}`)
	if got := LoadDefaultEffort(home, t.TempDir()); got != "medium" {
		t.Fatalf("got %q, want medium", got)
	}
}

func TestLoadDefaultEffort_LocalOverridesGlobal(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeEffortConfig(t, home, `{"default_effort":"medium"}`)
	writeEffortConfig(t, cwd, `{"default_effort":"low"}`)
	if got := LoadDefaultEffort(home, cwd); got != "low" {
		t.Fatalf("got %q, want the project override low", got)
	}
}

func TestLoadDefaultEffort_LocalMissingKeepsGlobal(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeEffortConfig(t, home, `{"default_effort":"xhigh"}`)
	writeEffortConfig(t, cwd, `{}`)
	if got := LoadDefaultEffort(home, cwd); got != "xhigh" {
		t.Fatalf("got %q, want the global value (project file present but sets nothing)", got)
	}
}
