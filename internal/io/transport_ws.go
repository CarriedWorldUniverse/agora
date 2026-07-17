// Package io: websocket transport for the session protocol.
// Spec: agora-spec-io.md §0a ("... and ws (loopback by default; tailnet/
// herald-authed for remote web later)"), §0 (chat webpage is a thin ws
// client of this same protocol).
package io

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/coder/websocket"
)

// HandleWS upgrades an HTTP request to a websocket and drives it through
// ServeConn — the handler an `agora daemon` http.ServeMux wires at its ws
// endpoint (U18). Blocks until the connection closes.
func HandleWS(ctx context.Context, w http.ResponseWriter, r *http.Request, sessions SessionLookup) error {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return fmt.Errorf("io: ws accept: %w", err)
	}
	defer conn.CloseNow()
	rw := websocket.NetConn(ctx, conn, websocket.MessageText)
	err = ServeConn(ctx, rw, sessions)
	conn.Close(websocket.StatusNormalClosure, "")
	return err
}

// DialWS dials a session-protocol websocket endpoint and returns a net.Conn
// suitable for ServeConn's rw parameter (the client-side counterpart to
// HandleWS — used by a TUI/vessel/web client, or tests).
func DialWS(ctx context.Context, url string) (net.Conn, error) {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("io: ws dial: %w", err)
	}
	return websocket.NetConn(ctx, conn, websocket.MessageText), nil
}
