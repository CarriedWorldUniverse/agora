// TestFlowPodProvision (blueprint §3.6): drives internal/pod.Pod THROUGH
// the daemon's own pod-mode wiring (daemon.Daemon.PodMode, internal/daemon/
// pod.go — fix finding #3) rather than constructing pod.NewPod directly:
// the *pod.Pod under test shares the daemon's clock/store/questions, so a
// bug in THAT wiring (wrong store, a second independent QuestionLog, a
// stale clock) is now detectable here — the daemon-assembly proof this
// flow exists for, not a near-duplicate of U17's own pod-package e2e. The
// Pod itself is then driven through its own public Go API (Provision then
// RunTurn) exactly as the real dispatch controller would (pod/turn_test.go's
// stubBroker shape) — NOT exec-CLI (blueprint §6 resolution 4).
//
// Two constraints this drive works within, both discovered grounding the
// blueprint against the actual (frozen, must-not-touch) internal/pod
// package:
//
//  1. The question's id is minted by planning.QuestionLog.Ask (crypto/rand,
//     same as question_park_resume) — not deterministic, so (like that
//     flow) this one asserts structurally (type sequence + every OTHER
//     field byte-exact) rather than a full byte match.
//  2. internal/pod exposes no second-attachment/observer hook (Pod.session
//     is unexported, and pod is itself a merged package this unit must not
//     touch) — so the "wire stream" this drive can independently observe is
//     exactly TurnResult.Events ([provisioned, item.started]; RunTurn's own
//     ladder-conversion logic intercepts question.asked BEFORE appending it
//     to Events, by design — turn.go). The fixture's third line
//     (question.asked) is reconstructed from TurnResult.Blocked.Question —
//     the REAL decoded broadcast payload RunTurn itself read off the wire
//     (turn.go: `json.Unmarshal(ev.Payload, &q)`), re-serialized with the
//     same struct and marshal path, not fabricated content.
//  3. daemon.PodMode is CONTROL-LEVEL wiring only (Provision/RunTurn) —
//     internal/pod exposes no second-attachment/observer hook to drive
//     multi-attach streaming through the daemon's session-protocol wire,
//     and adding one is a pod API change out of this unit's scope.
package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/daemon"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/pod"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
)

type podFlowIdentities struct{}

func (podFlowIdentities) Resolve(ref string) (contracts.Identity, error) {
	if ref == "keyring:anvil" {
		return contracts.Identity{ID: "anvil", Fingerprint: "agora:k5xw3zjanfzsa2lt", Kind: contracts.IdentityAspect, Source: ref}, nil
	}
	return contracts.Identity{}, fmt.Errorf("conformance: unknown identity source %q", ref)
}

var flowPodQuestion = contracts.QuestionArgs{
	Text:    "the ticket doesn't say which environment to deploy to — staging or prod?",
	Options: []contracts.QuestionOption{{Label: "staging"}, {Label: "prod"}},
}

