// Command agora is the operator-facing CLI for the nexus cluster.
//
// Connects to nexus over WS, opens a TUI chat panel, lets the operator
// type into the funnel-backed deliberation engine, and surfaces
// results either to the bus (chat-source) or to the panel (tty-source).
//
// As of NEX-82, the deliberation engine is funnel.Funnel — agora
// consumes the same compaction/session-resolver/filter pipeline the
// rest of the network uses, and plugs in its source-aware routing
// via AgoraReturnHandler + UIHook (see internal/engine/).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/provider/claudecode"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/CarriedWorldUniverse/nexus/nexus/frame/funnel"

	"github.com/CarriedWorldUniverse/agora/internal/bus"
	"github.com/CarriedWorldUniverse/agora/internal/engine"
	"github.com/CarriedWorldUniverse/agora/internal/ui"
	"github.com/CarriedWorldUniverse/agora/internal/version"
)

func main() {
	var (
		keyfilePath = flag.String("keyfile", "", "Path to aspect keyfile JSON (required)")
		logFile     = flag.String("log-file", "", "Write logs here; default /tmp/agora.log")
		claudePath  = flag.String("claude", "claude", "Path to the claude binary (claudecode provider)")
		cwd         = flag.String("cwd", "", "Working directory for the claude-code subprocess (default: keyfile's parent directory, so claude-code auto-discovers .mcp.json there)")
		cursorDir   = flag.String("cursor-dir", "", "Directory for the per-aspect chat cursor file (default: keyfile's parent directory; falls back to ~/.agora if unresolvable). NEX-119: align with nexus-comms-mcp so swapping shadow surfaces resumes from the same point.")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("agora %s\n", version.Version)
		return
	}

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

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGTERM/SIGHUP from outside (kill, supervisor) → ask the UI to
	// exit gracefully via the program's message channel. Bubbletea
	// owns Ctrl-C internally; we don't NotifyContext on SIGINT here.
	var p *tea.Program
	{
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			<-sigCh
			if p != nil {
				p.Send(ui.QuitGraceful{})
			}
		}()
	}

	// Engine pointer (filled after we build it below) — bus.OnChat
	// needs to call engine.Receive when a chat.deliver arrives.
	var eng *engine.Engine

	onChat := func(it bus.ChatItem) {
		if p != nil {
			p.Send(ui.ChatDelivered{
				From:       it.From,
				Content:    it.Content,
				MsgID:      it.MsgID,
				ReceivedAt: it.ReceivedAt,
			})
		}
		if eng != nil {
			eng.Receive(bridle.InboxItem{
				From:       it.From,
				Content:    it.Content,
				MsgID:      it.MsgID,
				ThreadRoot: it.ThreadRoot,
				Source:     engine.SourceChat,
			})
		}
	}

	// Resolve cursor dir: explicit flag wins; otherwise default to the
	// keyfile's parent so the cursor lives beside identity material and
	// any sibling consumer (nexus-comms-mcp once NEX-119 lands) can pick
	// it up cleanly. bus.Connect falls back to ~/.agora if both this and
	// the keyfile dir are unresolvable.
	resolvedCursorDir := *cursorDir
	if resolvedCursorDir == "" {
		if abs, err := filepath.Abs(*keyfilePath); err == nil {
			resolvedCursorDir = filepath.Dir(abs)
		}
	}

	// Resolve cwd for the claude-code subprocess. Explicit -cwd wins;
	// otherwise default to the keyfile's parent directory. That parent
	// is where the operator's identity material lives — including the
	// aspect's .mcp.json, which claude-code auto-discovers from its
	// spawn cwd. NEX-132: without this default, agora was spawning
	// claude with empty cwd → no MCP discovery → shadow-under-agora
	// had no jira/imap/comms tools.
	resolvedCwd := *cwd
	if resolvedCwd == "" {
		if abs, err := filepath.Abs(*keyfilePath); err == nil {
			resolvedCwd = filepath.Dir(abs)
		}
	}

	b, err := bus.Connect(rootCtx, bus.Config{
		KeyfilePath: *keyfilePath,
		CursorDir:   resolvedCursorDir,
		Logger:      log,
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

	// Provider + system prompt construction
	provider := claudecode.New()
	provider.ClaudePath = *claudePath
	// Task subagents inherit claude -p's lifetime; SIGKILL'd at
	// FinalText emission. Disallow under agora; delegate parallelism
	// to worker aspects via chat per the work-routing policy.
	provider.DisallowedTools = append(provider.DisallowedTools, "Task")

	providerID := bridle.ProviderID(b.Provider())
	if providerID == "" {
		providerID = "claude-code"
	}
	modelID := b.Model()
	if modelID == "" {
		modelID = "claude-opus-4-7"
	}

	sysPrompt := engine.AppendAgoraConventions(b.SystemPrompt())
	log.Info("system prompt composed",
		"bytes", len(sysPrompt),
		"provider", providerID,
		"model", modelID,
		"cwd", resolvedCwd)

	// UI Program: build before constructing the return handler since
	// the handler needs to send tea.Msgs into it.
	cfg := ui.Config{
		Logger:       log,
		AspectID:     b.AspectName(),
		OperatorName: "operator",
		WSConnected:  b.Connected,
	}

	// onSubmit + inboxLen wired below once engine exists.
	model := ui.NewModel(cfg)
	p = tea.NewProgram(model, tea.WithAltScreen())

	// Funnel construction. AgoraReturnHandler routes by Source tag;
	// UIHook pipes bridle ModelChunks to the live-line.
	returnHandler := &engine.AgoraReturnHandler{
		Bus:     b,
		Program: p,
		Logger:  log,
	}
	hook := &engine.UIHook{Program: p}

	f, err := funnel.New(funnel.Config{
		AspectID:          b.AspectName(),
		AspectHome:        resolvedCwd,
		SystemPrompt:      sysPrompt,
		Harness:           bridle.NewHarness(provider),
		Provider:          providerID,
		Model:             modelID,
		ContextMode:       funnel.ContextGlobal,
		Return:            returnHandler,
		ObservabilityHook: hook,
		Runner:            funnel.NullRunner{},
		Logger:            log,
	})
	if err != nil {
		log.Error("funnel build failed", "err", err)
		fmt.Fprintf(os.Stderr, "agora: funnel build: %v\n", err)
		os.Exit(1)
	}

	eng = engine.New(engine.Config{
		Funnel: f,
		Logger: log,
	})
	go eng.Run(rootCtx)

	// Wire the UI → engine paths now that engine exists. p.Send
	// blocks before p.Run starts the runtime, so do this in a
	// goroutine — Run consumes the message once it begins. Briefly
	// races user input: if they hit Enter before this lands, onSubmit
	// is nil and the keystroke is dropped. Acceptable (<1ms window).
	go func() {
		p.Send(ui.RegisterSubmit{
			OnSubmit: func(text string) {
				eng.Receive(bridle.InboxItem{
					From:    cfg.OperatorName,
					Content: text,
					Source:  engine.SourceTTY,
				})
			},
			InboxLen: eng.InboxLen,
		})
	}()

	if _, err := p.Run(); err != nil {
		log.Error("bubbletea program ended with error", "err", err)
		cancel()
		os.Exit(1)
	}

	// Graceful-shutdown tail: UI has exited (either /exit, first
	// Ctrl-C, second Ctrl-C, or SIGTERM). Send the deregister frame
	// best-effort then cancel rootCtx so the bus goroutine returns.
	log.Info("agora shutting down")
	dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := b.Deregister(dctx, "graceful shutdown"); err != nil {
		log.Warn("deregister failed", "err", err)
	} else {
		log.Info("deregister sent")
	}
	dcancel()

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
