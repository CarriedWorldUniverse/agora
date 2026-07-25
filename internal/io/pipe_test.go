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
				// The engine's own authoritative signal (FIX 4): a
				// turn.failed carrying {"interrupted":true}, NOT client-side
				// read-timing relative to the preceding interrupt Input.
				{Events: []contracts.Event{{Type: contracts.EvTurnFailed, Payload: json.RawMessage(`{"interrupted":true}`)}}},
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

// TestRunPipe_ExitCode_FailedIgnoresPrecedingInterrupt: a plain turn.failed
// with no {"interrupted":true} marker classifies ExitFailed regardless of
// an interrupt Input that happened to precede it on the wire (FIX 4 — the
// classifier must key off the ENGINE's own signal, not client-side
// read-time ordering: an interrupt sent right as an unrelated failure is
// already in flight must not be misread as having caused it).
func TestRunPipe_ExitCode_FailedIgnoresPrecedingInterrupt(t *testing.T) {
	engine := &ScriptedEngine{Script: []ScriptedTurn{
		{Events: []contracts.Event{{Type: contracts.EvTurnStarted}}},
		{Events: []contracts.Event{{Type: contracts.EvTurnFailed, Payload: json.RawMessage(`{"error":"boom"}`)}}},
	}}
	in := strings.NewReader(
		`{"type":"user_message","text":"go"}` + "\n" +
			`{"type":"interrupt"}` + "\n",
	)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := RunPipe(ctx, in, &out, &bytes.Buffer{}, engine, PipeOptions{})
	if err != nil {
		t.Fatalf("RunPipe: %v", err)
	}
	if code != ExitFailed {
		t.Fatalf("code = %d, want ExitFailed (a preceding interrupt Input must not flip a plain turn.failed to ExitInterrupted)\noutput:\n%s", code, out.String())
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

// collectPipeInputs runs readPipeInput over lines and returns what it
// delivered — the seam where session defaults are applied.
func collectPipeInputs(t *testing.T, lines string, opts PipeOptions) []contracts.Input {
	t.Helper()
	var got []contracts.Input
	err := readPipeInput(context.Background(), strings.NewReader(lines), opts, func(in contracts.Input) bool {
		got = append(got, in)
		return true
	})
	if err != nil {
		t.Fatalf("readPipeInput: %v", err)
	}
	return got
}

// The gap this closes: pipe never set Model/Provider, so headless runs
// could only ever reach the engine's built-in provider.
func TestReadPipeInput_AppliesSessionDefaults(t *testing.T) {
	spec := &contracts.ProviderSpec{Name: "openai", BaseURL: "http://gw:4000/v1", APIKey: "k"}
	got := collectPipeInputs(t, `{"type":"user_message","text":"hi"}`+"\n", PipeOptions{
		DefaultModel:    "kimi-k3",
		DefaultProvider: spec,
	})
	if len(got) != 1 {
		t.Fatalf("got %d inputs; want 1", len(got))
	}
	if got[0].Model != "kimi-k3" {
		t.Errorf("Model = %q; want the session default", got[0].Model)
	}
	if got[0].Provider != spec {
		t.Errorf("Provider = %+v; want the session default", got[0].Provider)
	}
}

// An explicit per-message model must WIN over the session default — the
// caller was specific for that message, and silently overriding it would
// make a documented wire field a lie.
func TestReadPipeInput_PerMessageModelBeatsTheDefault(t *testing.T) {
	got := collectPipeInputs(t, `{"type":"user_message","text":"hi","model":"explicit-model"}`+"\n", PipeOptions{
		DefaultModel: "default-model",
	})
	if got[0].Model != "explicit-model" {
		t.Fatalf("Model = %q; an explicit per-message model must win", got[0].Model)
	}
}

func TestReadPipeInput_PerMessageProviderBeatsTheDefault(t *testing.T) {
	explicit := `{"type":"user_message","text":"hi","provider":{"name":"openai","base_url":"http://explicit/v1"}}`
	got := collectPipeInputs(t, explicit+"\n", PipeOptions{
		DefaultProvider: &contracts.ProviderSpec{Name: "openai", BaseURL: "http://default/v1"},
	})
	if got[0].Provider == nil || got[0].Provider.BaseURL != "http://explicit/v1" {
		t.Fatalf("Provider = %+v; an explicit per-message provider must win", got[0].Provider)
	}
}

// No defaults configured must leave inputs exactly as they arrived —
// this is the overwhelmingly common case and must not be disturbed.
func TestReadPipeInput_NoDefaultsLeavesInputUntouched(t *testing.T) {
	got := collectPipeInputs(t, `{"type":"user_message","text":"hi"}`+"\n", PipeOptions{})
	if got[0].Model != "" || got[0].Provider != nil {
		t.Fatalf("input was modified with no defaults set: %+v", got[0])
	}
}

// Defaults are a USER-MESSAGE concept — applying them to an approval
// response or an end marker would be meaningless at best.
func TestReadPipeInput_DefaultsOnlyApplyToUserMessages(t *testing.T) {
	lines := `{"type":"approval_response","id":"1","decision":"allow"}` + "\n" +
		`{"type":"end"}` + "\n"
	got := collectPipeInputs(t, lines, PipeOptions{
		DefaultModel:    "kimi-k3",
		DefaultProvider: &contracts.ProviderSpec{Name: "openai"},
	})
	for _, in := range got {
		if in.Model != "" || in.Provider != nil {
			t.Errorf("%s input got session defaults applied: %+v", in.Type, in)
		}
	}
}

// The lenient path (a bare non-JSON line) synthesizes a user_message and
// must get defaults too — `echo "fix it" | agora pipe -model kimi` is
// exactly the shape this feature is for.
func TestReadPipeInput_LenientLineAlsoGetsDefaults(t *testing.T) {
	got := collectPipeInputs(t, "fix the test\n", PipeOptions{
		Lenient:      true,
		DefaultModel: "kimi-k3",
	})
	if len(got) != 1 || got[0].Model != "kimi-k3" {
		t.Fatalf("lenient line did not get the session default: %+v", got)
	}
}
