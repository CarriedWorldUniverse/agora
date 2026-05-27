package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Operator-reported 2026-05-27: agora "crashed" — actually a side-
// goroutine panic that scribbled its stack across the alt-screen via
// stderr; the main Update loop kept running but the screen was a
// mess of error messages and mouse hex. This guards the principle:
// the recover helper catches the panic, writes a stack to the
// supplied logger, and optionally cancels the supplied context so a
// load-bearing goroutine's death triggers graceful shutdown.

func TestRecoverGoroutine_CatchesPanicAndLogsStack(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Spawn a goroutine that panics, with the recover helper installed.
	// If recover doesn't catch the panic, the test goroutine takes
	// down the test process — there's no need for a manual assertion
	// on the absence of a panic propagating out.
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer recoverGoroutine("test-spike", log, nil)
		panic("synthetic mouse-flood panic")
	}()
	<-done

	out := buf.String()
	if !strings.Contains(out, "test-spike") {
		t.Errorf("log should mention goroutine name; got:\n%s", out)
	}
	if !strings.Contains(out, "synthetic mouse-flood panic") {
		t.Errorf("log should mention panic value; got:\n%s", out)
	}
	if !strings.Contains(out, "goroutine") {
		t.Errorf("log should include a stack trace (debug.Stack); got:\n%s", out)
	}
}

func TestRecoverGoroutine_LoadBearingCancelsContext(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Load-bearing: pass cancel so the panic triggers shutdown.
		defer recoverGoroutine("bus.Run-spike", log, cancel)
		panic("bus died")
	}()
	<-done

	// Recover must have called cancel — ctx.Done() should fire.
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Error("load-bearing goroutine panic should cancel rootCtx; ctx.Done() not fired")
	}
}

func TestRecoverGoroutine_NonLoadBearingDoesNotCancel(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Non-load-bearing: nil cancel — panic is logged but doesn't
		// tear down the program. The signal-handler goroutine fits
		// this shape.
		defer recoverGoroutine("signal-handler-spike", log, nil)
		panic("idempotent panic")
	}()
	<-done

	select {
	case <-ctx.Done():
		t.Error("non-load-bearing recover (nil cancel) should NOT cancel rootCtx")
	default:
		// expected
	}
}

func TestRecoverGoroutine_NoOpWhenNoPanic(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ran atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer recoverGoroutine("happy-path", log, cancel)
		ran.Store(true)
	}()
	<-done

	if !ran.Load() {
		t.Error("happy-path goroutine never ran (test setup error)")
	}
	if buf.Len() > 0 {
		t.Errorf("recover should not log when no panic; got:\n%s", buf.String())
	}
	select {
	case <-ctx.Done():
		t.Error("recover should not cancel when no panic")
	default:
		// expected
	}
}

// Mirrors the original failure mode more closely: a SUSTAINED stream
// of panics across many goroutines (think mouse-flood spawning N
// handlers) all get caught individually. Nothing dies.
func TestRecoverGoroutine_FloodOfPanicsAllCaught(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(&fmtSafeWriter{buf: &buf, mu: &mu}, &slog.HandlerOptions{Level: slog.LevelDebug}))

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			defer recoverGoroutine("flood", log, nil)
			panic(i)
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	got := strings.Count(buf.String(), "agora: goroutine panicked")
	if got != N {
		t.Errorf("expected %d panic log lines, got %d", N, got)
	}
}

// fmtSafeWriter serialises concurrent log writes (slog handlers don't
// guarantee concurrency safety against the underlying writer).
type fmtSafeWriter struct {
	buf *bytes.Buffer
	mu  *sync.Mutex
}

func (w *fmtSafeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
