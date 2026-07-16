package io

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// fixtureEvents decodes contracts/testdata/flows/<name> (the frozen golden
// event-stream fixtures U1 shipped alongside the Event/Input contracts)
// into a []contracts.Event, reusing them as U2's pipe-mode golden fixtures
// too: one source of truth for "what a valid agora-spec-io.md §1 event
// stream looks like."
func fixtureEvents(t *testing.T, name string) []contracts.Event {
	t.Helper()
	path := filepath.Join("..", "..", "contracts", "testdata", "flows", name)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture %s: %v", path, err)
	}
	defer f.Close()
	var out []contracts.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev contracts.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("decode fixture line: %v", err)
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("empty fixture %s", name)
	}
	return out
}

func fixtureRaw(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "contracts", "testdata", "flows", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// TestRunPipe_GoldenEventStream: RunPipe against a ScriptedEngine loaded
// from the frozen turn.jsonl fixture reproduces that fixture BYTE FOR BYTE
// on stdout for one user_message input — the deterministic-serialization
// requirement (no wall clock, no map-iteration order) made concrete.
func TestRunPipe_GoldenEventStream(t *testing.T) {
	for _, name := range []string{"turn.jsonl", "approval.jsonl", "question_park_resume.jsonl"} {
		t.Run(name, func(t *testing.T) {
			events := fixtureEvents(t, name)
			engine := &ScriptedEngine{Script: []ScriptedTurn{{Events: events}}}

			in := strings.NewReader(`{"type":"user_message","text":"go"}` + "\n")
			var out, errBuf bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Deltas: true — these fixtures include item.agent_message.delta
			// lines, which pipe mode otherwise suppresses by default (§1); a
			// byte-for-byte comparison against the raw fixture needs them on.
			code, err := RunPipe(ctx, in, &out, &errBuf, engine, PipeOptions{Deltas: true})
			if err != nil {
				t.Fatalf("RunPipe: %v", err)
			}
			wantCode := ExitCompleted
			last := events[len(events)-1]
			if last.Type == contracts.EvTurnFailed {
				wantCode = ExitFailed
			}
			if code != wantCode {
				t.Errorf("exit code = %d, want %d", code, wantCode)
			}

			want := fixtureRaw(t, name)
			if !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("stdout mismatch\n--- got ---\n%s\n--- want ---\n%s", out.String(), string(want))
			}
		})
	}
}

// TestRunPipe_DeltasGatedByDefault: item.agent_message.delta is suppressed
// unless PipeOptions.Deltas is set (§1: "off by default in pipe mode, on
// with --deltas").
func TestRunPipe_DeltasGatedByDefault(t *testing.T) {
	events := fixtureEvents(t, "turn.jsonl")
	deltaCount := 0
	for _, ev := range events {
		if ev.Type == contracts.EvAgentMessageDelta {
			deltaCount++
		}
	}
	if deltaCount == 0 {
		t.Fatal("fixture has no delta events to test gating against")
	}

	run := func(deltas bool) string {
		engine := &ScriptedEngine{Script: []ScriptedTurn{{Events: events}}}
		in := strings.NewReader(`{"type":"user_message","text":"go"}` + "\n")
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := RunPipe(ctx, in, &out, &bytes.Buffer{}, engine, PipeOptions{Deltas: deltas}); err != nil {
			t.Fatalf("RunPipe: %v", err)
		}
		return out.String()
	}

	withoutDeltas := run(false)
	if strings.Contains(withoutDeltas, "item.agent_message.delta") {
		t.Fatalf("deltas leaked into output with Deltas=false:\n%s", withoutDeltas)
	}
	withDeltas := run(true)
	if !strings.Contains(withDeltas, "item.agent_message.delta") {
		t.Fatalf("deltas missing from output with Deltas=true:\n%s", withDeltas)
	}
}

// TestRunPipe_FilterAgentMessage: --filter agent_message keeps only
// item.completed/agent_message lines (§1).
func TestRunPipe_FilterAgentMessage(t *testing.T) {
	events := fixtureEvents(t, "turn.jsonl")
	engine := &ScriptedEngine{Script: []ScriptedTurn{{Events: events}}}
	in := strings.NewReader(`{"type":"user_message","text":"go"}` + "\n")
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := RunPipe(ctx, in, &out, &bytes.Buffer{}, engine, PipeOptions{Filter: FilterAgentMessage}); err != nil {
		t.Fatalf("RunPipe: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (just the completed agent_message): %q", len(lines), out.String())
	}
	var ev contracts.Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("decode filtered line: %v", err)
	}
	if ev.Type != contracts.EvItemCompleted || ev.Item.Type != contracts.ItemAgentMessage {
		t.Fatalf("filtered line is not the completed agent_message: %+v", ev)
	}
}

