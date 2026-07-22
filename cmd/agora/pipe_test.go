package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	stdio "io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// syncBuffer is a concurrency-safe bytes.Buffer wrapper with a
// turn-completed signal: RunPipe's event loop writes stdout on one
// goroutine while this test reads/waits on another, so a plain
// bytes.Buffer (not safe for concurrent Write+Read) would race.
type syncBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	once sync.Once
	seen chan struct{}
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{seen: make(chan struct{})} }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	if bytes.Contains(p, []byte(`"turn.completed"`)) {
		b.once.Do(func() { close(b.seen) })
	}
	return n, err
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRunPipeWithProvider_UserMessageToTurnCompleted drives `agora pipe`'s
// real wiring (runPipeWithProvider -> newInProcessManager -> the SAME
// standalone in-process engine construction bare `agora` falls back to) in
// process against the fake bridle provider: one user_message JSONL line in,
// a turn.completed JSONL line out, and stdout containing ONLY valid JSONL —
// no stray log/diagnostic bytes leaked onto the protocol stream.
func TestRunPipeWithProvider_UserMessageToTurnCompleted(t *testing.T) {
	// Sandbox newInProcessStore's ~/.agora root so this test never touches
	// the real operator state dir.
	t.Setenv("HOME", t.TempDir())

	provider := fake.NewProvider(fake.Step{
		Text:  "hello from the fake pipe turn",
		Usage: bridle.Usage{InputTokens: 4, OutputTokens: 3},
	})

	// stdin is an io.Pipe under this test's own control rather than a
	// pre-built strings.Reader with both lines queued: RunPipe's Manager
	// treats stdin EOF/"end" the same as an interrupt of any turn still
	// in flight (contracts.InEnd/in-closed both call stopInFlight), so
	// sending "end"/closing stdin before the turn's own turn.completed
	// has been observed races against the (near-instant, fake-provider)
	// turn — exactly the failure this setup avoids: write user_message,
	// wait for the turn.completed line on stdout, THEN close stdin.
	pr, pw := stdio.Pipe()
	stdout := newSyncBuffer()
	var stderr bytes.Buffer

	codeCh := make(chan int, 1)
	errCh := make(chan error, 1)
	go func() {
		code, err := runPipeWithProvider(context.Background(), "th_pipe_test", provider, pr, stdout, &stderr, agoraio.PipeOptions{})
		codeCh <- code
		errCh <- err
	}()

	if _, err := pw.Write([]byte(`{"type":"user_message","text":"hi"}` + "\n")); err != nil {
		t.Fatalf("write user_message: %v", err)
	}

	select {
	case <-stdout.seen:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for turn.completed on stdout; stdout so far=%q stderr=%q", stdout.String(), stderr.String())
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}

	var code int
	var err error
	select {
	case code = <-codeCh:
		err = <-errCh
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runPipeWithProvider to return after stdin EOF")
	}
	if err != nil {
		t.Fatalf("runPipeWithProvider: %v (stderr=%s)", err, stderr.String())
	}
	if code != agoraio.ExitCompleted {
		t.Fatalf("exit code = %d; want ExitCompleted (0). stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	sc := bufio.NewScanner(strings.NewReader(stdout.String()))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var lines []string
	sawTurnCompleted := false
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		lines = append(lines, line)
		var ev contracts.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("stdout line is not valid JSONL: %q: %v", line, err)
		}
		if ev.Type == contracts.EvTurnCompleted {
			sawTurnCompleted = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan stdout: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("expected at least one JSONL line on stdout, got none")
	}
	if !sawTurnCompleted {
		t.Fatalf("expected a turn.completed line among stdout output, got: %v", lines)
	}
}
