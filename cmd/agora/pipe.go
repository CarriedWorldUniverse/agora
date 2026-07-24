// `agora pipe`: the standalone JSONL duplex CLI entry for internal/io's
// pipe mode (agora-spec-io.md §1). internal/io.RunPipe is conformance
// tested but had no CLI wiring at all — this file is that glue: it opens
// the SAME real, in-process turn engine bare `agora`'s no-daemon fallback
// uses (newInProcessManager, engine.go — the shared construction that also
// backs `agora daemon`'s per-thread EngineFactory), then hands it to
// RunPipe over stdin/stdout, exiting with RunPipe's classified exit code.
package main

import (
	"context"
	"flag"
	"fmt"
	stdio "io"
	"os"
	"os/signal"
	"syscall"

	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/provider/claudesdk"

	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// runPipe implements `agora pipe [flags]`. stdin/stdout carry ONLY JSONL
// protocol (§1: "stderr receives human diagnostics only, never protocol");
// every diagnostic here goes to os.Stderr.
func runPipe(args []string) {
	fs := flag.NewFlagSet("agora pipe", flag.ExitOnError)
	threadID := fs.String("thread", "default", "thread to run the pipe against")
	deltas := fs.Bool("deltas", false, "emit item.agent_message.delta streaming-text events (off by default per §1)")
	lenient := fs.Bool("lenient", false, "accept a non-JSON stdin line as a user_message's text")
	filter := fs.String("filter", "", `output filter: "" (all events) | "agent_message" (final agent-message items only) | "text" (bare text lines, no JSON envelope)`)
	applyMode := registerModeFlag(fs)
	_ = fs.Parse(args)
	applyMode()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
	}()

	code, err := runPipeWithProvider(ctx, *threadID, claudesdk.New(), os.Stdin, os.Stdout, os.Stderr, agoraio.PipeOptions{
		Deltas:  *deltas,
		Lenient: *lenient,
		Filter:  agoraio.Filter(*filter),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agora pipe: %v\n", err)
	}
	os.Exit(code)
}

// runPipeWithProvider is runPipe's testable core: the provider (production
// claudesdk.New(), a bridle/fake.Provider in tests) and the r/w/stderr
// streams are all parameters, so a test can run the real RunPipe/engine
// wiring in-process against a scripted provider and its own buffers instead
// of a subprocess. Builds the standalone in-process engine
// (newInProcessManager) for threadID and drives it via agoraio.RunPipe,
// closing the store afterward exactly like inProcessBackend.Close does.
func runPipeWithProvider(ctx context.Context, threadID string, provider bridle.Provider, r stdio.Reader, w, stderr stdio.Writer, opts agoraio.PipeOptions) (int, error) {
	mgr, store, closeGraph, err := newInProcessManager(threadID, provider)
	if err != nil {
		return agoraio.ExitFailed, err
	}
	defer closeGraph()
	defer func() {
		if c, ok := store.(stdio.Closer); ok {
			_ = c.Close()
		}
	}()

	return agoraio.RunPipe(ctx, r, w, stderr, mgr, opts)
}
