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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"

	"github.com/CarriedWorldUniverse/agora/internal/daemon"
)

// runDaemon implements `agora daemon [flags]`. Kept thin: it wires
// daemon.NewDaemon (default in-memory Store/Registry/Policy — a persistent
// deployment injects a real persistence.LocalStore/remote.Registry via a
// config file, a later follow-up per blueprint §6 q4) to a listening unix
// socket, plus an optional http server for the session-protocol websocket
// endpoint.
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

	d := daemon.NewDaemon(ctx, daemon.Config{})

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
}
