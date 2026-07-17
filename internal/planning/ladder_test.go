package planning

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestResolve_LadderMatrix is the (context × blocking) → disposition table
// the ladder is defined by (spec §5), plus the blocking:false row that
// applies at every level, plus an unrecognized-context fail-closed check.
func TestResolve_LadderMatrix(t *testing.T) {
	tests := []struct {
		name     string
		ctx      QuestionContext
		blocking bool
		want     Disposition
		wantErr  bool
	}{
		{"interactive blocking parks", ContextInteractive, true, DispositionPark, false},
		{"orchestrator blocking parks", ContextOrchestrator, true, DispositionPark, false},
		{"dispatch pod blocking dies honestly", ContextDispatchPod, true, DispositionDieHonestly, false},
		{"subagent blocking bubbles", ContextSubagent, true, DispositionBubble, false},
		{"workflow child blocking bubbles", ContextWorkflowChild, true, DispositionBubble, false},

		{"interactive non-blocking queues", ContextInteractive, false, DispositionQueue, false},
		{"orchestrator non-blocking queues", ContextOrchestrator, false, DispositionQueue, false},
		{"dispatch pod non-blocking queues", ContextDispatchPod, false, DispositionQueue, false},
		{"subagent non-blocking queues", ContextSubagent, false, DispositionQueue, false},
		{"workflow child non-blocking queues", ContextWorkflowChild, false, DispositionQueue, false},

		{"unknown context blocking fails closed", QuestionContext("bogus"), true, "", true},
		{"unknown context non-blocking fails closed", QuestionContext("bogus"), false, "", true},
		{"empty context blocking fails closed", QuestionContext(""), true, "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.ctx, tc.blocking)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q, %v) = %q, nil; want error", tc.ctx, tc.blocking, got)
				}
				if !errors.Is(err, ErrUnknownContext) {
					t.Fatalf("error = %v; want wrapping ErrUnknownContext", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q, %v) unexpected error: %v", tc.ctx, tc.blocking, err)
			}
			if got != tc.want {
				t.Fatalf("Resolve(%q, %v) = %q; want %q", tc.ctx, tc.blocking, got, tc.want)
			}
		})
	}
}

// TestTerminate_PodTerminationGolden pins the exact wire shape of a
// one-shot dispatch pod's blocked:needs-input termination (spec §5 dispatch
// row, §8) — a golden byte-for-byte JSON comparison so a field-order or tag
// slip is caught immediately.
func TestTerminate_PodTerminationGolden(t *testing.T) {
	q := contracts.QuestionAsked{
		ID:       "q_deadbeefcafebabe",
		Source:   contracts.QuestionFromAgent,
		Blocking: true,
		Args: contracts.QuestionArgs{
			Text: "which container registry should the build push to?",
			Options: []contracts.QuestionOption{
				{Label: "ghcr"},
				{Label: "docker hub", Description: "public, rate-limited"},
			},
			FreeText: true,
		},
	}

	got := Terminate(q, "th_abc123")
	want := contracts.BlockedNeedsInput{Question: q, ThreadID: "th_abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Terminate() = %+v; want %+v", got, want)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal BlockedNeedsInput: %v", err)
	}
	const wantJSON = `{"question":{"id":"q_deadbeefcafebabe","source":"agent","blocking":true,"payload":{"text":"which container registry should the build push to?","options":[{"label":"ghcr"},{"label":"docker hub","description":"public, rate-limited"}],"free_text":true}},"thread_id":"th_abc123"}`
	if string(b) != wantJSON {
		t.Fatalf("Terminate() golden JSON mismatch:\n got: %s\nwant: %s", b, wantJSON)
	}
}
