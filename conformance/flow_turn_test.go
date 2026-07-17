package conformance

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// driveFlowTurn is TestFlowTurn's live drive (blueprint §3.1): pure pipe-mode
// item/turn mechanics, no seam to exercise (that's the point of this flow —
// it is the U2 wire-mechanics proof already established by
// internal/io/pipe_test.go's TestRunPipe_GoldenEventStream, reused here as
// the conformance suite's own assertion so the flow is proven from THIS
// package too, not "trust io's test").
func driveFlowTurn(t *testing.T, events []agoraio.ScriptedTurn) []byte {
	t.Helper()
	engine := &agoraio.ScriptedEngine{Script: events}
	in := strings.NewReader(`{"type":"user_message","text":"fix the failing test"}` + "\n")
	var out, errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := agoraio.RunPipe(ctx, in, &out, &errBuf, engine, agoraio.PipeOptions{Deltas: true})
	if err != nil {
		t.Fatalf("RunPipe: %v", err)
	}
	if code != agoraio.ExitCompleted {
		t.Fatalf("exit code = %d, want ExitCompleted", code)
	}
	return out.Bytes()
}
