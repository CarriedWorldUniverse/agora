package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/daemon"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// approvalPipeBy is the fixed attribution pipe mode uses (blueprint §3.2a):
// pipe mode has exactly one implicit client and no io.Session/Attachment at
// all (io/pipe.go), so there is no arbitration to resolve — bake the golden
// fixture's own `by` value, matching pod/helpers_test.go's
// dispatchDevice-style fixed test constant convention.
const approvalPipeBy = "agora:q7ymdevice001"

type execCommandPayload struct {
	Command string `json:"command"`
}

type exitCodePayload struct {
	ExitCode int `json:"exit_code"`
}

// driveFlowApprovalPipe is TestFlowApproval's 3.2a sub-drive (blueprint
// §3.2): a flowEngine pre-cans the model-originated trigger (thread.started/
// turn.started/approval.requested, then the post-approval item/turn.completed
// tail) and delegates the RESOLUTION to a real internal/approval.Result.
// Resolution conversion (via daemon.ResolveApproval, reused rather than
// hand-rolled per blueprint §1.5) — the approval.resolved event on the wire
// is produced by that real seam call, not a canned ScriptedTurn.Events
// entry.
func driveFlowApprovalPipe(t *testing.T) []byte {
	t.Helper()
	steps := []awaitStep{
		{Emit: []contracts.Event{
			newThreadStarted("th_0002", threadStartedPayload{IdentityFP: "agora:k5xw3zjanfzsa2lt", Profile: "dev", WorkingDir: "/work/demo"}),
			newTurnStarted("th_0002", "tu_0001"),
			{
				Type: contracts.EvApprovalRequested, ThreadID: "th_0002", TurnID: "tu_0001",
				Payload: mustMarshalJSON(contracts.ApprovalRequest{
					ID: "ap_0001", Kind: contracts.KindExec,
					Payload: execCommandPayload{Command: "go test ./... -run TestFlaky -count=100"},
				}),
			},
		}},
		{
			Await: contracts.InApprovalResponse, AwaitID: "ap_0001",
			Resolve: func(in contracts.Input) ([]contracts.Event, error) {
				res := daemon.ResolveApproval("ap_0001", contracts.KindExec, in, approvalPipeBy)
				return []contracts.Event{{
					Type: contracts.EvApprovalResolved, ThreadID: "th_0002", TurnID: "tu_0001",
					Payload: mustMarshalJSON(res),
				}}, nil
			},
		},
		{Emit: []contracts.Event{
			{Type: contracts.EvItemStarted, ThreadID: "th_0002", TurnID: "tu_0001", Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemCommandExecution}},
			{Type: contracts.EvItemCompleted, ThreadID: "th_0002", TurnID: "tu_0001", Item: &contracts.ItemRef{Seq: 1, Type: contracts.ItemCommandExecution}, Payload: mustMarshalJSON(exitCodePayload{ExitCode: 0})},
			newTurnCompleted("th_0002", "tu_0001", contracts.Usage{Input: 2400, Output: 210}),
		}},
	}
	engine := &flowEngine{steps: steps}

	in := strings.NewReader(
		`{"type":"user_message","text":"the flaky test is failing again"}` + "\n" +
			`{"type":"approval_response","id":"ap_0001","decision":"allow","scope":"once"}` + "\n",
	)
	var out, errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := agoraio.RunPipe(ctx, in, &out, &errBuf, engine, agoraio.PipeOptions{})
	if err != nil {
		t.Fatalf("RunPipe: %v", err)
	}
	if code != agoraio.ExitCompleted {
		t.Fatalf("exit code = %d, want ExitCompleted, stderr=%s", code, errBuf.String())
	}
	return out.Bytes()
}

// approvalResolvedFields decodes the {by, decision, stage} fields of an
// approval.resolved payload — used by the 3.2b sub-drive, which asserts
// FIELDS rather than a byte-match (blueprint §6 resolution 2).
func approvalResolvedFields(t *testing.T, ev contracts.Event) contracts.ApprovalResolution {
	t.Helper()
	var res contracts.ApprovalResolution
	if err := json.Unmarshal(ev.Payload, &res); err != nil {
		t.Fatalf("decode approval.resolved: %v", err)
	}
	return res
}
