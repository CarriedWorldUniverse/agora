// Command agora is the operator-facing interactive client: a lean
// bubbletea TUI (internal/tui, agora-spec-tui.md) speaking to a local
// `agora daemon` over the session protocol (internal/io, agora-spec-io.md
// §0a/§2).
//
// v0-legacy retirement (U15, agora-spec-build.md §1): the previous
// broker-mediated, multi-aspect chat TUI (internal/ui + internal/opclient)
// is retired from `main` — both packages are now deleted (internal/ui at
// U15, internal/opclient at agora#139, which also dropped the
// CarriedWorldUniverse/nexus module it was the last importer of). The
// `v0-legacy` line remains the runnable reference per the U1 cut.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	// Quiet the Node sidecar's process warnings (bridle spawns the
	// claude-sdk sidecar inheriting our env — claudesdk.go's
	// scrubAuthEnv(os.Environ()) — and NODE_NO_WARNINGS is not scrubbed).
	// The SDK emits a benign every-turn warning (CLAUDE_SDK_CAN_USE_TOOL_SHADOWED)
	// because bridle registers its tools in allowedTools and gates them via its
	// OWN BeforeToolCall hook, so the SDK's canUseTool is intentionally shadowed
	// — not an error, but it lands on the sidecar's stderr and agora surfaces
	// stderr as a scary "error:" line on an otherwise-successful turn. Silence
	// it at the source; leave any operator-set value untouched.
	if os.Getenv("NODE_NO_WARNINGS") == "" {
		_ = os.Setenv("NODE_NO_WARNINGS", "1")
	}
	// arg0-style dispatch (U18, blueprint §6 q4): `agora daemon` boots the
	// internal/daemon runtime instead of the TUI client; bare `agora` (or
	// any other first arg) is unaffected — the client's own flag set never
	// sees "daemon" as a stray positional argument because this check runs
	// before flag.Parse() below.
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		runDaemon(os.Args[2:])
		return
	}
	// `agora doctor` (NEX-790): a live-turn preflight — checks the sidecar,
	// Node, and ambient Claude credentials, exits non-zero on any FAIL.
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		runDoctor(os.Args[2:])
		return
	}
	// `agora pipe` (agora-spec-io.md §1): the one-shot/chainable JSONL
	// stdin/stdout entry over the SAME in-process real engine bare `agora`
	// falls back to — see pipe.go.
	if len(os.Args) > 1 && os.Args[1] == "pipe" {
		runPipe(os.Args[2:])
		return
	}
	// `agora workflow run|list`: the CLI entry point onto the starlark
	// workflow engine (internal/workflow, agora-spec-workflows.md).
	if len(os.Args) > 1 && os.Args[1] == "workflow" {
		runWorkflowCmd(os.Args[2:])
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
		demo        = flag.Bool("demo", false, "play a scripted zero-cost turn (no model, no billing) to test/debug rendering")
		mode        = flag.String("mode", "", "approval posture for this session (overrides permission_mode in .agora/config.json)")
	)
	flag.Parse()

	applyModeFlag(*mode)

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
	// Per-directory default thread. The Claude Code SDK stores each
	// conversation under ~/.claude/projects/<cwd>/, so a single global
	// "default" thread breaks the moment you run agora in another directory:
	// its derived session id was created under the FIRST dir, and resuming it
	// from a new cwd throws "No conversation found with session ID: ...". When
	// -thread is left at its default, derive a stable per-cwd thread id so each
	// directory gets its own thread + session + store, matching the SDK's
	// project layout. An explicit -thread <name> still forces a shared thread.
	if *threadID == "default" {
		if cwd, werr := os.Getwd(); werr == nil {
			*threadID = cwdThreadID(cwd)
		}
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

	var backend tui.Backend
	if *demo {
		log.Info("agora: demo mode (scripted turn, no model, no billing)")
		fmt.Fprintln(os.Stderr, "agora: demo mode — scripted turn, no model called, nothing billed.")
		backend, err = newDemoBackend(rootCtx, attach)
	} else {
		backend, err = dialBackend(rootCtx, log, *socketPath, *wsURL, attach)
	}
	if err != nil {
		emitExit(log, exitDaemonConnect, err.Error(), 1)
	}
	defer backend.Close()

	// NEX-798 resume: a thread with prior history prints its trailing
	// exchanges into terminal scrollback BEFORE the TUI starts — the inline
	// no-altscreen design (§0) makes pre-printed history read exactly like
	// the live transcript above the session. In-process backend only (a
	// daemon backend has no store handle here; its Replay covers live ring
	// history instead).
	printResumeHistory(backend, *threadID)

	m := tui.NewModel(tui.Config{
		Backend: backend,
		AgentID: *agentID,
		// resolveModel adds the default_model config fallback; tui.NewModel
		// then does the registry lookup (it needs the resolved entry for
		// /model and pricing anyway, so resolving the SPEC here too would
		// duplicate that work rather than save it).
		Model:    resolveModel(*model, mustGetwd()),
		ThreadID: *threadID,
		// Lets the TUI recognise its OWN client.attached event and warn if
		// the backend granted capabilities that cannot send input.
		ClientID:    *clientID,
		ListServers: listMCPServers,
		// /hooks: which lifecycle hooks were discovered and whether trust
		// lets them fire — fail-closed trust is invisible without this.
		ListHooks: listHooks,
		// /permissions: inspect and revoke the approval grants that outlive
		// this session. A durable permission store the operator cannot see
		// into would be a liability.
		ListPermissions:  listPermissions,
		RevokePermission: revokePermission,
		// /mode: the posture actually in force, resolved by the SAME
		// function the engine seam uses so the two cannot disagree.
		PermissionMode: resolvePermissionMode(mustGetwd()),
		ModeCatalog:    modeCatalog,
	})
	// Never tea.WithAltScreen() (§0 non-negotiable: the transcript lives in
	// the terminal's own scrollback, not a full-screen widget) — and NO mouse
	// capture. The TUI is keyboard-driven and handles no tea.MouseMsg, so
	// grabbing the mouse (tea.WithMouseCellMotion) would only break the
	// terminal's native select-to-copy and scrollback — which §0's
	// scrollback-transcript design depends on — for zero functional gain.
	p := tea.NewProgram(m)

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

// dialBackend picks the transport: an explicit -ws URL wins (an explicit
// remote target — never falls back to in-process, per U-E1's brief),
// otherwise it tries the local daemon's unix socket first and falls back to
// running the turn engine IN-PROCESS (newInProcessBackend, U-E1) when no
// daemon is reachable there. Kept as its own function so it's the one place
// that changes if/when `agora daemon` (U18) adds e.g. TLS or auth to the
// dial, and so the dial-then-fallback DECISION (isNoDaemonErr, dial.go) is
// exercised here exactly once, in one place.
func dialBackend(ctx context.Context, log *slog.Logger, socketPath, wsURL string, attach agoraio.AttachRequest) (tui.Backend, error) {
	if wsURL != "" {
		return tui.DialWSBackend(ctx, wsURL, attach)
	}

	backend, dialErr := tui.DialUnixBackend(socketPath, attach)
	if dialErr == nil {
		log.Info("agora: attached to daemon", "socket", socketPath)
		fmt.Fprintf(os.Stderr, "agora: attached to daemon at %s\n", socketPath)
		return backend, nil
	}
	if !isNoDaemonErr(dialErr) {
		// A genuine auth/protocol error talking to something that DID
		// accept the connection — surfacing that (instead of silently
		// masking it behind an in-process run against a totally different
		// engine) is the whole point of this classification; see
		// isNoDaemonErr's doc comment.
		return nil, dialErr
	}
	// This is the normal single-user path (no daemon running) — run the turn
	// engine in-process. Keep the raw dial error in the log for debugging, but
	// the stderr line stays reassuring: a missing socket is expected, not a fault.
	log.Info("agora: running standalone (no daemon); engine in-process", "socket", socketPath, "dial_err", dialErr.Error())
	fmt.Fprintln(os.Stderr, "agora: running standalone (no daemon) — engine in-process.")
	return newInProcessBackend(ctx, attach.ThreadID, attach)
}

// resumeHistoryK is how many trailing text messages a resumed session prints
// into scrollback — enough to re-orient without re-dumping the whole thread.
const resumeHistoryK = 12

// printResumeHistory writes a resumed thread's trailing exchanges to stdout
// before the TUI starts (NEX-798). No-op when the backend can't serve history
// (daemon/ws) or the thread is fresh.
func printResumeHistory(backend tui.Backend, threadID string) {
	hb, ok := backend.(*inProcessBackend)
	if !ok {
		return
	}
	entries, elided, err := hb.HistoryTail(threadID, resumeHistoryK)
	if err != nil || len(entries) == 0 {
		return
	}
	fmt.Printf("── resumed thread %s ──\n", threadID)
	if elided > 0 {
		fmt.Printf("  … %d earlier message(s) not shown\n", elided)
	}
	for _, e := range entries {
		if e.Role == "user" {
			fmt.Println("› " + e.Text)
		} else {
			fmt.Println(e.Text)
		}
	}
	fmt.Println("── end of history ──")
}

// cwdThreadID derives a stable, filesystem-safe thread id from a working
// directory — a sanitized basename (readable in the ~/.agora store) plus a
// short hash of the ABSOLUTE path (uniqueness; two dirs with the same basename
// don't collide). Deterministic: the same directory always yields the same
// thread id, hence the same derived session id, hence the SDK resumes the
// right per-project conversation.
func cwdThreadID(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	base := threadSafe(filepath.Base(cwd))
	if base == "" {
		base = "dir"
	}
	return base + "-" + hex.EncodeToString(sum[:])[:12]
}

// threadSafe keeps only [A-Za-z0-9-_] so a thread id is always a safe store
// directory name and session-derivation input.
func threadSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
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
