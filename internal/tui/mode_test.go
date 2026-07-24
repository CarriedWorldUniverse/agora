package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func modeModel(t *testing.T, mode string) (*Model, *[]string) {
	t.Helper()
	m := testModel(newFakeBackend())
	printed := &[]string{}
	m.cfg.Printer = capturingPrinter(printed)
	m.cfg.PermissionMode = mode
	m.cfg.ModeCatalog = func() [][2]string {
		return [][2]string{
			{"sandbox-auto", "run inside the working dir, prompt outside"},
			{"never-escalate", "never prompt"},
		}
	}
	return m, printed
}

func runModeCmd(m *Model, text string) {
	m.composer.InsertText(text)
	m.press(tea.KeyMsg{Type: tea.KeyEnter})
}

func TestSlashMode_ShowsCurrentPosture(t *testing.T) {
	m, printed := modeModel(t, "never-escalate")
	runModeCmd(m, "/mode")
	if len(*printed) != 1 {
		t.Fatalf("printed %d blocks; want 1", len(*printed))
	}
	out := (*printed)[0]
	if !strings.Contains(out, "never-escalate") {
		t.Errorf("output does not name the current mode; got:\n%s", out)
	}
	// It must say how to change it — the command is read-only, so an
	// operator who wants a different posture needs the pointer.
	if !strings.Contains(out, "-mode") || !strings.Contains(out, "config.json") {
		t.Errorf("output does not say how to change the mode; got:\n%s", out)
	}
}

// An unset mode must not render as blank — it means the engine default.
func TestSlashMode_UnsetSaysEngineDefault(t *testing.T) {
	m, printed := modeModel(t, "")
	runModeCmd(m, "/mode")
	out := (*printed)[0]
	if !strings.Contains(out, "sandbox-auto") || !strings.Contains(out, "default") {
		t.Fatalf("unset mode did not render as the engine default; got:\n%s", out)
	}
}

func TestSlashMode_ListsAvailableModes(t *testing.T) {
	m, printed := modeModel(t, "prompt")
	runModeCmd(m, "/mode")
	out := (*printed)[0]
	for _, want := range []string{"sandbox-auto", "never-escalate"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalog missing %q; got:\n%s", want, out)
		}
	}
}

func TestSlashMode_NoCatalogStillShowsCurrent(t *testing.T) {
	m, printed := modeModel(t, "strict")
	m.cfg.ModeCatalog = nil
	runModeCmd(m, "/mode")
	if !strings.Contains((*printed)[0], "strict") {
		t.Fatalf("current mode lost when no catalog is wired; got:\n%s", (*printed)[0])
	}
}

func TestSlashMode_IsLocalAndDiscoverable(t *testing.T) {
	m, printed := modeModel(t, "prompt")
	backend := m.cfg.Backend.(*fakeBackend)
	runModeCmd(m, "/mode")
	if len(backend.Sent) != 0 {
		t.Fatal("/mode reached the model; it is a local command")
	}
	*printed = nil
	runModeCmd(m, "/help")
	if len(*printed) == 0 || !strings.Contains((*printed)[0], "mode") {
		t.Fatalf("/help does not list /mode; got:\n%v", *printed)
	}
}

// nearestCommand's tie-break must be deterministic, not dependent on where
// a verb sits in the command table. It used to be order-dependent, so
// adding /mode silently changed what "/modek" suggested.
func TestNearestCommand_TieBreaksAlphabeticallyNotByTableOrder(t *testing.T) {
	// Same candidates, opposite orders: the answer must not move.
	a := nearestCommand("modek", []string{"model", "mode"})
	b := nearestCommand("modek", []string{"mode", "model"})
	if a != b {
		t.Fatalf("suggestion depends on candidate order (%q vs %q); it must be deterministic", a, b)
	}
	if a != "mode" {
		t.Fatalf("tie resolved to %q; want the alphabetically first candidate %q", a, "mode")
	}
}
