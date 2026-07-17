package remote

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// TestReconnectHandshakeThenGapReplay is the end-to-end reconnect/replay
// path (spec §9): pair a device, complete an IK handshake, attach with the
// device's REGISTRY capabilities, watch some items go by, detach
// (simulated flaky link), then reattach and get exactly the gap via
// GapTracker fed from the session's own replay tail.
func TestReconnectHandshakeThenGapReplay(t *testing.T) {
	daemonKey := mustKey(t)
	deviceKey := mustKey(t)
	reg := NewRegistry(nil)
	fp := Fingerprint(deviceKey.PublicBytes())
	if _, err := reg.Enroll(fp, deviceKey.PublicBytes(), DeviceMetadata{DisplayName: "phone"}, []contracts.Capability{contracts.CapObserver, contracts.CapInteractive}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// 1. Handshake: re-handshake on every reconnect (spec §9: "no session-
	// ticket resumption in v1"). Epoch increments across reconnects so an
	// old session can't be replayed into a new one.
	handshake := func(epoch uint64) {
		prologue := Prologue{DaemonID: "daemon1", StreamID: "s1", Epoch: epoch}
		init, err := NewInitiatorHandshake(deviceKey, daemonKey.Public(), prologue)
		if err != nil {
			t.Fatalf("epoch %d: NewInitiatorHandshake: %v", epoch, err)
		}
		m1, err := init.Message1([]byte("tok"))
		if err != nil {
			t.Fatalf("epoch %d: Message1: %v", epoch, err)
		}
		responderPrologue := prologue
		responderPrologue.DeviceID = fp
		resp := NewResponderHandshake(daemonKey, responderPrologue, reg)
		deviceFP, _, m2, _, err := resp.Accept(m1)
		if err != nil {
			t.Fatalf("epoch %d: Accept: %v", epoch, err)
		}
		if deviceFP != fp {
			t.Fatalf("epoch %d: deviceFP mismatch", epoch)
		}
		if _, err := init.Complete(m2); err != nil {
			t.Fatalf("epoch %d: Complete: %v", epoch, err)
		}
	}
	handshake(1)

	// 2. Attach with registry-derived capabilities, not client-declared.
	device, _ := reg.Get(fp)
	engine := newTickEngine()
	sess := agoraio.NewSession(context.Background(), "th1", engine)
	defer sess.Close()

	info := AttachInfo(device, "tui")
	att := sess.Attach(info, 0)

	gap := NewGapTracker()
	var seen []contracts.Event
	drain := func(n int) {
		for i := 0; i < n; i++ {
			ev := <-att.Events()
			seen = append(seen, ev)
		}
	}

	engine.emit(evItem(1))
	engine.emit(evItem(2))
	drain(2)
	for _, ev := range seen {
		if ev.Item != nil {
			gap.Ack(fp, "th1", ev.Item.Seq)
		}
	}

	// 3. Flaky link: detach (simulating a dropped connection) while more
	// items are produced.
	att.Detach()
	engine.emit(evItem(3))
	engine.emit(evItem(4))
	time.Sleep(10 * time.Millisecond) // let the broadcaster append to backlog

	// 4. Reconnect: re-handshake (epoch bumps), reattach with a full
	// replay request, then filter to exactly the gap via GapTracker.
	handshake(2)
	att2 := sess.Attach(info, 64)
	defer att2.Detach()

	// Drain everything the fresh attach delivers (replay tail + presence)
	// into a snapshot, then compute the gap against it.
	var backlog []contracts.Event
	timeout := time.After(time.Second)
drain2:
	for {
		select {
		case ev := <-att2.Events():
			backlog = append(backlog, ev)
			if ev.Item != nil && ev.Item.Seq == 4 {
				break drain2
			}
		case <-timeout:
			break drain2
		}
	}

	replay, err := gap.Replay(fp, "th1", backlog)
	if err != nil {
		t.Fatalf("gap.Replay: %v", err)
	}
	var gotSeqs []int64
	for _, ev := range replay {
		if ev.Item != nil {
			gotSeqs = append(gotSeqs, ev.Item.Seq)
		}
	}
	if len(gotSeqs) != 2 || gotSeqs[0] != 3 || gotSeqs[1] != 4 {
		t.Fatalf("gap replay: got seqs %v, want [3 4] (exactly what was missed)", gotSeqs)
	}
}

// TestRevokeKillsSubsequentHandshakeMidFlight: a device that successfully
// handshakes, then gets revoked, cannot complete a NEW handshake
// afterward — "revocation ... refuses future handshakes" (spec §3).
func TestRevokeKillsSubsequentHandshakeMidFlight(t *testing.T) {
	daemonKey := mustKey(t)
	deviceKey := mustKey(t)
	reg := NewRegistry(nil)
	fp := Fingerprint(deviceKey.PublicBytes())
	if _, err := reg.Enroll(fp, deviceKey.PublicBytes(), DeviceMetadata{}, DefaultCapabilities()); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	attempt := func() error {
		prologue := Prologue{DaemonID: "d1", StreamID: "s1", Epoch: 1}
		init, err := NewInitiatorHandshake(deviceKey, daemonKey.Public(), prologue)
		if err != nil {
			return err
		}
		m1, err := init.Message1([]byte("tok"))
		if err != nil {
			return err
		}
		rp := prologue
		rp.DeviceID = fp
		resp := NewResponderHandshake(daemonKey, rp, reg)
		_, _, _, _, err = resp.Accept(m1)
		return err
	}

	if err := attempt(); err != nil {
		t.Fatalf("pre-revoke handshake: %v", err)
	}
	if err := reg.Revoke(fp); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := attempt(); !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("post-revoke handshake: got %v want ErrDeviceRevoked", err)
	}
}

// tickEngine is a manually-driven Engine for integration_test.go: emit
// pushes an event onto its out channel on demand instead of running a
// fixed script, so the test controls exact interleaving with
// attach/detach/reattach. out is set exactly once by Run and read by emit
// through a channel handoff (not a bare field) so -race sees no data race
// between the Run goroutine and the test goroutine calling emit.
type tickEngine struct {
	outCh chan chan<- contracts.Event
}

func newTickEngine() *tickEngine {
	return &tickEngine{outCh: make(chan chan<- contracts.Event, 1)}
}

func (e *tickEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	e.outCh <- out
	<-ctx.Done()
	close(out)
	return nil
}

func (e *tickEngine) emit(ev contracts.Event) {
	out := <-e.outCh
	out <- ev
	e.outCh <- out
}
