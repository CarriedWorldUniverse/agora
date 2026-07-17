package pod

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
)

// stubBroker is a minimal stand-in for the nexus dispatch controller (§6a:
// "dispatch drives it like any interactive client"). It only ever calls the
// same public seam a real broker/daemon wiring (U18) would: Provision then
// RunTurn. Modeling it as a distinct type (rather than calling Pod methods
// directly in the test body) makes the "stub broker drives ..." shape in
// the DoD bullet an actual, named actor in the test, not just prose.
type stubBroker struct {
	pod    *Pod
	device remote.Device
}

func (b *stubBroker) provision(ctx context.Context, msg contracts.Provision) (ProvisionedInfo, error) {
	return b.pod.Provision(ctx, b.device, msg)
}

func (b *stubBroker) runTurn(ctx context.Context, text string) (TurnResult, error) {
	return b.pod.RunTurn(ctx, text)
}

// TestE2E_StubBroker_ProvisionTurnBlockedRoundTrip is the U17 DoD's e2e
// acceptance test: "a stub broker drives provision → turn → blocked:
// needs-input round-trip. The pod boots blank (--pod), gets provisioned,
// runs a turn, and when the turn hits an unanswered question it surfaces
// blocked: needs-input back to the broker ... not a fabricated answer."
func TestE2E_StubBroker_ProvisionTurnBlockedRoundTrip(t *testing.T) {
	ctx := context.Background()

	question := contracts.QuestionArgs{
		Text:     "the ticket doesn't say which environment to deploy to — staging or prod?",
		Options:  []contracts.QuestionOption{{Label: "staging"}, {Label: "prod"}},
		FreeText: false,
	}
	engine := &agoraio.ScriptedEngine{Script: []agoraio.ScriptedTurn{
		{Events: []contracts.Event{
			{Type: contracts.EvItemStarted, Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemAgentMessage}},
			{Type: contracts.EvQuestionAsked, Payload: mustJSON(t, contracts.QuestionAsked{
				ID: "ignored-on-the-wire", Source: contracts.QuestionFromAgent, Blocking: true, Args: question,
			})},
		}},
	}}

	p, store := newTestPod(t, ctx, engine)

	// 1. Boots blank: nobody attached, no working identity/profile/workspace
	// (§6a). The stub broker's own enrollment is the pod's ONLY controller —
	// modeled here as the one device the test constructs.
	if got := p.State(); got != StateBlank {
		t.Fatalf("fresh pod state = %q, want %q (--pod boots blank)", got, StateBlank)
	}
	broker := &stubBroker{
		pod:    p,
		device: dispatchDevice("nexus-dispatch", remote.DeviceConstraints{}),
	}

	// A turn attempted before provisioning is refused — "refuses turns
	// until provisioned" is not just a docstring, the broker actually hits
	// it if it tries.
	if _, err := broker.runTurn(ctx, "too early"); err == nil {
		t.Fatalf("runTurn before provision succeeded, want ErrNotProvisioned")
	}

	// 2. Provision: the broker sends the §6a provision message shape.
	msg := contracts.Provision{
		Profile: "aspect-builder",
		Session: contracts.ProvisionSession{New: true},
		Workspace: &contracts.ProvisionWorkspace{
			Dir: "/work",
		},
	}
	msg.Identity.Source = "keyring:anvil"

	info, err := broker.provision(ctx, msg)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if info.IdentityFP == "" || info.Profile != "aspect-builder" || info.ThreadID == "" {
		t.Fatalf("provisioned info incomplete: %+v", info)
	}
	if got := p.State(); got != StateProvisioned {
		t.Fatalf("state after provision = %q, want %q", got, StateProvisioned)
	}

	// 3. Turn: the broker dispatches the ticket/task as user_message.
	result, err := broker.runTurn(ctx, "deploy the release")
	if err != nil {
		t.Fatalf("runTurn: %v", err)
	}

	// 4. blocked:needs-input round-trip: the broker gets back a typed
	// blocked result, never a fabricated deploy-to-staging-or-prod guess.
	if result.Blocked == nil {
		t.Fatalf("result.Blocked = nil, want a BlockedNeedsInput — the turn asked a blocking question")
	}
	if result.Blocked.ThreadID != info.ThreadID {
		t.Errorf("Blocked.ThreadID = %q, want %q", result.Blocked.ThreadID, info.ThreadID)
	}
	if result.Blocked.Question.Args.Text != question.Text {
		t.Errorf("Blocked.Question.Args.Text = %q, want %q", result.Blocked.Question.Args.Text, question.Text)
	}
	if len(result.Blocked.Question.Args.Options) != 2 {
		t.Errorf("Blocked.Question.Args.Options = %v, want the 2 options the model offered (never dropped/guessed)", result.Blocked.Question.Args.Options)
	}

	// The pod itself stays warm (only the work item/turn died honestly) —
	// dispatch can re-provision or reuse it; deprovisioning is a separate,
	// explicit lifecycle act.
	if got := p.State(); got != StateProvisioned {
		t.Errorf("pod state after a blocked turn = %q, want still %q (only the turn dies honestly, not the pod — §6a warm-pool reuse)", got, StateProvisioned)
	}

	// Full audit trail on the thread: provisioning + the question, in order.
	items := threadItems(t, store, info.ThreadID)
	if len(items) < 2 {
		t.Fatalf("expected at least provisioning + question_asked items, got %d: %+v", len(items), items)
	}
	if items[0].Type != contracts.TIProvisioning {
		t.Errorf("items[0].Type = %q, want %q (provisioning recorded first)", items[0].Type, contracts.TIProvisioning)
	}
	var sawQuestion bool
	for _, it := range items {
		if it.Type == contracts.TIQuestionAsked {
			sawQuestion = true
		}
	}
	if !sawQuestion {
		t.Errorf("no question_asked item recorded: %+v", items)
	}
}

