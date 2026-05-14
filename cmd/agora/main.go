// Command agora is the operator-facing CLI for the nexus cluster.
//
// Connects to nexus over WS, opens a TUI chat panel, lets the operator
// type into the aspect's inbox, renders incoming chat in real time,
// and spawns a per-turn engine (claude-code subprocess via bridle)
// when the inbox has work.
//
// See docs/spec.md for the full architecture + behaviour rules.
//
// NEX-51: v0 skeleton (alt-screen, ctrl-c, keyfile load).
// NEX-52: WS intake — keyfile.Validate, wsasp.Client.Run, chat.deliver → inbox.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/CarriedWorldUniverse/agora/internal/bus"
	"github.com/CarriedWorldUniverse/agora/internal/inbox"
	"github.com/CarriedWorldUniverse/agora/internal/ui"
)

func main() {
	var (
		keyfilePath = flag.String("keyfile", "", "Path to aspect keyfile JSON (required)")
		logFile     = flag.String("log-file", "", "Write logs here; default /tmp/agora.log")
	)
	flag.Parse()

	if *keyfilePath == "" {
		fmt.Fprintln(os.Stderr, "agora: -keyfile required")
		os.Exit(2)
	}

	logPath := *logFile
	if logPath == "" {
		logPath = "/tmp/agora.log"
	}
	logCloser, log, err := openLogger(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agora: open log: %v\n", err)
		os.Exit(2)
	}
	defer logCloser()

	log.Info("agora starting", "keyfile", *keyfilePath, "log_file", logPath)

	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	box := inbox.New()

	b, err := bus.Connect(rootCtx, bus.Config{
		KeyfilePath: *keyfilePath,
		Logger:      log,
		Inbox:       box,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agora: bus connect: %v\n", err)
		log.Error("bus connect failed", "err", err)
		os.Exit(1)
	}

	// Run the WS lifecycle in a goroutine — bubbletea owns the main
	// goroutine for stdin. When the TUI exits, cancel propagates and
	// wsasp.Run returns.
	busDone := make(chan error, 1)
	go func() { busDone <- b.Run(rootCtx) }()

	cfg := ui.Config{
		Logger:       log,
		AspectID:     b.AspectName(),
		OperatorName: "operator",
		Inbox:        box,
	}

	model := ui.NewModel(cfg)
	p := tea.NewProgram(model, tea.WithAltScreen())

	// Inbox wake-ups → tea.Msg. Goroutine bridges the channel into
	// the bubbletea program so the UI repaints (and later, kicks the
	// engine) on every Push.
	go pumpInbox(rootCtx, box, p)

	if _, err := p.Run(); err != nil {
		log.Error("bubbletea program ended with error", "err", err)
		cancel()
		os.Exit(1)
	}
	log.Info("agora shutting down")
	cancel()
	if err := <-busDone; err != nil && rootCtx.Err() == nil {
		log.Error("bus exited with error", "err", err)
	}
}

// pumpInbox forwards inbox wake-ups to the bubbletea Program as
// ui.InboxUpdated messages. The UI's Update sees these and refreshes
// any inbox-derived view state (the status line counter today; the
// chat panel render in NEX-53).
func pumpInbox(ctx context.Context, box *inbox.Inbox, p *tea.Program) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-box.Updates():
			p.Send(ui.InboxUpdated{})
		}
	}
}

// openLogger opens the log file (append, mode 0600 — secrets-aware)
// and wraps it in a slog.Logger. Returns a closer the caller defers.
func openLogger(path string) (func(), *slog.Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return func() {}, nil, err
	}
	log := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return func() { _ = f.Close() }, log, nil
}
