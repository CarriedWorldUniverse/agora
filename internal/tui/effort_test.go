package tui

import (
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestEffortCommand_SetsCurrentEffort(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.composer.SetValue("/effort xhigh")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if m.currentEffort != contracts.EffortXHigh {
		t.Fatalf("currentEffort = %q, want xhigh", m.currentEffort)
	}
	if len(backend.Sent) != 0 {
		t.Fatalf("Sent = %v, want none (/effort must not send a turn)", backend.Sent)
	}
	if j := strings.Join(printed, "\n"); !strings.Contains(j, "xhigh") {
		t.Fatalf("printed = %v, want a confirmation mentioning xhigh", printed)
	}
}

func TestEffortCommand_UnknownTierErrorsAndLeavesUnchanged(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	m.composer.SetValue("/effort xhigh")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	m.composer.SetValue("/effort bogus")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if m.currentEffort != contracts.EffortXHigh {
		t.Fatalf("currentEffort = %q, want unchanged xhigh", m.currentEffort)
	}
}

func TestEffortCommand_DefaultClearsPin(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	m.composer.SetValue("/effort low")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	m.composer.SetValue("/effort default")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if m.currentEffort != "" {
		t.Fatalf("currentEffort = %q, want cleared to empty", m.currentEffort)
	}
}

func TestSubmitComposer_NormalTurnCarriesCurrentEffort(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	m.composer.SetValue("/effort medium")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	m.composer.SetValue("hello there")
	runCmd(m.submitComposer())
	if len(backend.Sent) != 1 {
		t.Fatalf("Sent = %v, want exactly 1 turn", backend.Sent)
	}
	if got := backend.Sent[0]; got.Text != "hello there" || got.Effort != contracts.EffortMedium {
		t.Fatalf("Sent[0] = %+v, want Effort=medium", got)
	}
}

func TestSubmitComposer_NoEffortPinLeavesInputEffortEmpty(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	m.composer.SetValue("hello there")
	runCmd(m.submitComposer())
	if len(backend.Sent) != 1 {
		t.Fatalf("Sent = %v, want exactly 1 turn", backend.Sent)
	}
	if got := backend.Sent[0]; got.Effort != "" {
		t.Fatalf("Effort = %q, want empty (engine default applies)", got.Effort)
	}
}

func TestStatusRow_ShowsEffortOnlyWhenPinned(t *testing.T) {
	m := testModel(newFakeBackend())
	if row := m.renderStatusRow(); strings.Contains(row, "xhigh") {
		t.Fatalf("status row shows an effort segment before any pin:\n%s", row)
	}
	m.composer.SetValue("/effort xhigh")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if row := m.renderStatusRow(); !strings.Contains(row, "xhigh") {
		t.Fatalf("status row missing the pinned effort tier:\n%s", row)
	}
}

func TestRunSlashStatus_ReportsEffort(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/status")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if j := strings.Join(printed, "\n"); !strings.Contains(j, "default (engine-configured)") {
		t.Fatalf("printed = %v, want an unpinned effort row", printed)
	}

	printed = nil
	m.composer.SetValue("/effort low")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	printed = nil
	m.composer.SetValue("/status")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if j := strings.Join(printed, "\n"); !strings.Contains(j, "low") {
		t.Fatalf("printed = %v, want the pinned effort tier", printed)
	}
}

func TestRunSlashHelp_DocumentsEffortAndOverrideSyntax(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.composer.SetValue("/help")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	j := strings.Join(printed, "\n")
	if !strings.Contains(j, "/effort") {
		t.Fatalf("help output missing /effort:\n%s", j)
	}
	if !strings.Contains(j, "%model[:effort]") {
		t.Fatalf("help output missing the one-shot %%-override row:\n%s", j)
	}
}
