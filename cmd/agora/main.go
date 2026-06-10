// Command agora is the operator-facing one-to-one client for always-on agents.
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

	"github.com/CarriedWorldUniverse/agora/internal/opclient"
	"github.com/CarriedWorldUniverse/agora/internal/ui"
	"github.com/CarriedWorldUniverse/agora/internal/version"
)

const defaultBrokerURL = "https://nexus.tail41686e.ts.net:7888"

func main() {
	var (
		brokerURL   = flag.String("broker", defaultBrokerURL, "Operator broker URL")
		agent       = flag.String("agent", "", "Agent name to open as dm:<agent> (required)")
		token       = flag.String("token", os.Getenv("AGORA_TOKEN"), "Operator JWT (defaults to AGORA_TOKEN)")
		stateDir    = flag.String("state-dir", filepath.Join(userHomeOrDot(), ".agora"), "Directory for cursor and client state")
		logFile     = flag.String("log-file", "", "Write logs here; default /tmp/agora.log")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("agora %s\n", version.Version)
		return
	}
	if *agent == "" {
		flag.Usage()
		emitExit(nil, exitBadFlags, "-agent required", 2)
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

	log.Info("agora starting", "broker", *brokerURL, "agent", *agent, "log_file", logPath, "state_dir", *stateDir)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := opclient.Dial(rootCtx, opclient.Config{
		BrokerURL: *brokerURL,
		Token:     *token,
		StateDir:  *stateDir,
	})
	if err != nil {
		emitExit(log, exitBrokerConnect, err.Error(), 1)
	}
	defer client.Close()

	model := ui.NewModel(ui.Config{
		Logger:       log,
		AspectID:     *agent,
		Agent:        *agent,
		OperatorName: "operator",
		Client:       client,
	})
	p := tea.NewProgram(model, tea.WithAltScreen())

	signalReceived := ""
	{
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			defer recoverGoroutine("signal-handler", log, nil)
			s := <-sigCh
			signalReceived = s.String()
			p.Send(ui.QuitGraceful{})
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
	_ = client.Close()

	if signalReceived != "" {
		emitExit(log, exitSignal, signalReceived, 0)
	}
	emitExit(log, exitClean, "", 0)
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
// QuitGraceful before force-killing the bubbletea program.
const signalKillGrace = 2 * time.Second

// Exit-reason constants used by emitExit.
const (
	exitClean          = "clean"
	exitBadFlags       = "bad-flags"
	exitBrokerConnect  = "broker-connect"
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
