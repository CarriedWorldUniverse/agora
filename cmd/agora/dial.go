package main

import (
	"errors"
	"net"
	"os"
	"syscall"
)

// isNoDaemonErr reports whether err (as returned by tui.DialUnixBackend)
// looks like "no daemon is listening at this socket path" — connection
// refused (a listener existed once but nothing's serving now) or no such
// file/directory (the socket was never created, e.g. a fresh ~/.agora) —
// as opposed to a genuine auth/protocol error from something that DID
// accept the TCP/unix connection.
//
// dialBackend's contract (main.go): only THIS classification triggers the
// in-process fallback. Any other dial error (a malformed socket path
// permission error, or — in principle, once real auth/protocol failures
// can occur pre-Attach — something that isn't "nobody's home") is returned
// to the caller as a hard failure instead of silently masked behind a
// totally different engine.
//
// tui.DialUnixBackend can fail two ways today: agoraio.DialUnix's own
// net.Dial (always a connect-level failure — ECONNREFUSED/ENOENT/a bare
// *net.OpError with Op=="dial"), or the post-dial attach-frame write
// (newIOBackend's writeFrame) failing on an already-broken pipe — which is
// also connect-adjacent (nothing has responded yet at that point; no
// server-side auth/protocol rejection can have happened before the first
// byte is even read back). Factored into its own function (rather than
// inlined in dialBackend) specifically so this DECISION is unit-testable
// against synthetic errors without dialing a real socket — see dial_test.go.
func isNoDaemonErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	return false
}
