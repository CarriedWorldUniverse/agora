package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestLoadModelRegistry_SeedsDefaultsWhenFileAbsent(t *testing.T) {
	home := t.TempDir()
	reg := LoadModelRegistry(home)

	want := DefaultModelRegistry()
	if len(reg) != len(want) {
		t.Fatalf("reg = %v, want %v", reg, want)
	}
	for name, entry := range want {
		if reg[name] != entry {
			t.Fatalf("reg[%q] = %+v, want %+v", name, reg[name], entry)
		}
	}

	path := filepath.Join(home, ".agora", "models.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected models.json to be created: %v", err)
	}
}

func TestLoadModelRegistry_CorruptFileFallsBackToDefaults(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".agora", "models.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := LoadModelRegistry(home)
	want := DefaultModelRegistry()
	if len(reg) != len(want) {
		t.Fatalf("reg = %v, want defaults %v", reg, want)
	}
}

func TestLoadModelRegistry_ReadsExistingFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".agora", "models.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := `{"sonnet":{"model":"custom-sonnet"}}`
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := LoadModelRegistry(home)
	if len(reg) != 1 || reg["sonnet"].Model != "custom-sonnet" {
		t.Fatalf("reg = %v, want the file's single custom entry", reg)
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
	m := testModelWithRegistry(nil, DefaultModelRegistry())
	if m.currentModel != "claude-sonnet-5" {
		t.Fatalf("currentModel = %q, want claude-sonnet-5 default", m.currentModel)
	}
}

func TestNewModel_CurrentModelFromCfgModelWhenSet(t *testing.T) {
	m := NewModel(Config{
		Theme:         PlainTheme(),
		Now:           func() time.Time { return time.Unix(0, 0).UTC() },
		Model:         "frontier:high",
		ModelRegistry: DefaultModelRegistry(),
	})
	if m.currentModel != "frontier:high" {
		t.Fatalf("currentModel = %q, want cfg.Model frontier:high", m.currentModel)
	}
}

func TestModelCommand_SwitchesCurrentModel(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, DefaultModelRegistry())
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
		t.Fatalf("statusErr = %q, want empty on a known model", m.statusErr)
	}
	joined := strings.Join(printed, "\n")
	if !strings.Contains(joined, "opus") || !strings.Contains(joined, "claude-opus-4-8") {
		t.Fatalf("printed = %v, want a confirmation naming opus/claude-opus-4-8", printed)
	}
}

func TestModelCommand_UnknownNameErrorsAndLeavesCurrentModelUnchanged(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, DefaultModelRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
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
		t.Fatalf("expected statusErr to be set for an unknown model name")
	}
	joined := strings.Join(printed, "\n")
	if !strings.Contains(joined, "sonnet") || !strings.Contains(joined, "opus") || !strings.Contains(joined, "haiku") {
		t.Fatalf("printed = %v, want the available names listed", printed)
	}
}

func TestModelCommand_NoArgListsRegistryMarkingCurrent(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, DefaultModelRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/model")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}

	if len(backend.Sent) != 0 {
		t.Fatalf("Sent = %v, want none", backend.Sent)
	}
	joined := strings.Join(printed, "\n")
	for _, name := range []string{"sonnet", "opus", "haiku"} {
		if !strings.Contains(joined, name) {
			t.Fatalf("printed = %q, missing name %q", joined, name)
		}
	}
	if !strings.Contains(joined, "*") {
		t.Fatalf("printed = %q, want the current model marked", joined)
	}
}

func TestSubmitComposer_NormalTurnCarriesCurrentModel(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, DefaultModelRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/model opus")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}

	m.composer.SetValue("hello there")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}

	if len(backend.Sent) != 1 {
		t.Fatalf("Sent = %v, want exactly 1 turn", backend.Sent)
	}
	got := backend.Sent[0]
	if got.Type != contracts.InUserMessage || got.Text != "hello there" || got.Model != "claude-opus-4-8" {
		t.Fatalf("Sent[0] = %+v, want Model=claude-opus-4-8", got)
	}
}
