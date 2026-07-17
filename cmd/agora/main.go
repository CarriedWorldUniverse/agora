// Command agora is the operator-facing interactive client: a lean
// bubbletea TUI (internal/tui, agora-spec-tui.md) speaking to a local
// `agora daemon` over the session protocol (internal/io, agora-spec-io.md
// §0a/§2).
//
// v0-legacy retirement (U15, agora-spec-build.md §1): the previous
// broker-mediated, multi-aspect chat TUI (internal/ui + internal/opclient)
// is retired from `main` as of this unit — internal/ui is deleted; the
// `v0-legacy` git branch remains the runnable reference per the U1 cut.
// internal/opclient is untouched (nothing in this unit needs it deleted;
// it was already a self-contained package with its own tests and no other
// caller) — see the build report for exactly what still references it.
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

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/tui"
	"github.com/CarriedWorldUniverse/agora/internal/version"
)

func defaultSocketPath() string {
	return filepath.Join(userHomeOrDot(), ".agora", "agora.sock")
}

func main() {
	// arg0-style dispatch (U18, blueprint §6 q4): `agora daemon` boots the
	// internal/daemon runtime instead of the TUI client; bare `agora` (or
	// any other first arg) is unaffected — the client's own flag set never
	// sees "daemon" as a stray positional argument because this check runs
	// before flag.Parse() below.
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		runDaemon(os.Args[2:])
		return
	}

	var (
		socketPath  = flag.String("socket", defaultSocketPath(), "agora daemon unix socket path")
		wsURL       = flag.String("ws", "", "agora daemon session-protocol websocket URL (overrides -socket if set)")
		threadID    = flag.String("thread", "default", "thread to attach to")
		clientID    = flag.String("client-id", "", "client id to attach as (default: generated)")
		agentID     = flag.String("agent", "agora", "agent id shown in the session header")
		model       = flag.String("model", "", "model shown in the session header/status row")
		stateDir    = flag.String("state-dir", filepath.Join(userHomeOrDot(), ".agora"), "directory for client state")
		logFile     = flag.String("log-file", "", "write logs here; default /tmp/agora.log")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("agora %s\n", version.Version)
		return
	}

	logPath := *logFile
	if logPath == "" {
		logPath = "/tmp/agora.log"
	}
	logCloser, log, logHandle, err := openLogger(logPath)
	if err != nil {
		emitExit(nil, exitBadFlags, fmt.Sprintf("open log: %v", err), 2)
	}
	defer logCloser()

	defer restoreTerminalEscapes()
	installCrashCapture(logHandle, log)
	defer func() {
		if r := recover(); r != nil {
			emitExit(log, exitPanic, fmt.Sprintf("%v", r), 1)
		}
	}()

	if *clientID == "" {
		*clientID = "tui-" + uuid.NewString()
	}
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		emitExit(log, exitBadFlags, fmt.Sprintf("mkdir state dir: %v", err), 2)
	}

	log.Info("agora starting", "socket", *socketPath, "ws", *wsURL, "thread", *threadID, "client_id", *clientID, "log_file", logPath)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	attach := agoraio.AttachRequest{
		ThreadID: *threadID,
		ClientID: *clientID,
		Kind:     "tui",
		Capabilities: []contracts.Capability{
			contracts.CapInteractive,
			contracts.CapApprover,
		},
		Replay: 200,
	}

	backend, err := dialBackend(rootCtx, *socketPath, *wsURL, attach)
	if err != nil {
		emitExit(log, exitDaemonConnect, err.Error(), 1)
	}
	defer backend.Close()

	m := tui.NewModel(tui.Config{
		Backend: backend,
		AgentID: *agentID,
		Model:   *model,
	})
	// Never tea.WithAltScreen() (§0 non-negotiable: the transcript lives in
	// the terminal's own scrollback, not a full-screen widget).
	// WithMouseCellMotion forwards wheel events to the composer/modal.
	// Text selection under mouse capture: use the clipboard yank binding,
	// or the emulator's shift+drag override.
	p := tea.NewProgram(m, tea.WithMouseCellMotion())

	signalReceived := ""
	{
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			defer recoverGoroutine("signal-handler", log, nil)
			s := <-sigCh
			signalReceived = s.String()
			p.Send(tea.Quit())
			go func() {
				defer recoverGoroutine("signal-kill-backstop", log, nil)
				time.Sleep(signalKillGrace)
				log.Warn("agora: graceful shutdown didn't complete; force-killing program",
					"signal", s.String(), "grace", signalKillGrace)
				p.Kill()
			}()
		}()
	}

	restoreStderr, redirErr := redirectStderr(logHandle)
	if redirErr != nil {
		log.Warn("agora: stderr redirect failed; panic stacks may still corrupt screen", "err", redirErr)
	}

	if _, err := p.Run(); err != nil {
		restoreStderr()
		cancel()
		emitExit(log, exitBubbleteaError, err.Error(), 1)
	}
	restoreStderr()

	log.Info("agora shutting down")
	cancel()
	_ = backend.Close()

	if signalReceived != "" {
		emitExit(log, exitSignal, signalReceived, 0)
	}
	emitExit(log, exitClean, "", 0)
}

// dialBackend picks the transport: an explicit -ws URL wins, otherwise the
// unix socket path. Kept as its own function so it's the one place that
// changes if/when `agora daemon` (U18) adds e.g. TLS or auth to the dial.
func dialBackend(ctx context.Context, socketPath, wsURL string, attach agoraio.AttachRequest) (tui.Backend, error) {
	if wsURL != "" {
		return tui.DialWSBackend(ctx, wsURL, attach)
	}
	return tui.DialUnixBackend(socketPath, attach)
}

func userHomeOrDot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return home
}

// openLogger opens the log file (append, mode 0600, secrets-aware)
// and wraps it in a slog.Logger. Returns the underlying *os.File
// alongside the slog.Logger so callers can also redirect stderr to
// the same file during TUI mode (see redirectStderr). The closer
// the caller defers closes the file.
func openLogger(path string) (func(), *slog.Logger, *os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return func() {}, nil, nil, err
	}
	log := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return func() { _ = f.Close() }, log, f, nil
}

// signalKillGrace is how long the signal handler waits after sending
// tea.Quit before force-killing the bubbletea program.
const signalKillGrace = 2 * time.Second

// Exit-reason constants used by emitExit.
const (
	exitClean          = "clean"
	exitBadFlags       = "bad-flags"
	exitDaemonConnect  = "daemon-connect"
	exitBubbleteaError = "bubbletea-error"
	exitSignal         = "signal"
	exitPanic          = "panic"
)

// emitExit prints the structured exit-reason line to stderr, logs it,
// restores terminal escapes, and exits with the given code.
func emitExit(log *slog.Logger, reason, detail string, code int) {
	restoreTerminalEscapes()
	line := fmt.Sprintf("agora: exit reason=%s code=%d", reason, code)
	if detail != "" {
		line += " · " + detail
	}
	fmt.Fprintln(os.Stderr, line)
	if log != nil {
		log.Info("agora exit", "reason", reason, "detail", detail, "code", code)
	}
	os.Exit(code)
}