// TestE2E_StubBroker_ScopedDevice_ProvisionRefused folds the CRITICAL U16
// handoff into the same stub-broker shape as the round-trip test above: a
// device whose enrollment narrows it to a DIFFERENT profile than the one
// the broker requests is refused at the provision boundary — fail-closed,
// no session ever starts, no thread ever created. This is the deliberate
// "scope violation is REFUSED" acceptance test the brief calls out by name.
func TestE2E_StubBroker_ScopedDevice_ProvisionRefused(t *testing.T) {
	ctx := context.Background()
	p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})

	// This device is enrolled narrowly: it may only ever drive the
	// "aspect-reviewer" profile (e.g. a vessel bound to review work,
	// remote §4's own example: "vessel bound to the chat profile only").
	broker := &stubBroker{
		pod:    p,
		device: dispatchDevice("scoped-vessel", remote.DeviceConstraints{AllowedProfiles: []string{"aspect-reviewer"}}),
	}

	// The broker (compromised, misconfigured, or simply wrong) tries to
	// provision it as a builder instead.
	msg := validNewProvision("aspect-builder")
	if _, err := broker.provision(ctx, msg); err == nil {
		t.Fatalf("provision with out-of-scope profile succeeded, want refusal")
	}

	if got := p.State(); got != StateBlank {
		t.Fatalf("pod state after refused out-of-scope provision = %q, want %q", got, StateBlank)
	}
	threads, err := store.List(contracts.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(threads) != 0 {
		t.Fatalf("%d thread(s) created despite the refused, out-of-scope provision", len(threads))
	}

	// The in-scope profile still works for the same device — constraints
	// narrow, they don't blanket-deny the device.
	if _, err := broker.provision(ctx, validNewProvision("aspect-reviewer")); err != nil {
		t.Fatalf("in-scope provision after an out-of-scope refusal: %v", err)
	}
}

// TestE2E_ProvisionedEvent_ReachesAnObservingOperatorTUI exercises §6a's
// last bullet — "multiple concurrent controllers still work: dispatch
// (admin) driving while the operator's TUI attaches as observer to watch a
// builder live" — by attaching a second, observer-only client directly to
// the underlying io.Session (the same multi-attach model io already
// proves) and checking it sees the `provisioned {identity_fp, profile}`
// event (§6a) the pod's own provisionedEngine wrapper emits.
func TestE2E_ProvisionedEvent_ReachesAnObservingOperatorTUI(t *testing.T) {
	ctx := context.Background()
	p, _ := newTestPod(t, ctx, &agoraio.ScriptedEngine{Script: []agoraio.ScriptedTurn{{}}})
	device := dispatchDevice("nexus-dispatch", remote.DeviceConstraints{})

	info, err := p.Provision(ctx, device, validNewProvision("aspect-builder"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	p.mu.Lock()
	session := p.session
	p.mu.Unlock()

	observer := session.Attach(agoraio.AttachInfo{
		ClientID:     "operator-tui",
		Kind:         "tui",
		Capabilities: []contracts.Capability{contracts.CapObserver},
	}, attachReplayWindow)
	defer observer.Detach()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-observer.Events():
			if ev.Type != contracts.EvProvisioned {
				continue
			}
			var payload provisionedPayload
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("decode provisioned payload: %v", err)
			}
			if payload.IdentityFP != info.IdentityFP || payload.Profile != info.Profile {
				t.Fatalf("provisioned payload = %+v, want identity_fp=%q profile=%q", payload, info.IdentityFP, info.Profile)
			}
			return
		case <-deadline:
			t.Fatal("observer never saw the provisioned event within 2s")
		}
	}
}
