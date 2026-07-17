package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// recordingInvoker captures the resolved AgentCallOpts for every call, so
// tests can assert on §2a's model/effort resolution order without caring
// about the (echoed) Output value.
type recordingInvoker struct {
	opts []AgentCallOpts
}

func (r *recordingInvoker) InvokeAgent(_ context.Context, prompt string, opts AgentCallOpts) (AgentCallResult, error) {
	r.opts = append(r.opts, opts)
	return AgentCallResult{Output: []byte(`"ok"`)}, nil
}

// TestRun_PhaseDefaults_ResolveBelowExplicitCallArgs exercises spec §2a's
// resolution order: explicit model=/effort= on the call wins; otherwise the
// call's phase's meta.phases default applies; resolved independently for
// model and effort.
func TestRun_PhaseDefaults_ResolveBelowExplicitCallArgs(t *testing.T) {
	script := []byte(`
meta = workflow_meta(
    name = "routing-demo",
    description = "exercises phase-default vs explicit-call resolution",
    phases = [
        {"title": "Review", "model": "local-fast", "effort": "low"},
        {"title": "Verify", "model": "frontier", "effort": "high"},
    ],
)

def main(ctx, args):
    a = ctx.agent("p1", phase = "Review")
    b = ctx.agent("p2", phase = "Verify", model = "explicit-override")
    c = ctx.agent("p3")
    return {"a": a, "b": b, "c": c}
`)

	inv := &recordingInvoker{}
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-routing", Script: script, Args: map[string]any{},
		Clock: fakeClock{t: time.Unix(1, 0).UTC()}, Invoker: inv, Journal: NewMemJournalStore(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusCompleted {
		t.Fatalf("Status = %q; want completed (err=%v)", out.Status, out.Err)
	}
	if len(inv.opts) != 3 {
		t.Fatalf("got %d resolved calls; want 3", len(inv.opts))
	}

	review, verify, plain := inv.opts[0], inv.opts[1], inv.opts[2]

	if review.Model != "local-fast" || review.Effort != contracts.EffortLow {
		t.Fatalf("Review-phase call resolved to model=%q effort=%q; want local-fast/low from the phase default", review.Model, review.Effort)
	}
	if verify.Model != "explicit-override" {
		t.Fatalf("Verify-phase call with explicit model= resolved to %q; want the explicit override to win over the phase default", verify.Model)
	}
	if verify.Effort != contracts.EffortHigh {
		t.Fatalf("Verify-phase call (no explicit effort=) resolved to effort=%q; want high from the phase default", verify.Effort)
	}
	if plain.Model != "" || plain.Effort != "" {
		t.Fatalf("no-phase call resolved to model=%q effort=%q; want both empty (falls through to agent-def/parent inheritance downstream)", plain.Model, plain.Effort)
	}
}

func TestWorkflowMetaBuiltin_RequiresName(t *testing.T) {
	script := []byte(`
meta = workflow_meta(description = "no name given")
def main(ctx, args):
    return {}
`)
	_, err := Run(context.Background(), RunOptions{
		RunID: "run-noname", Script: script, Args: map[string]any{},
		Clock: fakeClock{t: time.Unix(1, 0).UTC()}, Invoker: &echoInvoker{}, Journal: NewMemJournalStore(),
	})
	if err == nil {
		t.Fatalf("Run succeeded with no workflow_meta name; want an error")
	}
}

func TestRun_LifetimeAgentCapEnforced(t *testing.T) {
	script := []byte(`
meta = workflow_meta(name = "cap-demo", description = "loops past the cap")
def main(ctx, args):
    total = 0
    for i in range(5):
        ctx.agent("p" + str(i))
        total += 1
    return {"total": total}
`)
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-cap", Script: script, Args: map[string]any{},
		Clock: fakeClock{t: time.Unix(1, 0).UTC()}, Invoker: &echoInvoker{}, Journal: NewMemJournalStore(),
		LifetimeAgentCap: 2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusErrored {
		t.Fatalf("Status = %q; want errored (lifetime cap 2 exceeded by 5 calls)", out.Status)
	}
}
