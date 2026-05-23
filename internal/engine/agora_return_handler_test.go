package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	bridle "github.com/CarriedWorldUniverse/bridle"

	"github.com/CarriedWorldUniverse/nexus/nexus/frame/funnel"
)

// fakeBus records SendChat invocations and optionally errors.
type fakeBus struct {
	calls    int
	lastBody string
	lastTo   int64
	err      error
}

func (f *fakeBus) SendChat(_ context.Context, content string, replyTo int64, _ string) (int64, error) {
	f.calls++
	f.lastBody = content
	f.lastTo = replyTo
	return 1, f.err
}

func newHandlerWithBus(bus busSender) *AgoraReturnHandler {
	return &AgoraReturnHandler{
		Bus:     bus,
		Program: nil, // tests that don't need UI side-effects pass nil; handler tolerates nil Program
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestHandle_TTYSource_DoesNotCallBusSendChat(t *testing.T) {
	bus := &fakeBus{}
	h := newHandlerWithBus(bus)
	err := h.Handle(context.Background(),
		funnel.DeliberateResult{TurnResult: bridle.TurnResult{FinalText: "panel reply"}},
		funnel.TurnTrigger{Source: SourceTTY, MsgID: 0})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if bus.calls != 0 {
		t.Fatalf("SendChat calls for TTY source: want 0, got %d", bus.calls)
	}
}

func TestHandle_ChatSource_CallsBusSendChat(t *testing.T) {
	bus := &fakeBus{}
	h := newHandlerWithBus(bus)
	err := h.Handle(context.Background(),
		funnel.DeliberateResult{TurnResult: bridle.TurnResult{FinalText: "hello peers"}},
		funnel.TurnTrigger{Source: SourceChat, MsgID: 42})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if bus.calls != 1 {
		t.Fatalf("SendChat calls: want 1, got %d", bus.calls)
	}
	if bus.lastBody != "hello peers" {
		t.Fatalf("SendChat body: want %q, got %q", "hello peers", bus.lastBody)
	}
	if bus.lastTo != 42 {
		t.Fatalf("SendChat replyTo: want 42, got %d", bus.lastTo)
	}
}

func TestHandle_ChatSource_BusErrorReturnsError(t *testing.T) {
	bus := &fakeBus{err: errors.New("broker rejected")}
	h := newHandlerWithBus(bus)
	err := h.Handle(context.Background(),
		funnel.DeliberateResult{TurnResult: bridle.TurnResult{FinalText: "hello"}},
		funnel.TurnTrigger{Source: SourceChat, MsgID: 1})
	if err == nil {
		t.Fatalf("Handle: want error, got nil")
	}
}

func TestHandle_EmptyFinalText_NoOp(t *testing.T) {
	bus := &fakeBus{}
	h := newHandlerWithBus(bus)
	err := h.Handle(context.Background(),
		funnel.DeliberateResult{TurnResult: bridle.TurnResult{FinalText: ""}},
		funnel.TurnTrigger{Source: SourceChat, MsgID: 1})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if bus.calls != 0 {
		t.Fatalf("SendChat calls for empty reply: want 0, got %d", bus.calls)
	}
}
