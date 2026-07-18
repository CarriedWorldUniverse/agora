package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestIsNoDaemonErr proves the fallback-trigger classification: a
// connect-level failure (nobody's listening, or the socket file doesn't
// exist yet) says "fall back to in-process"; a generic non-connect error
// does not.
func TestIsNoDaemonErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"connection refused", syscall.ECONNREFUSED, true},
		{"no such file", syscall.ENOENT, true},
		{"os not exist", os.ErrNotExist, true},
		{"wrapped connection refused", fmt.Errorf("io: dial unix %s: %w", "/tmp/x.sock", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), true},
		{"dial OpError, arbitrary cause", &net.OpError{Op: "dial", Net: "unix", Err: errors.New("boom")}, true},
		{"non-dial OpError", &net.OpError{Op: "read", Net: "unix", Err: errors.New("boom")}, false},
		{"write OpError (attach-frame on a pipe a daemon dropped mid-handshake)", &net.OpError{Op: "write", Net: "unix", Err: syscall.EPIPE}, true},
		{"bare EPIPE", syscall.EPIPE, true},
		{"generic error", errors.New("something else entirely"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNoDaemonErr(tc.err); got != tc.want {
				t.Errorf("isNoDaemonErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestDialBackend_FallsBackToInProcessWhenNoDaemon proves the SELECTION
// end-to-end: dialing a unix socket path that has never been listened on
// (ENOENT -> isNoDaemonErr) reaches newInProcessBackend, which actually
// succeeds in constructing a working Backend — no daemon binary, no real
// claude-sdk turn (constructing claudesdk.New()/turnengine.NewManager does
// not itself spawn the sidecar or touch ambient credentials; see
// inprocess.go's doc comment — that only happens once a turn actually
// RUNS, which this test never triggers). Isolates HOME/USERPROFILE to a
// temp dir so newInProcessStore's ~/.agora store lives in a scratch
// directory, never the real operator state.
func TestDialBackend_FallsBackToInProcessWhenNoDaemon(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	bogusSocket := filepath.Join(t.TempDir(), "no-such-daemon", "agora.sock")
	attach := agoraio.AttachRequest{
		ThreadID: "fallback-test-thread",
		ClientID: "test-client",
		Kind:     "tui",
		Capabilities: []contracts.Capability{
			contracts.CapInteractive,
			contracts.CapApprover,
		},
	}

	backend, err := dialBackend(ctx, discardLogger(), bogusSocket, "", attach)
	if err != nil {
		t.Fatalf("dialBackend: expected fallback to succeed, got error: %v", err)
	}
	if backend == nil {
		t.Fatal("dialBackend: expected a non-nil in-process backend")
	}
	defer backend.Close()

	// Prove the store actually persisted the thread on disk under the
	// isolated HOME — the observable signature of the REAL (not stub) v1
	// assembly (newInProcessStore's file/JSONL store choice).
	threadFile, globErr := filepath.Glob(filepath.Join(tmpHome, ".agora", "threads", "*", "fallback-test-thread.jsonl"))
	if globErr != nil {
		t.Fatalf("glob: %v", globErr)
	}
	if len(threadFile) == 0 {
		t.Fatalf("expected a persisted thread file under %s/.agora/threads, found none", tmpHome)
	}

	if err := backend.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestDialBackend_WSAlwaysExplicitRemote proves -ws forces the WS backend
// even when nothing is listening there — it never falls back to
// in-process (a remote target is explicit; silently rerouting an explicit
// -ws request to an in-process engine on a DIFFERENT machine's cwd would
// be a correctness bug, not a convenience).
func TestDialBackend_WSAlwaysExplicitRemote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attach := agoraio.AttachRequest{ThreadID: "t", ClientID: "c", Kind: "tui"}
	_, err := dialBackend(ctx, discardLogger(), "/dev/null/not-a-real-socket", "ws://127.0.0.1:1/nope", attach)
	if err == nil {
		t.Fatal("expected an error dialing an unreachable ws URL, got nil (did it silently fall back to in-process?)")
	}
}
