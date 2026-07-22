// `agora daemon`: boots the internal/daemon runtime (serve UDS + optional
// ws), per this unit's blueprint §6 resolution 4 — thin CLI wiring over the
// real internal/daemon Go API. The conformance suite drives that Go API
// directly (blueprint's own DoD); this subcommand is what actually makes
// the daemon bootable as a standalone process, not just testable in-process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"

	"github.com/CarriedWorldUniverse/agora/internal/daemon"
	"github.com/CarriedWorldUniverse/bridle/provider/claudesdk"
)

// runDaemon implements `agora daemon [flags]`. Kept thin: it wires
// daemon.NewDaemon to a real EngineFactory (newEngineFactory, engine.go) —
// the SAME turnengine.Manager construction bare `agora`'s in-process
// fallback uses (claudesdk.New()'s real subscription provider), over a
// persistence.LocalStore rooted at the operator's ~/.agora state dir (the
// same store newInProcessStore opens, so a daemon-hosted thread and an
// in-process one are backed by identical on-disk state) — then listens on
// a unix socket, plus an optional http server for the session-protocol
// websocket endpoint.
func runDaemon(args []string) {
	fs := flag.NewFlagSet("agora daemon", flag.ExitOnError)
	socketPath := fs.String("socket", defaultSocketPath(), "unix socket to serve the session protocol on")
	httpAddr := fs.String("http", "", "http address to serve the session-protocol websocket on at /ws (empty disables it)")
	_ = fs.Parse(args)

	if err := os.MkdirAll(filepath.Dir(*socketPath), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "agora daemon: mkdir socket dir: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
	}()

	store, err := newInProcessStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agora daemon: open thread store: %v\n", err)
		os.Exit(1)
	}

	// One process-wide agent-graph handle shared by every thread's engine —
	// closed at shutdown below (engine.go openAgentGraph's contract).
	graph, closeGraph := openAgentGraph()
	defer closeGraph()

	d := daemon.NewDaemon(ctx, daemon.Config{
		Store:         store,
		EngineFactory: newEngineFactory(claudesdk.New(), store, graph),
	})

	ln, err := agoraio.ListenUnix(*socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agora daemon: listen unix %s: %v\n", *socketPath, err)
		os.Exit(1)
	}
	defer ln.Close()

	errCh := make(chan error, 2)
	go func() { errCh <- d.ServeUnix(ctx, ln) }()

	var httpSrv *http.Server
	if *httpAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			_ = d.HandleWS(ctx, w, r)
		})
		httpSrv = &http.Server{Addr: *httpAddr, Handler: mux}
		go func() {
			<-ctx.Done()
			_ = httpSrv.Close()
		}()
		go func() { errCh <- httpSrv.ListenAndServe() }()
	}

	fmt.Fprintf(os.Stderr, "agora daemon: serving %s", *socketPath)
	if *httpAddr != "" {
		fmt.Fprintf(os.Stderr, " and ws on %s/ws", *httpAddr)
	}
	fmt.Fprintln(os.Stderr)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "agora daemon: %v\n", err)
		}
	}
	d.Close()
	// Daemon.Close tears down every live Session but, like
	// inProcessBackend.Close, does not own the store's lifecycle (it was
	// injected via Config.Store) — close it here if it holds a resource
	// (LocalStore's *sql.DB) so the process doesn't leak the handle past
	// the daemon's own shutdown.
	if c, ok := store.(io.Closer); ok {
		if cerr := c.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "agora daemon: close store: %v\n", cerr)
		}
	}
}
