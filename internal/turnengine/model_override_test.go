package turnengine

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// TestManager_InputModelOverridesRequestModel: a non-empty input.Model (set by
// the TUI's /model switch or a %-override) must be the model the turn actually
// runs on; an input with no model falls back to the Manager's default.
func TestManager_InputModelOverridesRequestModel(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "a"}, fake.Step{Text: "b"})
	_, in, out, runErr := newTestManagerWithStore(t, "th_model", nil, provider)

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi", Model: "custom-opus"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn 1 never completed")
	}
	if got := provider.LastRequest().Model; got != "custom-opus" {
		t.Fatalf("turn 1 request model = %q, want the input override %q", got, "custom-opus")
	}

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "again"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn 2 never completed")
	}
	if got := provider.LastRequest().Model; got != "claude-sonnet-5" {
		t.Fatalf("turn 2 request model = %q, want the DevProfile default", got)
	}

	endAndClose(t, in, out, runErr)
}