// TestRunPipe_FilterText: --filter text degrades to bare text lines, no
// JSON envelope at all (§1: "pure Unix pipe: text in -> text out").
func TestRunPipe_FilterText(t *testing.T) {
	events := fixtureEvents(t, "turn.jsonl")
	engine := &ScriptedEngine{Script: []ScriptedTurn{{Events: events}}}
	in := strings.NewReader(`{"type":"user_message","text":"go"}` + "\n")
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := RunPipe(ctx, in, &out, &bytes.Buffer{}, engine, PipeOptions{Filter: FilterText}); err != nil {
		t.Fatalf("RunPipe: %v", err)
	}
	got := strings.TrimRight(out.String(), "\n")
	want := "Looking at the failing test now.\n"
	want = strings.TrimRight(want, "\n")
	if got != want {
		t.Fatalf("filter text output = %q, want %q", got, want)
	}
	if strings.Contains(out.String(), "{") {
		t.Fatalf("filter text output still has a JSON envelope: %q", out.String())
	}
}

// TestRunPipe_LenientNonJSONLine: a bare non-JSON stdin line is accepted as
// a user_message's text when Lenient is set (§1: "so `echo "fix the test" |
// agora pipe` works"), and rejected otherwise.
func TestRunPipe_LenientNonJSONLine(t *testing.T) {
	turn := ScriptedTurn{Events: []contracts.Event{
		{Type: contracts.EvTurnCompleted, ThreadID: "th_x", TurnID: "tu_1", Payload: json.RawMessage(`{"usage":{"input":1,"output":1}}`)},
	}}

	t.Run("lenient accepts", func(t *testing.T) {
		engine := &ScriptedEngine{Script: []ScriptedTurn{turn}}
		in := strings.NewReader("fix the test\n")
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		code, err := RunPipe(ctx, in, &out, &bytes.Buffer{}, engine, PipeOptions{Lenient: true})
		if err != nil {
			t.Fatalf("RunPipe: %v", err)
		}
		if code != ExitCompleted {
			t.Fatalf("code = %d, want ExitCompleted", code)
		}
	})

	t.Run("strict rejects", func(t *testing.T) {
		engine := &ScriptedEngine{Script: []ScriptedTurn{turn}}
		in := strings.NewReader("fix the test\n")
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := RunPipe(ctx, in, &out, &bytes.Buffer{}, engine, PipeOptions{Lenient: false}); err == nil {
			t.Fatal("expected an error decoding a non-JSON line in strict mode")
		}
	})
}

// TestRunPipe_ExitCodes covers §4's three exit codes: completed, failed,
// and interrupted (the classifier documented on RunPipe: turn.failed
// immediately preceded by an "interrupt" Input is ExitInterrupted).
func TestRunPipe_ExitCodes(t *testing.T) {
	cases := []struct {
		name     string
		inputs   string
		script   []ScriptedTurn
		wantCode int
	}{
		{
			name:   "completed",
			inputs: `{"type":"user_message","text":"go"}` + "\n",
			script: []ScriptedTurn{{Events: []contracts.Event{
				{Type: contracts.EvTurnCompleted, Payload: json.RawMessage(`{"usage":{"input":1,"output":1}}`)},
			}}},
			wantCode: ExitCompleted,
		},
		{
			name:   "failed",
			inputs: `{"type":"user_message","text":"go"}` + "\n",
			script: []ScriptedTurn{{Events: []contracts.Event{
				{Type: contracts.EvTurnFailed, Payload: json.RawMessage(`{"error":"boom"}`)},
			}}},
			wantCode: ExitFailed,
		},
		{
			name: "interrupted",
			inputs: `{"type":"user_message","text":"go"}` + "\n" +
				`{"type":"interrupt"}` + "\n",
			script: []ScriptedTurn{
				{Events: []contracts.Event{{Type: contracts.EvTurnStarted}}},
				{Events: []contracts.Event{{Type: contracts.EvTurnFailed, Payload: json.RawMessage(`{"error":"interrupted"}`)}}},
			},
			wantCode: ExitInterrupted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &ScriptedEngine{Script: tc.script}
			in := strings.NewReader(tc.inputs)
			var out bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			code, err := RunPipe(ctx, in, &out, &bytes.Buffer{}, engine, PipeOptions{})
			if err != nil {
				t.Fatalf("RunPipe: %v", err)
			}
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d\noutput:\n%s", code, tc.wantCode, out.String())
			}
		})
	}
}

// TestRunPipe_UnscriptedInputEmitsError exercises ScriptedEngine's own
// unscripted-input path via the pipe (more inputs than script turns).
func TestRunPipe_UnscriptedInputEmitsError(t *testing.T) {
	engine := &ScriptedEngine{Script: nil}
	in := strings.NewReader(`{"type":"user_message","text":"go"}` + "\n")
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := RunPipe(ctx, in, &out, &bytes.Buffer{}, engine, PipeOptions{}); err != nil {
		t.Fatalf("RunPipe: %v", err)
	}
	if !strings.Contains(out.String(), `"type":"error"`) {
		t.Fatalf("expected an error event, got: %s", out.String())
	}
}
