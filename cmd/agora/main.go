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

	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/provider/claudecode"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/CarriedWorldUniverse/agora/internal/bus"
	"github.com/CarriedWorldUniverse/agora/internal/engine"
	"github.com/CarriedWorldUniverse/agora/internal/inbox"
	"github.com/CarriedWorldUniverse/agora/internal/ui"
)

func main() {
	var (
		keyfilePath = flag.String("keyfile", "", "Path to aspect keyfile JSON (required)")
		logFile     = flag.String("log-file", "", "Write logs here; default /tmp/agora.log")
		stub        = flag.Bool("stub", false, "Use the StubTurn (no model); default false = bridle/claude-code")
		claudePath  = flag.String("claude", "claude", "Path to the claude binary (claudecode provider)")
		cwd         = flag.String("cwd", "", "Working directory for the claude-code subprocess; empty = inherit")
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

	// Wire OnChat after the program is built so we can call p.Send;
	// declared here so bus.Config can reference it.
	var p *tea.Program
	onChat := func(it inbox.Item) {
		if p == nil {
			return
		}
		p.Send(ui.ChatDelivered{
			From:       it.From,
			Content:    it.Content,
			MsgID:      it.MsgID,
			ReceivedAt: it.ReceivedAt,
		})
	}

	b, err := bus.Connect(rootCtx, bus.Config{
		KeyfilePath: *keyfilePath,
		Logger:      log,
		Inbox:       box,
		OnChat:      onChat,
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
	p = tea.NewProgram(model, tea.WithAltScreen())

	// Engine: pulls items off the inbox, runs a turn, routes the
	// reply by Source tag. Default to bridle/claude-code (NEX-59);
	// -stub falls back to engine.StubTurn for routing-only tests.
	var turn engine.TurnFunc
	if *stub {
		log.Info("engine: using StubTurn (-stub set)")
		turn = engine.StubTurn
	} else {
		provider := claudecode.New()
		provider.ClaudePath = *claudePath
		// Task spawns subagents whose lifetime is bound to the parent
		// claude -p process; the parent exits as soon as it produces
		// FinalText, killing any in-flight subagent before its work
		// returns. Disallow it under agora so the model doesn't pick
		// up the pattern. Revisit when the engine lifecycle changes.
		provider.ExtraArgs = append(provider.ExtraArgs, "--disallowedTools", "Task")
		providerID := bridle.ProviderID(b.Provider())
		if providerID == "" {
			providerID = "claude-code"
		}
		model := b.Model()
		if model == "" {
			model = "claude-opus-4-7"
		}
		log.Info("engine: using bridle/claude-code",
			"provider_id", providerID,
			"model", model,
			"claude_path", *claudePath,
			"cwd", *cwd)
		sysPrompt := b.SystemPrompt()
		log.Info("system prompt composed",
			"bytes", len(sysPrompt),
			"empty", sysPrompt == "")
		turn = engine.NewBridleTurn(engine.BridleConfig{
			Provider:     provider,
			ProviderID:   providerID,
			Model:        model,
			AspectID:     b.AspectName(),
			Cwd:          *cwd,
			SystemPrompt: sysPrompt,
		})
	}

	eng := engine.New(engine.Config{
		Inbox:   box,
		Bus:     b,
		Program: p,
		Logger:  log,
		Turn:    turn,
	})
	go eng.Run(rootCtx)

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
