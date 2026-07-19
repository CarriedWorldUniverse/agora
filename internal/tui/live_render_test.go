package tui

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	"github.com/CarriedWorldUniverse/agora/internal/turnengine"
	"github.com/CarriedWorldUniverse/bridle/provider/claudesdk"
)

// TestLiveRender drives the REAL in-process backend + a real Claude turn
// through the Model's handleEvent with a capturing Printer, printing exactly
// what would reach the terminal transcript. Gated behind AGORA_LIVE=1 (bills a
// subscription turn); run: AGORA_LIVE=1 CLAUDE_CODE_OAUTH_TOKEN=... go test
// ./internal/tui -run TestLiveRender -v
func TestLiveRender(t *testing.T) {
	if os.Getenv("AGORA_LIVE") != "1" {
		t.Skip("set AGORA_LIVE=1 (bills a live turn)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cwd, _ := os.Getwd()
	roots, err := toolrunner.NewRoots(cwd)
	if err != nil {
		t.Fatal(err)
	}
	store := persistence.NewMemStore()
	_ = store.Create(contracts.ThreadMeta{ThreadID: "lr2", CreatedAt: time.Now().UTC(), Profile: "dev", WorkingDir: roots.WorkingDir})
	provider := claudesdk.New()
	mgr := turnengine.NewManager("lr2", provider, turnengine.WithRoots(roots), turnengine.WithStore(store))
	sess := agoraio.NewSession(ctx, "lr2", mgr)
	att := sess.Attach(agoraio.AttachInfo{ClientID: "lr2", Kind: "tui", Capabilities: []contracts.Capability{contracts.CapInteractive, contracts.CapApprover}}, 0)
	backend := NewLocalBackend(sess, att)
	defer backend.Close()

	var printed []string
	m := NewModel(Config{Backend: backend, AgentID: "agora", Theme: PlainTheme(), Printer: capturingPrinter(&printed), Now: func() time.Time { return time.Unix(0, 0).UTC() }})

	if err := backend.Send(ctx, contracts.Input{Type: contracts.InUserMessage, Text: "Reply with exactly the single word: PONG"}); err != nil {
		t.Fatal("send:", err)
	}

	done := false
	for !done {
		select {
		case <-ctx.Done():
			t.Fatal("timeout waiting for turn")
		case ev := <-backend.Events():
			cmds := m.handleEvent(ev)
			for _, c := range cmds { // execute so any non-Printer cmd runs; Printer already recorded
				if c != nil {
					_ = c()
				}
			}
			t.Logf("event=%s", ev.Type)
			if ev.Type == contracts.EvTurnCompleted || ev.Type == contracts.EvTurnFailed {
				done = true
			}
		}
	}

	fmt.Println("=== PRINTER (transcript) lines ===")
	for i, l := range printed {
		fmt.Printf("%2d| %q\n", i, l)
	}
	fmt.Printf("=== %d transcript lines; View() tail = %q ===\n", len(printed), m.View())
}
