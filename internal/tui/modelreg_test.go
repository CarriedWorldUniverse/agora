package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// runCmd executes a tea.Cmd the way the bubbletea runtime would, recursing into
// a tea.BatchMsg so batched cmds (e.g. submitComposer's echo+send) actually run.
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			runCmd(c)
		}
	}
}

// testRegistry is a fixed in-memory registry for TUI tests (models are config,
// not built in — so tests supply their own instead of a package default).
func testRegistry() ModelRegistry {
	return ModelRegistry{
		"sonnet": {Model: "claude-sonnet-5"},
		"opus":   {Model: "claude-opus-4-8"},
		"haiku":  {Model: "claude-haiku-4-5-20251001"},
	}
}

func writeModels(t *testing.T, dir, json string) {
	t.Helper()
	p := filepath.Join(dir, ".agora", "models.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadModelRegistry_EmptyWhenNoConfig(t *testing.T) {
	reg := LoadModelRegistry(t.TempDir(), t.TempDir())
	if len(reg) != 0 {
		t.Fatalf("reg = %v, want empty (models are config, none hardcoded)", reg)
	}
}

func TestLoadModelRegistry_CorruptFileSkipped(t *testing.T) {
	home := t.TempDir()
	writeModels(t, home, "not json")
	if reg := LoadModelRegistry(home, t.TempDir()); len(reg) != 0 {
		t.Fatalf("reg = %v, want empty (corrupt file skipped, not fatal)", reg)
	}
}

func TestLoadModelRegistry_ReadsGlobalFile(t *testing.T) {
	home := t.TempDir()
	writeModels(t, home, `{"sonnet":{"model":"custom-sonnet"}}`)
	reg := LoadModelRegistry(home, t.TempDir())
	if len(reg) != 1 || reg["sonnet"].Model != "custom-sonnet" {
		t.Fatalf("reg = %v, want the global file's single entry", reg)
	}
}

func TestLoadModelRegistry_LocalOverridesGlobal(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeModels(t, home, `{"sonnet":{"model":"g-sonnet"},"opus":{"model":"g-opus"}}`)
	writeModels(t, cwd, `{"opus":{"model":"local-opus"},"kimi":{"model":"kimi-k3","base_url":"http://x"}}`)
	reg := LoadModelRegistry(home, cwd)
	if reg["sonnet"].Model != "g-sonnet" {
		t.Fatalf("global-only entry lost: %v", reg["sonnet"])
	}
	if reg["opus"].Model != "local-opus" {
		t.Fatalf("local did not override global opus: %v", reg["opus"])
	}
	if reg["kimi"].Model != "kimi-k3" {
		t.Fatalf("local-only entry missing: %v", reg["kimi"])
	}
}

func TestModelEntry_ProviderEnv(t *testing.T) {
	if (ModelEntry{Model: "claude-sonnet-5"}).ProviderEnv() != nil {
		t.Fatal("default (no base_url) ProviderEnv should be nil")
	}
	pe := (ModelEntry{Model: "kimi-k3", BaseURL: "http://x:4000"}).ProviderEnv()
	if pe["ANTHROPIC_BASE_URL"] != "http://x:4000" || pe["ANTHROPIC_API_KEY"] != "dummy" {
		t.Fatalf("local ProviderEnv = %v, want base_url + dummy key", pe)
	}
	lit := (ModelEntry{Model: "m", BaseURL: "http://x", APIKey: "lit"}).ProviderEnv()
	if lit["ANTHROPIC_API_KEY"] != "lit" {
		t.Fatal("literal api_key not honored")
	}
	t.Setenv("AGORA_TEST_KEY", "from-env")
	env := (ModelEntry{Model: "m", BaseURL: "http://x", APIKey: "$AGORA_TEST_KEY"}).ProviderEnv()
	if env["ANTHROPIC_API_KEY"] != "from-env" {
		t.Fatal("$ENV api_key not resolved")
	}
	f := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(f, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := (ModelEntry{Model: "m", BaseURL: "http://x", APIKey: "@" + f}).ProviderEnv()
	if file["ANTHROPIC_API_KEY"] != "from-file" {
		t.Fatal("@file api_key not resolved/trimmed")
	}
}

func testModelWithRegistry(backend Backend, reg ModelRegistry) *Model {
	return NewModel(Config{
		Backend:       backend,
		AgentID:       "anvil-builder",
		Theme:         PlainTheme(),
		Now:           func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) },
		ModelRegistry: reg,
	})
}

func TestNewModel_CurrentModelDefaultsToRegistrySonnet(t *testing.T) {
	if m := testModelWithRegistry(nil, testRegistry()); m.currentModel != "claude-sonnet-5" {
		t.Fatalf("currentModel = %q, want claude-sonnet-5", m.currentModel)
	}
}

func TestNewModel_CurrentModelFromCfgModelWhenSet(t *testing.T) {
	m := NewModel(Config{Theme: PlainTheme(), Now: func() time.Time { return time.Unix(0, 0).UTC() },
		Model: "frontier:high", ModelRegistry: testRegistry()})
	if m.currentModel != "frontier:high" {
		t.Fatalf("currentModel = %q, want cfg.Model frontier:high", m.currentModel)
	}
}

func TestModelCommand_SwitchesCurrentModel(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.composer.SetValue("/model opus")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if m.currentModel != "claude-opus-4-8" {
		t.Fatalf("currentModel = %q, want claude-opus-4-8", m.currentModel)
	}
	if len(backend.Sent) != 0 {
		t.Fatalf("Sent = %v, want none (/model must not send a turn)", backend.Sent)
	}
	if m.statusErr != "" {
		t.Fatalf("statusErr = %q, want empty", m.statusErr)
	}
	if j := strings.Join(printed, "\n"); !strings.Contains(j, "opus") || !strings.Contains(j, "claude-opus-4-8") {
		t.Fatalf("printed = %v, want a confirmation", printed)
	}
}

func TestModelCommand_UnknownNameErrorsAndLeavesCurrentModelUnchanged(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	before := m.currentModel
	m.composer.SetValue("/model bogus")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if m.currentModel != before {
		t.Fatalf("currentModel = %q, want unchanged %q", m.currentModel, before)
	}
	if len(backend.Sent) != 0 {
		t.Fatalf("Sent = %v, want none", backend.Sent)
	}
	if m.statusErr == "" {
		t.Fatal("expected statusErr for an unknown model")
	}
}

func TestSubmitComposer_NormalTurnCarriesCurrentModel(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	m.composer.SetValue("/model opus")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	m.composer.SetValue("hello there")
	runCmd(m.submitComposer()) // drain the echo+send batch so the send fires
	if len(backend.Sent) != 1 {
		t.Fatalf("Sent = %v, want exactly 1 turn", backend.Sent)
	}
	if got := backend.Sent[0]; got.Text != "hello there" || got.Model != "claude-opus-4-8" {
		t.Fatalf("Sent[0] = %+v, want Model=claude-opus-4-8", got)
	}
}