func TestFlowPodProvision(t *testing.T) {
	fixture := loadFlow(t, "pod_provision.jsonl")

	ctx := context.Background()
	store := persistence.NewMemStore()
	threadID := "th_pod0001"
	if err := store.Create(contracts.ThreadMeta{ThreadID: threadID, CreatedAt: time.Unix(0, 0).UTC(), Profile: "aspect-builder"}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	// The engine stands in for the model (pod's own e2e_test.go house
	// pattern, TestE2E_StubBroker_ProvisionTurnBlockedRoundTrip): it emits
	// item.started(agent_message) then question.asked(blocking:true) — the
	// TRIGGER content is pre-canned (there is no model in this test), but
	// everything downstream (the ladder resolution to blocked:needs-input,
	// the wire shapes) is the REAL internal/pod + internal/planning seam.
	engine := &agoraio.ScriptedEngine{Script: []agoraio.ScriptedTurn{{Events: []contracts.Event{
		{Type: contracts.EvItemStarted, ThreadID: threadID, Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemAgentMessage}},
		{Type: contracts.EvQuestionAsked, ThreadID: threadID, Payload: mustMarshalJSON(contracts.QuestionAsked{
			ID: "ignored-on-the-wire", Source: contracts.QuestionFromAgent, Blocking: true, Args: flowPodQuestion,
		})},
	}}}}

	// REAL daemon-level wiring — d.PodMode constructs the *pod.Pod sharing
	// d's own clock/store/questions (internal/daemon/pod.go, fix #3), not a
	// pod.NewPod built fresh from scratch here.
	d := daemon.NewDaemon(ctx, daemon.Config{
		Clock: func() time.Time { return time.Unix(0, 0).UTC() },
		Store: store,
	})
	p := d.PodMode(podFlowIdentities{}, func(contracts.Identity, string) agoraio.Engine { return engine })

	if got := p.State(); got != pod.StateBlank {
		t.Fatalf("fresh pod state = %q, want blank", got)
	}

	device := remote.Device{ID: "nexus-dispatch", Capabilities: []contracts.Capability{contracts.CapAdmin, contracts.CapInteractive, contracts.CapObserver}}
	msg := contracts.Provision{Profile: "aspect-builder", Session: contracts.ProvisionSession{Resume: threadID}}
	msg.Identity.Source = "keyring:anvil"

	// REAL seam call — Pod.Provision (atomic apply-all-or-reject).
	info, err := p.Provision(ctx, device, msg)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if info.ThreadID != threadID || info.Profile != "aspect-builder" {
		t.Fatalf("provisioned info = %+v", info)
	}

	// REAL seam call — Pod.RunTurn, which internally routes the blocking
	// question through planning.QuestionLog.Ask(ContextDispatchPod) and
	// converts it to blocked:needs-input (the ladder's real
	// DispositionDieHonestly resolution, not a stub).
	result, err := p.RunTurn(ctx, "deploy the release")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if result.Blocked == nil {
		t.Fatal("result.Blocked = nil, want a real BlockedNeedsInput — the turn asked a blocking question")
	}
	if got := p.State(); got != pod.StateProvisioned {
		t.Fatalf("pod state after a blocked turn = %q, want still provisioned (warm-pool — only the turn dies honestly)", got)
	}

	// (b) the direct Go assertion blueprint §3.6 calls for — BlockedNeedsInput
	// is a Go return value of RunTurn, never a wire Event (there is no
	// EvBlockedNeedsInput type in contracts/event.go).
	if result.Blocked.ThreadID != threadID {
		t.Fatalf("Blocked.ThreadID = %q, want %q", result.Blocked.ThreadID, threadID)
	}
	if result.Blocked.Question.Args.Text != flowPodQuestion.Text {
		t.Fatalf("Blocked.Question.Args.Text = %q, want %q", result.Blocked.Question.Args.Text, flowPodQuestion.Text)
	}
	if result.Blocked.Question.ID == "" {
		t.Fatal("real QuestionLog.Ask minted an empty question id")
	}

	// (a) the wire stream: result.Events IS the real observable stream
	// (provisioned + item.started); question.asked is reconstructed from
	// the real Blocked.Question per this file's header comment.
	got := append(append([]contracts.Event{}, result.Events...),
		contracts.Event{Type: contracts.EvQuestionAsked, ThreadID: threadID, Payload: mustMarshalJSON(result.Blocked.Question)},
	)
	assertPodFlowStructurallyMatches(t, got, fixture, result.Blocked.Question.ID)
}

func assertPodFlowStructurallyMatches(t *testing.T, got, want []contracts.Event, questionID string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot:  %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i].Type != want[i].Type || got[i].ThreadID != want[i].ThreadID {
			t.Fatalf("line %d: (type,thread_id) = (%s,%s), want (%s,%s)", i+1, got[i].Type, got[i].ThreadID, want[i].Type, want[i].ThreadID)
		}
		// finding #6(a): assert TurnID + Item per line too — a wrong
		// Item.Seq/Type (both payloads nil, e.g. on item.started) or a
		// wrong TurnID previously passed silently.
		if got[i].TurnID != want[i].TurnID {
			t.Fatalf("line %d: turn_id = %q, want %q", i+1, got[i].TurnID, want[i].TurnID)
		}
		if !itemRefsEqual(got[i].Item, want[i].Item) {
			t.Fatalf("line %d: item = %+v, want %+v", i+1, got[i].Item, want[i].Item)
		}
		if got[i].Type != contracts.EvQuestionAsked {
			if string(got[i].Payload) != string(want[i].Payload) {
				t.Fatalf("line %d: payload = %s, want %s", i+1, got[i].Payload, want[i].Payload)
			}
			continue
		}
		var g, w contracts.QuestionAsked
		mustDecode(t, got[i].Payload, &g)
		mustDecode(t, want[i].Payload, &w)
		if g.ID != questionID {
			t.Fatalf("line %d: question.asked id %q != real minted id %q", i+1, g.ID, questionID)
		}
		g.ID = w.ID // the only field allowed to differ
		gj, _ := json.Marshal(g)
		wj, _ := json.Marshal(w)
		if string(gj) != string(wj) {
			t.Fatalf("line %d: question.asked content mismatch (ignoring id)\ngot:  %s\nwant: %s", i+1, gj, wj)
		}
	}
}
