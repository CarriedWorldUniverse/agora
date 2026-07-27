package enginerunner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/subagent"
	"github.com/CarriedWorldUniverse/agora/internal/turnengine"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

const testTimeout = 5 * time.Second

// commandExecCompletedWire mirrors turnengine's unexported
// commandExecCompletedPayload wire shape ({command,output,error}) — the
// agent tool's item.completed payload, since itemTypeForTool has no
// dedicated ItemType for it and falls back to ItemCommandExecution (see
// turnengine/sink.go's itemTypeForTool doc comment). Duplicated here
// (rather than importing an unexported type) the same way this repo's own
// packages duplicate wire shapes across package boundaries (c.f.
// turnengine/sink.go's itemPayload doc comment on contracts/testdata being
// the frozen cross-package contract).
type commandExecCompletedWire struct {
	Command string `json:"command"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
}

// runParentToClose drives one turn on parent (a *turnengine.Manager already
// wired with turnengine.WithSubagents) and returns every event observed,
// closing the turn out cleanly (mirrors Run's own drain-then-InEnd pattern
// in enginerunner.go, one level up: this is the PARENT's turn, not a
// child's).
func runParentToClose(t *testing.T, parent *turnengine.Manager, userText string) []contracts.Event {
	t.Helper()
	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 256)
	runErr := make(chan error, 1)
	go func() { runErr <- parent.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: userText}

	var events []contracts.Event
	endSent := false
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				if err := <-runErr; err != nil {
					t.Fatalf("parent Run: %v", err)
				}
				return events
			}
			events = append(events, ev)
			if (ev.Type == contracts.EvTurnCompleted || ev.Type == contracts.EvTurnFailed) && !endSent {
				endSent = true
				in <- contracts.Input{Type: contracts.InEnd}
			}
		case <-time.After(testTimeout):
			t.Fatal("timed out waiting for parent turn events")
		}
	}
}

// TestEndToEnd_ParentSpawnsChild_ResultRoundTrips is the round trip this
// unit exists to prove: a parent turn calls agent(prompt) → a REAL child
// turnengine.Manager runs on the fake provider (scripted child reply) → the
// parent's tool call receives the child's text as the tool result → the
// parent's own closing message completes the turn — with the agent graph
// edge and the child's transcript actually persisted.
func TestEndToEnd_ParentSpawnsChild_ResultRoundTrips(t *testing.T) {
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_parent", CreatedAt: time.Unix(1000, 0).UTC()}); err != nil {
		t.Fatalf("create parent thread: %v", err)
	}

	childProvider := fake.NewProvider(fake.Step{Text: "child result text"})
	runner := New(childProvider, store)
	graph := subagent.NewMemGraphStore()
	subMgr := subagent.NewManager(store, graph, subagent.NewRegistry(nil), runner)
	subMgr.RegisterRoot("th_parent", subagent.ParentContext{
		Cwd:    "/",
		Policy: contracts.BuiltinPresets()[contracts.PresetAutoSafe],
	})

	agentArgs, err := json.Marshal(map[string]string{"prompt": "child task"})
	if err != nil {
		t.Fatalf("marshal agent args: %v", err)
	}
	parentProvider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{{ID: "1", Name: "agent", Args: agentArgs}}},
		fake.Step{Text: "parent closing message"},
	)

	policy := contracts.BuiltinPresets()[contracts.PresetAutoSafe] // KindExec: auto — the agent() tool classifies as KindExec (toolrunner/classify.go)
	parent := turnengine.NewManager("th_parent", parentProvider,
		turnengine.WithStore(store),
		turnengine.WithPolicy(policy),
		turnengine.WithContextEngine(false),
		turnengine.WithSubagents(subMgr),
	)

	events := runParentToClose(t, parent, "please delegate")

	var sawTurnCompleted bool
	var agentToolOutput string
	var sawAgentToolOutput bool
	for _, ev := range events {
		if ev.Type == contracts.EvTurnCompleted {
			sawTurnCompleted = true
		}
		if ev.Type == contracts.EvItemCompleted && ev.Item != nil && ev.Item.Type == contracts.ItemCommandExecution {
			var p commandExecCompletedWire
			if json.Unmarshal(ev.Payload, &p) == nil && p.Command != "" {
				agentToolOutput = p.Output
				sawAgentToolOutput = true
			}
		}
	}
	if !sawTurnCompleted {
		t.Fatal("parent turn never reached turn.completed")
	}
	if !sawAgentToolOutput {
		t.Fatal("never saw the agent tool's item.completed event")
	}
	if agentToolOutput != "child result text" {
		t.Fatalf("agent tool result = %q; want the child's final message %q", agentToolOutput, "child result text")
	}

	// Parent's own closing agent_message (its "parent closing message" step)
	// must have completed the turn too — i.e. the parent kept running its
	// OWN turn after the tool result came back, not just relayed the child.
	var sawParentClosing bool
	for _, ev := range events {
		if ev.Type == contracts.EvItemCompleted && ev.Item != nil && ev.Item.Type == contracts.ItemAgentMessage {
			var p struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && p.Text == "parent closing message" {
				sawParentClosing = true
			}
		}
	}
	if !sawParentClosing {
		t.Fatal("parent's own closing message never completed")
	}

	// --- Persisted graph edge + child thread/transcript ---
	children, err := subMgr.Children("th_parent")
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("th_parent children = %v; want exactly 1", children)
	}
	childID := children[0]

	meta, err := store.Meta(childID)
	if err != nil {
		t.Fatalf("store.Meta(%s): %v", childID, err)
	}
	if meta.ParentThread != "th_parent" {
		t.Fatalf("child meta.ParentThread = %q; want th_parent", meta.ParentThread)
	}

	edge, ok, err := graph.Edge("th_parent", childID)
	if err != nil {
		t.Fatalf("graph.Edge: %v", err)
	}
	if !ok {
		t.Fatal("no agent_edges row for (th_parent, child)")
	}
	// Deviation from the ticket's literal "open→closed" phrasing, documented
	// in the build report: subagent.Manager (pre-existing, unmodified by
	// this unit) only ever closes an edge on CANCELLATION (cancel.go) — a
	// normally-completed agent stays resumable-by-continuation (spec §2a)
	// and its edge stays OPEN, matching graph.go's own doc comment ("open:
	// the child is a live part of the graph"). Asserting Closed here would
	// contradict that pre-existing, spec-consistent behavior.
	if edge.Status != subagent.EdgeOpen {
		t.Fatalf("edge status = %s; want open (normal completion does not close the edge — see subagent/cancel.go)", edge.Status)
	}

	it, err := store.Resume(childID)
	if err != nil {
		t.Fatalf("store.Resume(child): %v", err)
	}
	defer it.Close()
	n := 0
	var sawChildAgentMessage bool
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		n++
		if item.Type == contracts.TIAgentMessage {
			sawChildAgentMessage = true
		}
	}
	if n == 0 {
		t.Fatal("no items persisted for the child thread")
	}
	if !sawChildAgentMessage {
		t.Fatal("child transcript has no agent_message item (the child's own closing text)")
	}

	// --- Depth guard: the child's turnengine.Manager must never have
	// carried the agent tool itself (no recursive spawning, spec §2 depth
	// cap default 1) ---
	toolNames := make(map[string]bool)
	for _, td := range childProvider.LastRequest().Tools {
		toolNames[td.Name] = true
	}
	if toolNames[toolAgentName] {
		t.Fatalf("child's tool set includes %q — depth guard failed, child must not get the agent tool", toolAgentName)
	}
	if !toolNames["Read"] { // sanity: the child DID get a real, non-empty tool surface (fs family)
		t.Fatalf("child's tool set = %v; want it to at least include the fs family's Read", toolNames)
	}
}

// toolAgentName mirrors toolrunner.ToolAgent's value ("agent") without an
// import cycle concern (there is none here — this is just avoiding a
// second, easy-to-typo string literal in the test above).
const toolAgentName = "agent"

// TestEndToEnd_AgentSpawnDeniedByPolicy_NoChildThreadCreated: when the
// parent's policy denies KindExec (the kind agent() classifies as —
// toolrunner/classify.go), the spawn never runs — no child thread, no
// graph edge — proving the tool passes through the SAME beforeToolCall
// approval gate every other tool does (bridle's Deny short-circuits before
// ToolRunner.Run/AgentFamily.Execute is ever invoked — see
// surfacerunner.go's doc comment).
func TestEndToEnd_AgentSpawnDeniedByPolicy_NoChildThreadCreated(t *testing.T) {
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "th_parent", CreatedAt: time.Unix(1000, 0).UTC()}); err != nil {
		t.Fatalf("create parent thread: %v", err)
	}

	childProvider := fake.NewProvider(fake.Step{Text: "should never run"})
	runner := New(childProvider, store)
	graph := subagent.NewMemGraphStore()
	subMgr := subagent.NewManager(store, graph, subagent.NewRegistry(nil), runner)
	subMgr.RegisterRoot("th_parent", subagent.ParentContext{Cwd: "/"})

	agentArgs, err := json.Marshal(map[string]string{"prompt": "child task"})
	if err != nil {
		t.Fatalf("marshal agent args: %v", err)
	}
	parentProvider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{{ID: "1", Name: "agent", Args: agentArgs}}},
		fake.Step{Text: "acknowledged the denial"},
	)

	denyPolicy := contracts.PolicySet{contracts.KindExec: contracts.PolicyDeny}
	parent := turnengine.NewManager("th_parent", parentProvider,
		turnengine.WithStore(store),
		turnengine.WithPolicy(denyPolicy),
		turnengine.WithContextEngine(false),
		turnengine.WithSubagents(subMgr),
	)

	events := runParentToClose(t, parent, "please delegate")

	var sawTurnCompleted bool
	for _, ev := range events {
		if ev.Type == contracts.EvTurnCompleted {
			sawTurnCompleted = true
		}
	}
	if !sawTurnCompleted {
		t.Fatal("parent turn never reached turn.completed")
	}
	if childProvider.StepsRemaining() != 1 {
		t.Fatalf("child provider steps remaining = %d; want 1 (the child must never have run)", childProvider.StepsRemaining())
	}

	children, err := subMgr.Children("th_parent")
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if len(children) != 0 {
		t.Fatalf("th_parent children = %v; want none — a policy-denied spawn must not create a child thread", children)
	}
}

// blockingProvider never returns from RunTurn until ctx is cancelled —
// used to prove Run (this package's AgentRunner) respects ctx cancellation
// promptly (subagent.AgentRunner's documented contract, runner.go: "Run
// must respect ctx cancellation promptly").
type blockingProvider struct{}

func (blockingProvider) Name() bridle.ProviderID { return "blocking" }

func (blockingProvider) Capabilities() bridle.ProviderCapabilities {
	return bridle.ProviderCapabilities{Category: bridle.CategoryDirectAPI}
}

func (blockingProvider) RunTurn(ctx context.Context, req bridle.ProviderRequest, sink bridle.EventSink) (bridle.ProviderResult, error) {
	<-ctx.Done()
	return bridle.ProviderResult{}, ctx.Err()
}

// TestRun_CtxCancellation_ReturnsPromptly proves Run's cancellation
// contract directly against a provider that never completes on its own:
// cancelling ctx must make Run return quickly with an error, not hang.
//
// Full end-to-end Esc-interrupt-cancels-a-foreground-child (agora-spec-
// subagents.md §2a: "Background children keep running ... Foreground
// spawns are cancelled with the turn") is NOT independently re-proven
// end-to-end here — v1's agent() tool is always foreground/synchronous, so
// this Run-level guarantee (ctx cancellation → prompt return) is exactly
// what a parent-turn interrupt propagating through bridle's BeforeToolCall
// hook ctx relies on; verifying bridle's own ctx threading down to
// ToolRunner.Run end-to-end is bridle's contract, not this package's, and
// is a documented cut (see the build report) rather than a silent gap.
func TestRun_CtxCancellation_ReturnsPromptly(t *testing.T) {
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "ag_blocked", CreatedAt: time.Unix(1000, 0).UTC()}); err != nil {
		t.Fatalf("create child thread: %v", err)
	}
	runner := New(blockingProvider{}, store)

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, subagent.RunRequest{AgentID: "ag_blocked", ParentThread: "th_parent", Prompt: "never finishes"})
		resultCh <- err
	}()

	// Give Run a moment to actually start the child turn before cancelling,
	// so this proves mid-flight cancellation, not "cancel raced construction".
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-resultCh:
		if err == nil {
			t.Fatal("Run returned nil error after ctx cancellation; want a cancellation error")
		}
	case <-time.After(testTimeout):
		t.Fatal("Run did not return promptly after ctx cancellation")
	}
}
