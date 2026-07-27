package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// capsModel builds a Model with a known ClientID for the attach tests.
func capsModel(t *testing.T, clientID string) *Model {
	t.Helper()
	return NewModel(Config{
		AgentID:  "agora",
		ClientID: clientID,
		Theme:    PlainTheme(),
		Now:      func() time.Time { return time.Unix(0, 0).UTC() },
	})
}

func attachedEvent(t *testing.T, clientID string, caps ...contracts.Capability) contracts.Event {
	t.Helper()
	names := make([]string, len(caps))
	for i, c := range caps {
		names[i] = string(c)
	}
	payload, err := json.Marshal(map[string]any{"client_id": clientID, "capabilities": names})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return contracts.Event{Type: contracts.EvClientAttached, Payload: payload}
}

// TestClientAttached_ObserverOnlyWarnsLoudly covers the client half of
// agora#133: when the backend grants capabilities that cannot send input,
// the TUI must SAY so. Silence produced a window that rendered normally and
// dropped everything typed into it — and dialBackend PREFERS a listening
// daemon, so reaching that state took no more than leaving one running.
func TestClientAttached_ObserverOnlyWarnsLoudly(t *testing.T) {
	m := capsModel(t, "me")
	m.handleEvent(attachedEvent(t, "me", contracts.CapObserver))

	if m.statusErr == "" {
		t.Fatal("observer-only attach produced no warning — the TUI would render normally and silently drop every message (agora#133)")
	}
	if !strings.Contains(m.statusErr, "read-only") {
		t.Errorf("statusErr = %q; want it to say the attach is read-only", m.statusErr)
	}
}

// TestClientAttached_InteractiveIsSilent guards the other direction: the
// normal case must not nag, and another client's read-only attach is not
// ours to report.
func TestClientAttached_InteractiveIsSilent(t *testing.T) {
	m := capsModel(t, "me")

	m.handleEvent(attachedEvent(t, "me", contracts.CapObserver, contracts.CapInteractive, contracts.CapApprover))
	if m.statusErr != "" {
		t.Errorf("interactive attach set statusErr = %q; want silence", m.statusErr)
	}

	m.handleEvent(attachedEvent(t, "someone-else", contracts.CapObserver))
	if m.statusErr != "" {
		t.Errorf("another client's read-only attach set statusErr = %q; want silence", m.statusErr)
	}
}
