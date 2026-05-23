package engine

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	bridle "github.com/CarriedWorldUniverse/bridle"

	"github.com/CarriedWorldUniverse/nexus/nexus/frame/funnel"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	f, err := funnel.New(funnel.Config{
		AspectID:     "test",
		AspectHome:   t.TempDir(),
		SystemPrompt: "test",
		Harness:      bridle.NewHarness(stubProvider{}),
		Provider:     "stub",
		Model:        "stub",
		ContextMode:  funnel.ContextStateless,
		Return:       funnel.NoopReturnHandler{},
		Runner:       funnel.NullRunner{},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("funnel.New: %v", err)
	}
	e, err := New(Config{
		Funnel: f,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return e
}

// TestNew_RequiresFunnelAndLogger verifies the Config validation
// added with the engine NPE-guard. Pre-fix, constructing an Engine
// with nil Funnel or nil Logger would NPE on first Run/drain/Receive
// (Run + drain log unconditionally; the docstring said "all fields
// required" but New() didn't enforce).
func TestNew_RequiresFunnelAndLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f, err := funnel.New(funnel.Config{
		AspectID: "test",
		Harness:  bridle.NewHarness(stubProvider{}),
		Provider: "stub",
		Model:    "m",
		Runner:   funnel.NullRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		cfg  Config
	}{
		{"no Funnel", Config{Logger: logger}},
		{"no Logger", Config{Funnel: f}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil {
				t.Errorf("expected error for %s; got nil", tc.name)
			}
		})
	}

	// Sanity: both fields set → no error.
	if _, err := New(Config{Funnel: f, Logger: logger}); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestReceive_TTYDedupeBlocksRepeatedContent(t *testing.T) {
	e := newTestEngine(t)
	item := bridle.InboxItem{From: "operator", Content: "hello world", Source: SourceTTY}

	e.Receive(item)
	if got := e.InboxLen(); got != 1 {
		t.Fatalf("first Receive: want inbox=1, got %d", got)
	}

	// Same content within window → dropped.
	e.Receive(item)
	if got := e.InboxLen(); got != 1 {
		t.Fatalf("duplicate Receive: want inbox=1 (dropped), got %d", got)
	}
}

func TestReceive_TTYDedupeAllowsDifferentContent(t *testing.T) {
	e := newTestEngine(t)
	e.Receive(bridle.InboxItem{From: "operator", Content: "first", Source: SourceTTY})
	e.Receive(bridle.InboxItem{From: "operator", Content: "second", Source: SourceTTY})
	if got := e.InboxLen(); got != 2 {
		t.Fatalf("two distinct TTY submissions: want inbox=2, got %d", got)
	}
}

func TestReceive_TTYDedupeExpiresAfterWindow(t *testing.T) {
	e := newTestEngine(t)
	item := bridle.InboxItem{From: "operator", Content: "hello", Source: SourceTTY}

	e.Receive(item)
	// Backdate the cached entry beyond the window so the next Receive
	// passes the freshness check. This is the unit-test analogue of
	// "operator resends the same line 16 minutes later" — accepted.
	hash := hashContent(item.Content)
	e.ttyMu.Lock()
	e.ttyHashes[hash] = time.Now().Add(-ttyDedupeWindow - time.Second)
	e.ttyMu.Unlock()

	e.Receive(item)
	if got := e.InboxLen(); got != 2 {
		t.Fatalf("post-window resend: want inbox=2, got %d", got)
	}
}

func TestReceive_TTYDedupeBoundedAtCap(t *testing.T) {
	e := newTestEngine(t)
	// Push more than cap distinct items so FIFO eviction kicks in.
	for i := 0; i < ttyDedupeCap+5; i++ {
		e.Receive(bridle.InboxItem{From: "operator", Content: distinctContent(i), Source: SourceTTY})
	}
	e.ttyMu.Lock()
	gotEntries := len(e.ttyHashes)
	gotOrder := len(e.ttyOrder)
	e.ttyMu.Unlock()
	if gotEntries != ttyDedupeCap {
		t.Fatalf("hash map size: want %d, got %d", ttyDedupeCap, gotEntries)
	}
	if gotOrder != ttyDedupeCap {
		t.Fatalf("hash order size: want %d, got %d", ttyDedupeCap, gotOrder)
	}
}

func TestReceive_ChatSourceNotDeduped(t *testing.T) {
	e := newTestEngine(t)
	item := bridle.InboxItem{From: "peer", Content: "ping", Source: SourceChat, MsgID: 1}
	e.Receive(item)
	// Chat-route peers re-asserting the same text is meaningful (real
	// retry / clarification). Engine dedupe must not eat it; funnel's
	// MsgID-based seenMsgIDs guard handles broker re-push instead.
	item.MsgID = 2
	e.Receive(item)
	if got := e.InboxLen(); got != 2 {
		t.Fatalf("two chat-source submissions with same text: want inbox=2, got %d", got)
	}
}

func TestReceive_TTYDedupeHitFiresOnDrop(t *testing.T) {
	var (
		gotReason    string
		gotFirstSeen time.Time
		gotCalls     int
	)
	cb := func(reason string, firstSeen time.Time) {
		gotCalls++
		gotReason = reason
		gotFirstSeen = firstSeen
	}
	f, err := funnel.New(funnel.Config{
		AspectID:     "test",
		AspectHome:   t.TempDir(),
		SystemPrompt: "test",
		Harness:      bridle.NewHarness(stubProvider{}),
		Provider:     "stub",
		Model:        "stub",
		ContextMode:  funnel.ContextStateless,
		Return:       funnel.NoopReturnHandler{},
		Runner:       funnel.NullRunner{},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("funnel.New: %v", err)
	}
	e, err := New(Config{
		Funnel: f,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnDrop: cb,
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}

	item := bridle.InboxItem{From: "operator", Content: "dup-test", Source: SourceTTY}
	e.Receive(item) // first — accepted
	if gotCalls != 0 {
		t.Fatalf("OnDrop fired on first Receive: %d calls", gotCalls)
	}
	e.Receive(item) // duplicate — dropped, OnDrop fires
	if gotCalls != 1 {
		t.Fatalf("OnDrop calls: want 1 got %d", gotCalls)
	}
	if gotReason != "tty-duplicate" {
		t.Fatalf("OnDrop reason: want tty-duplicate got %q", gotReason)
	}
	if gotFirstSeen.IsZero() {
		t.Fatalf("OnDrop firstSeen was zero")
	}
}

func distinctContent(i int) string {
	return "msg-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i/26%10))
}

// stubProvider is a no-op bridle.Provider used to satisfy funnel.New's
// required Harness. Receive-path tests never trigger Deliberate, so
// the provider is never invoked.
type stubProvider struct{}

func (stubProvider) Name() bridle.ProviderID { return "stub" }
func (stubProvider) Capabilities() bridle.ProviderCapabilities {
	return bridle.ProviderCapabilities{}
}
func (stubProvider) RunTurn(_ context.Context, _ bridle.ProviderRequest, _ bridle.EventSink) (bridle.ProviderResult, error) {
	return bridle.ProviderResult{}, nil
}
