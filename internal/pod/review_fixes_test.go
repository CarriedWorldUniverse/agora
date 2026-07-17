package pod

// Regression tests for the U17 review gate (security-validator + Sonnet
// adversarial + DeepSeek-v4-pro).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
)

// blockingEngine emits nothing and blocks until its context is canceled —
// simulating an in-flight turn the engine has not yet produced output for.
type blockingEngine struct{}

func (blockingEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	<-ctx.Done()
	close(out)
	return ctx.Err()
}

// HIGH (Sonnet CONFIRMED repro) — RunTurn must not hang forever if Deprovision
// races a concurrent turn. The attach event channel is never closed on
// teardown, so a RunTurn blocked on it with a deadline-less caller ctx
// (context.Background, as this package's own callers use) would leak/hang. The
// fix ties RunTurn's wait to the session lifetime; Deprovision cancels it.
func TestRunTurn_DeprovisionRace_DoesNotHang(t *testing.T) {
	ctx := context.Background()
	p, _ := newTestPod(t, ctx, blockingEngine{})
	device := dispatchDevice("disp-race", remote.DeviceConstraints{})
	if _, err := p.Provision(ctx, device, validNewProvision("aspect-builder")); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(context.Background(), "long-running task")
		done <- err
	}()

	// Let RunTurn enter its read loop, then deprovision out from under it.
	time.Sleep(50 * time.Millisecond)
	if err := p.Deprovision(); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}

	select {
	case <-done:
		// Returned (with either ErrTurnAborted or ErrNotProvisioned depending
		// on who won the lock) — the point is it did NOT hang.
	case <-time.After(3 * time.Second):
		t.Fatal("RunTurn hung after Deprovision raced the turn (never returned within 3s)")
	}
	if p.State() != StateBlank {
		t.Fatalf("State = %q after Deprovision, want blank", p.State())
	}
}

// appendFailStore wraps a real store but fails Append (only), to exercise the
// mid-apply failure path in Provision's new-session branch.
type appendFailStore struct {
	contracts.ThreadStore
	fail bool
}

func (s *appendFailStore) Append(id string, items []contracts.ThreadItem) error {
	if s.fail {
		return errors.New("test: simulated durable-write failure")
	}
	return s.ThreadStore.Append(id, items)
}

// MED (security-validator + Sonnet, both CONFIRMED) — Provision claims
// apply-all-or-reject, but Create (new thread) then a failing Append left an
// orphaned zero-item ThreadMeta. The fix compensates with Delete on the
// new-thread Append-failure path.
func TestProvision_AppendFailure_NoOrphanThread(t *testing.T) {
	ctx := context.Background()
	base := persistence.NewMemStore()
	store := &appendFailStore{ThreadStore: base, fail: true}
	p := NewPod(ctx, Config{
		Clock:         fixedClock(time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)),
		Identities:    newTestIdentities(),
		Store:         store,
		Questions:     planning.NewQuestionLog(store),
		EngineFactory: constEngineFactory(&agoraio.ScriptedEngine{}),
	})
	device := dispatchDevice("disp-append-fail", remote.DeviceConstraints{})

	_, err := p.Provision(ctx, device, validNewProvision("aspect-builder"))
	if err == nil {
		t.Fatal("Provision succeeded despite Append failure, want error")
	}
	if p.State() != StateBlank {
		t.Fatalf("State = %q, want blank after a failed provision", p.State())
	}
	metas, lerr := base.List(contracts.ListFilter{})
	if lerr != nil {
		t.Fatalf("List: %v", lerr)
	}
	if len(metas) != 0 {
		t.Fatalf("orphaned thread left after Append failure: %d thread(s) in store, want 0 (apply-all-or-reject)", len(metas))
	}
}

// MED (Sonnet CONFIRMED) — a non-blocking (blocking:false) question raised
// during a dispatch-pod turn must still be durably recorded (TIQuestionAsked)
// so it can be answered out-of-band (§5: "skip straight to the queue"). The
// bug only appended it to the discarded in-memory Events slice.
func TestRunTurn_NonBlockingQuestion_RecordsAudit(t *testing.T) {
	ctx := context.Background()
	q := contracts.QuestionArgs{Text: "fyi: which changelog section?", FreeText: true}
	engine := &agoraio.ScriptedEngine{Script: []agoraio.ScriptedTurn{
		{Events: []contracts.Event{
			{Type: contracts.EvQuestionAsked, Payload: mustJSON(t, contracts.QuestionAsked{
				ID: "q_wire", Source: contracts.QuestionFromAgent, Blocking: false, Args: q,
			})},
			{Type: contracts.EvTurnCompleted, Payload: mustJSON(t, contracts.Usage{Input: 3})},
		}},
	}}
	p, store := newTestPod(t, ctx, engine)
	device := dispatchDevice("disp-nbq", remote.DeviceConstraints{})
	info, err := p.Provision(ctx, device, validNewProvision("aspect-builder"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	result, err := p.RunTurn(ctx, "write the changelog")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if result.Blocked != nil {
		t.Fatalf("Blocked = %+v, want nil — a non-blocking question must not terminate the turn", result.Blocked)
	}
	var sawQ bool
	for _, it := range threadItems(t, store, info.ThreadID) {
		if it.Type == contracts.TIQuestionAsked {
			sawQ = true
		}
	}
	if !sawQ {
		t.Fatal("non-blocking question was not durably recorded (no TIQuestionAsked item) — the queued answer has nothing to hang off of")
	}
}

// LOW/MED (security-validator) — a thread-scoped device (non-empty
// AllowedThreads) must not be able to mint brand-new threads via session.new;
// that escapes its leash and contradicts the file's own documented invariant.
// Fail-closed: refuse the whole provision, no mutation.
func TestProvision_ThreadScopedDevice_RefusesNewThread(t *testing.T) {
	ctx := context.Background()
	p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})
	device := dispatchDevice("disp-leashed", remote.DeviceConstraints{AllowedThreads: []string{"thread-T"}})

	_, err := p.Provision(ctx, device, validNewProvision("aspect-builder"))
	if !errors.Is(err, remote.ErrThreadNotAllowed) {
		t.Fatalf("err = %v, want remote.ErrThreadNotAllowed — a thread-leashed device must not create new threads", err)
	}
	if p.State() != StateBlank {
		t.Fatalf("State = %q, want blank", p.State())
	}
	metas, lerr := store.List(contracts.ListFilter{})
	if lerr != nil {
		t.Fatalf("List: %v", lerr)
	}
	if len(metas) != 0 {
		t.Fatalf("mutation on a refused provision: %d thread(s), want 0", len(metas))
	}
}
