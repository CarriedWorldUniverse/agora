package turnengine

import (
	"testing"
	"time"

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

// TestManager_NilProviderUsesDefault: an input with no Provider spec (the common
// case) runs on the Manager's default harness — the default (fake) provider is
// the one that receives the turn.
func TestManager_NilProviderUsesDefault(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "a"})
	_, in, out, runErr := newTestManagerWithStore(t, "th_defprov", nil, provider)
	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	// The default provider was reached (it recorded the turn's model) — a nil
	// Provider spec did not divert the turn to some other harness.
	if got := provider.LastRequest().Model; got != "claude-sonnet-5" {
		t.Fatalf("default provider request model = %q, want the DevProfile default", got)
	}
	endAndClose(t, in, out, runErr)
}

// TestManager_UnknownProviderErrors: a Provider spec naming a provider the turn
// engine can't build fails the turn with an error rather than silently falling
// back to the default (which would run the wrong model).
func TestManager_UnknownProviderErrors(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "a"})
	_, in, out, runErr := newTestManagerWithStore(t, "th_badprov", nil, provider)
	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi",
		Provider: &contracts.ProviderSpec{Name: "no-such-provider"}}

	var sawError, sawFailed bool
	deadline := time.After(testTimeout)
	for !(sawError && sawFailed) {
		select {
		case ev := <-out:
			switch ev.Type {
			case contracts.EvError:
				sawError = true
			case contracts.EvTurnFailed:
				sawFailed = true
			case contracts.EvTurnCompleted:
				t.Fatal("turn completed, want failure for an unknown provider")
			}
		case <-deadline:
			t.Fatalf("timed out; sawError=%v sawFailed=%v", sawError, sawFailed)
		}
	}
	if n := provider.LastRequest().Model; n != "" {
		t.Fatalf("default provider was called (model %q) despite an unknown provider spec", n)
	}
	endAndClose(t, in, out, runErr)
}
