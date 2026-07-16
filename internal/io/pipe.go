// Package io: pipe mode.
// Spec: agora-spec-io.md §1.
package io

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	stdio "io"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// Process exit codes for one-shot pipe/exec runs.
// Spec: agora-spec-io.md §4.
const (
	ExitCompleted   = 0
	ExitFailed      = 2
	ExitInterrupted = 3
)

// Filter selects the pipe-mode output shape (§1 "channel filtering for
// chains").
type Filter string

const (
	// FilterNone emits every event (still subject to Deltas gating).
	FilterNone Filter = ""
	// FilterAgentMessage emits only item.completed events whose item is
	// agent_message, one JSONL line each, full text — no tool noise, no
	// deltas: the shape a TTS consumer wants.
	FilterAgentMessage Filter = "agent_message"
	// FilterText degrades further to raw text lines (no JSON envelope at
	// all): pure Unix pipe, text in -> text out.
	FilterText Filter = "text"
)

// PipeOptions controls RunPipe's behavior (the pipe-mode flags in
// agora-spec-io.md §1).
type PipeOptions struct {
	// Deltas emits item.agent_message.delta events. Off by default (§1:
	// "streaming text ... off by default in pipe mode, on with --deltas").
	Deltas bool
	// Lenient accepts a non-JSON stdin line as a user_message's text (§1:
	// "so `echo "fix the test" | agora pipe` works").
	Lenient bool
	// Filter narrows stdout per Filter's doc.
	Filter Filter
}

// maxPipeLineBytes bounds a single stdin/JSONL line, mirroring
// persistence's maxLineBytes backstop.
const maxPipeLineBytes = 1 << 24 // 16 MiB

// itemPayload is the payload shape item.* events carry for agent_message —
// the one item type §1's filtering singles out. Other item types are
// opaque to this package and pass through verbatim.
type itemPayload struct {
	Text string `json:"text"`
}

// RunPipe drives engine in pipe mode: it decodes stdin JSONL Input, forwards
// them to engine, and writes engine's output Event stream to stdout as
// JSONL (§1), honoring opts. stderr receives human diagnostics only — never
// protocol (§1).
//
// It returns the process exit code (§4) and an error only for I/O-level
// failures (a stdout write failing, a fatally malformed non-lenient input
// line, etc.) — a turn failing or being interrupted is a NORMAL outcome
// reported via the exit code, not a Go error, so callers can script against
// it exactly like a shell command's exit status.
//
// Exit-code classification (spec-ambiguity call, documented here since §4
// doesn't spell out how "interrupted" is detected on the wire — there is no
// turn.interrupted event type): RunPipe tracks the most recently sent Input
// type. If the engine's last terminal event for the run is turn.failed AND
// the immediately preceding Input was "interrupt", the run is classified
// ExitInterrupted; a turn.failed with any other preceding input is
// ExitFailed; turn.completed is ExitCompleted; a run with no terminal event
// at all (e.g. only a config/end round-trip, no turn ever started) is
// ExitCompleted.
func RunPipe(ctx context.Context, r stdio.Reader, w stdio.Writer, stderr stdio.Writer, engine Engine, opts PipeOptions) (int, error) {
	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 1)

	engineErr := make(chan error, 1)
	go func() { engineErr <- engine.Run(ctx, in, out) }()

	var mu sync.Mutex // guards lastInput between the reader goroutine and the classifier below
	var lastInput contracts.InputType

	readErr := make(chan error, 1)
	go func() {
		defer close(in)
		readErr <- readPipeInput(ctx, r, opts, func(i contracts.Input) bool {
			mu.Lock()
			lastInput = i.Type
			mu.Unlock()
			select {
			case in <- i:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()

	exitCode := ExitCompleted
	for ev := range out {
		if err := writePipeEvent(w, ev, opts); err != nil {
			return exitCode, fmt.Errorf("io: write event: %w", err)
		}
		switch ev.Type {
		case contracts.EvTurnCompleted:
			exitCode = ExitCompleted
		case contracts.EvTurnFailed:
			mu.Lock()
			li := lastInput
			mu.Unlock()
			if li == contracts.InInterrupt {
				exitCode = ExitInterrupted
			} else {
				exitCode = ExitFailed
			}
		}
	}

	if err := <-engineErr; err != nil {
		return exitCode, fmt.Errorf("io: engine: %w", err)
	}
	if err := <-readErr; err != nil {
		return exitCode, fmt.Errorf("io: read input: %w", err)
	}
	return exitCode, nil
}

// writePipeEvent applies opts.Filter/Deltas and, if the event survives,
// writes it: JSONL (one json.Marshal + newline) in FilterNone/
// FilterAgentMessage, or a bare text line under FilterText. Using
// json.Marshal directly (rather than json.Encoder, which behaves
// identically but would hide the write behind an unrelated stateful value)
// keeps every line byte-for-byte reproducible for golden tests.
func writePipeEvent(w stdio.Writer, ev contracts.Event, opts PipeOptions) error {
	if ev.Type == contracts.EvAgentMessageDelta && !opts.Deltas {
		return nil
	}
	switch opts.Filter {
	case FilterNone:
		return writeJSONLine(w, ev)
	case FilterAgentMessage:
		if !isCompletedAgentMessage(ev) {
			return nil
		}
		return writeJSONLine(w, ev)
	case FilterText:
		if !isCompletedAgentMessage(ev) {
			return nil
		}
		var p itemPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("io: decode agent_message payload: %w", err)
		}
		_, err := fmt.Fprintln(w, p.Text)
		return err
	default:
		return fmt.Errorf("io: unknown filter %q", opts.Filter)
	}
}

func writeJSONLine(w stdio.Writer, ev contracts.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("io: marshal event: %w", err)
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func isCompletedAgentMessage(ev contracts.Event) bool {
	return ev.Type == contracts.EvItemCompleted && ev.Item != nil && ev.Item.Type == contracts.ItemAgentMessage
}

// readPipeInput decodes stdin JSONL into Input values, delivering each via
// deliver until it returns false (ctx done) or the reader hits EOF/error.
// A non-JSON line is either lenient-mode user_message text or a hard error.
// Delivery stops (without erroring) right after an "end" Input — §1: "end
// (graceful shutdown, also SIGTERM/stdin EOF)".
func readPipeInput(ctx context.Context, r stdio.Reader, opts PipeOptions, deliver func(contracts.Input) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxPipeLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var in contracts.Input
		if err := json.Unmarshal(line, &in); err != nil {
			if !opts.Lenient {
				return fmt.Errorf("io: decode input line: %w", err)
			}
			in = contracts.Input{Type: contracts.InUserMessage, Text: string(line)}
		}
		if !deliver(in) {
			return nil
		}
		if in.Type == contracts.InEnd {
			return nil
		}
	}
	return sc.Err()
}
