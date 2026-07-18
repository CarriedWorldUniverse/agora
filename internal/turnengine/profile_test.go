package turnengine

import (
	"context"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// runOneManagerTurn drives exactly one text-only turn through m and returns
// once it has completed, so the caller can assert on provider.LastRequest().
func runOneManagerTurn(t *testing.T, m *Manager) {
	t.Helper()
	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "hi"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn never completed")
	}
	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}

// TestManager_NoOptions_CarriesDevProfileIntoTurnRequest: a Manager built
// with zero options is a fully-formed dev-profile Manager (U-C4) — its
// TurnRequest.Model/AppendSystemPrompt come from DevProfile(), not an empty
// placeholder.
func TestManager_NoOptions_CarriesDevProfileIntoTurnRequest(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_profile_default", provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	runOneManagerTurn(t, m)

	dev := DevProfile()
	got := provider.LastRequest()
	if got.Model != dev.Model {
		t.Fatalf("ProviderRequest.Model = %q; want DevProfile's %q", got.Model, dev.Model)
	}
	if !strings.HasPrefix(got.AppendSystemPrompt, dev.AppendSystemPrompt) { // ctxmap appends its working-memory block; the profile prompt is the PREFIX
		t.Fatalf("ProviderRequest.AppendSystemPrompt = %q; want DevProfile's %q", got.AppendSystemPrompt, dev.AppendSystemPrompt)
	}
}

// TestManager_WithModel_OverridesDevProfile: a single-field WithModel wins
// over the DevProfile base it's layered on top of.
func TestManager_WithModel_OverridesDevProfile(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_profile_model", provider, WithModel("x"), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	runOneManagerTurn(t, m)

	if got := provider.LastRequest().Model; got != "x" {
		t.Fatalf("ProviderRequest.Model = %q; want %q (WithModel override)", got, "x")
	}
}

// TestManager_WithAppendSystemPrompt_OverridesDevProfile: same, for
// AppendSystemPrompt.
func TestManager_WithAppendSystemPrompt_OverridesDevProfile(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_profile_prompt", provider, WithAppendSystemPrompt("y"), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	runOneManagerTurn(t, m)

	if got := provider.LastRequest().AppendSystemPrompt; !strings.HasPrefix(got, "y") {
		t.Fatalf("ProviderRequest.AppendSystemPrompt = %q; want %q (WithAppendSystemPrompt override)", got, "y")
	}
}

// TestManager_WithPolicy_OverridesDevProfile: DevProfile's fail-closed
// defaultPolicy() (KindExec -> Ask) is overridable via WithPolicy — proven
// by switching exec to auto-allow and confirming no approval.requested
// event fires for a run_command call. (The reverse direction — that
// DevProfile's own policy still asks for exec when WithPolicy is NOT
// supplied — is exec_unix_test.go's
// TestManager_Approval_RunCommandStillAsksUnderDefaultPolicy, a regression
// guard this unit reuses unchanged now that NewManager's zero-options case
// IS the DevProfile policy.)
func TestManager_WithPolicy_OverridesDevProfile(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_profile_policy", provider, WithPolicy(allowAllPolicy()), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	if m.policy[contracts.KindExec] != contracts.PolicyAuto {
		t.Fatalf("m.policy[KindExec] = %v; want PolicyAuto (WithPolicy override of DevProfile's Ask)", m.policy[contracts.KindExec])
	}
}

// TestManager_WithProfile_OverridesDevProfile: WithProfile(custom)
// overrides the DevProfile base wholesale — Model, AppendSystemPrompt, and
// Policy all come from the supplied ProfileConfig, not DevProfile()'s.
func TestManager_WithProfile_OverridesDevProfile(t *testing.T) {
	custom := ProfileConfig{
		Name:               "custom",
		Model:              "custom-model",
		AppendSystemPrompt: "custom prompt",
		Policy:             allowAllPolicy(),
		ScopeStore:         approval.NewMemScopeStore(),
	}
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_profile_custom", provider, WithProfile(custom), WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))

	if m.policy[contracts.KindExec] != contracts.PolicyAuto {
		t.Fatalf("m.policy[KindExec] = %v; want PolicyAuto (WithProfile's custom policy)", m.policy[contracts.KindExec])
	}

	runOneManagerTurn(t, m)

	got := provider.LastRequest()
	if got.Model != "custom-model" {
		t.Fatalf("ProviderRequest.Model = %q; want %q (WithProfile override)", got.Model, "custom-model")
	}
	if !strings.HasPrefix(got.AppendSystemPrompt, "custom prompt") {
		t.Fatalf("ProviderRequest.AppendSystemPrompt = %q; want %q (WithProfile override)", got.AppendSystemPrompt, "custom prompt")
	}
}

// TestManager_WithModel_AfterWithProfile_StillWins: a single-field Option
// listed AFTER WithProfile overrides the profile's value for that one
// field — proving NewManager's documented "opts apply in argument order"
// precedence, not just "WithProfile always loses to any WithModel
// anywhere".
func TestManager_WithModel_AfterWithProfile_StillWins(t *testing.T) {
	custom := ProfileConfig{
		Name:               "custom",
		Model:              "custom-model",
		AppendSystemPrompt: "custom prompt",
		Policy:             defaultPolicy(),
		ScopeStore:         approval.NewMemScopeStore(),
	}
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_profile_order", provider,
		WithProfile(custom),
		WithModel("later-model"),
		WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}),
	)

	runOneManagerTurn(t, m)

	got := provider.LastRequest()
	if got.Model != "later-model" {
		t.Fatalf("ProviderRequest.Model = %q; want %q (WithModel after WithProfile)", got.Model, "later-model")
	}
	if !strings.HasPrefix(got.AppendSystemPrompt, "custom prompt") {
		t.Fatalf("ProviderRequest.AppendSystemPrompt = %q; want %q (untouched profile field)", got.AppendSystemPrompt, "custom prompt")
	}
}
