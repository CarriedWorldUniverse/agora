// Package io: unix-socket transport for the session protocol.
// Spec: agora-spec-io.md §0a ("agora daemon ... listens on UDS (local) and
// ws (loopback by default) ...").
package io

import (
	"context"
	"fmt"
	"net"
	"os"
)

// ListenUnix opens a unix-domain-socket listener at path, removing any
// stale socket file left by a prior process first (best-effort — a genuine
// permission error still surfaces from Listen).
//
// DEFERRED (not this unit): a TOCTOU on path's parent directory (an
// attacker pre-creating/symlinking the socket path or its parent before
// ListenUnix runs) is a daemon-placement concern — U18 (internal/daemon)
// is where the daemon chooses a safe, non-attacker-writable directory for
// the socket; this package only opens whatever path it's given.
//
// Cross-platform note (agora-spec-io.md's Windows CI requirement): this
// compiles on every GOOS unmodified — Go's net package defines the "unix"
// network for all platforms. Recent Windows (10 1803+) supports AF_UNIX and
// Go's net.Listen("unix", ...) works there; ServeUnix below closes every
// handle before returning so a Windows caller's temp-dir cleanup never hits
// a file still open. On a Windows version without AF_UNIX support, Listen
// returns a runtime error here (not a build failure) — callers such as
// `agora daemon` (U18) fail over to ws-only in that case; this package only
// needs to not crash the build.
func ListenUnix(path string) (net.Listener, error) {
	_ = os.Remove(path) // clear a stale socket file from a prior run, if any
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("io: listen unix %s: %w", path, err)
	}
	// FIX 5: harden the socket's mode so it isn't world-connectable under
	// the default umask (net.Listen creates it per-umask, which can be
	// looser than we want for a control socket carrying capability-gated
	// input).
	if err := os.Chmod(path, 0o700); err != nil {
		ln.Close()
		return nil, fmt.Errorf("io: chmod unix socket %s: %w", path, err)
	}
	return ln, nil
}

// DialUnix dials a unix-domain-socket session-protocol listener.
func DialUnix(path string) (net.Conn, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("io: dial unix %s: %w", path, err)
	}
	return conn, nil
}

// ServeUnix accepts connections on ln and runs ServeConn for each in its own
// goroutine, until ctx is done or Accept returns an error (e.g. the
// listener was closed). It does not close ln itself — the caller owns the
// listener's lifecycle (so it can, e.g., close it from a signal handler to
// unblock Accept).
func ServeUnix(ctx context.Context, ln net.Listener, sessions SessionLookup) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("io: accept: %w", err)
			}
		}
		go func() {
			defer conn.Close()
			_ = ServeConn(ctx, conn, sessions)
		}()
	}
}
