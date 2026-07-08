package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/backend"
	"github.com/CarriedWorldUniverse/agora/internal/extractor"
	"github.com/CarriedWorldUniverse/agora/internal/render"
	"github.com/CarriedWorldUniverse/agora/internal/store"
)

// fakeProvider replays a script of ProviderResults and records requests.
type fakeProvider struct {
	script []backend.ProviderResult
	reqs   []backend.ProviderRequest
}

func (f *fakeProvider) Name() backend.ProviderID { return "fake" }
func (f *fakeProvider) Capabilities() backend.ProviderCapabilities {
	return backend.ProviderCapabilities{Category: backend.CategoryDirectAPI, SupportsCustomTools: true}
}
func (f *fakeProvider) RunTurn(_ context.Context, req backend.ProviderRequest, _ backend.EventSink) (backend.ProviderResult, error) {
	f.reqs = append(f.reqs, req)
	if len(f.script) == 0 {
		return backend.ProviderResult{FinalText: "ok"}, nil
	}
	r := f.script[0]
	f.script = f.script[1:]
	return r, nil
}

type fakeProposer struct{ out []extractor.FactProposal }

func (f *fakeProposer) Propose(extractor.Turn, []extractor.Turn, map[string]string) ([]extractor.FactProposal, error) {
	return f.out, nil
}

func newRig(t *testing.T, fp *fakeProvider, prop Proposer, mapOn bool) (*Session, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := render.New(st)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession(Config{Model: "fake", MapEnabled: mapOn}, fp, st, rend, prop)
	t.Cleanup(func() { s.Close(); st.Close() })
	return s, st
}

func TestTurnInjectsMapBlocksAndExtractsAsync(t *testing.T) {
	fp := &fakeProvider{script: []backend.ProviderResult{{FinalText: "the answer"}}}
	prop := &fakeProposer{out: []extractor.FactProposal{
		{Statement: "the render node was renamed to forge-node", Kind: "OBSERVED", Entities: []string{"forge-node"}, Confidence: 0.9},
	}}
	s, st := newRig(t, fp, prop, true)

	res, err := s.Turn(context.Background(), "i renamed the render node to forge-node")
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "the answer" {
		t.Fatalf("answer: %q", res.Answer)
	}
	// cache-friendly layout: core (stable) in the system prompt; the per-turn
	// subgraph rides at the END of messages, just before the user message
	sys := fp.reqs[0].AppendSystemPrompt
	if !strings.Contains(sys, "Working memory — core") {
		t.Fatalf("core block missing from system prompt:\n%s", sys)
	}
	if strings.Contains(sys, "Working memory — relevant now") {
		t.Fatal("subgraph churn must NOT be in the system prompt (breaks prefix cache)")
	}
	m := fp.reqs[0].Messages
	if len(m) < 2 || !strings.Contains(m[len(m)-2].Content, "Working memory — relevant now") {
		t.Fatalf("subgraph block must precede the user message at the prompt end")
	}
	// tools offered
	if len(fp.reqs[0].Tools) != 2 {
		t.Fatalf("want recall+inspect tools, got %d", len(fp.reqs[0].Tools))
	}
	// async extraction lands with provenance
	ids := s.WaitExtraction()
	if len(ids) != 1 {
		t.Fatalf("want 1 extracted fact, got %d", len(ids))
	}
	f, err := st.Get(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Provenance) != 1 || f.Provenance[0].Turn != 1 {
		t.Fatalf("provenance not attached: %+v", f.Provenance)
	}
	if f.Status != store.StatusProposed {
		t.Fatalf("model fact must enter PROPOSED, got %s", f.Status)
	}
}

func TestRecallToolRoundTrip(t *testing.T) {
	// seed the store with a fact the model will recall
	fp := &fakeProvider{}
	s, st := newRig(t, fp, &fakeProposer{}, true)
	id, err := st.AssertFact(store.Fact{
		Statement: "pool workers are named personality-role", Kind: store.KindObserved,
		Trust: store.TrustOperatorStated, Provenance: []store.Span{{SessionID: "x", Turn: 1, Start: 0, End: 5}},
		Entities: []string{"pool-workers"},
	})
	if err != nil {
		t.Fatal(err)
	}

	fp.script = []backend.ProviderResult{
		{ToolCalls: []backend.ToolInvocation{{ID: "c1", Name: "recall", Args: json.RawMessage(`{"query":"pool workers naming"}`)}}},
		{FinalText: "workers are named personality-role"},
	}
	res, err := s.Turn(context.Background(), "what did we decide about worker naming?")
	if err != nil {
		t.Fatal(err)
	}
	if res.RecallCalls != 1 {
		t.Fatalf("want 1 recall call, got %d", res.RecallCalls)
	}
	// second request must contain the tool result with the fact
	last := fp.reqs[len(fp.reqs)-1]
	found := false
	for _, m := range last.Messages {
		if m.Role == "tool_result" && strings.Contains(m.Content, id) {
			found = true
		}
	}
	if !found {
		t.Fatal("tool_result with recalled fact not threaded back to provider")
	}
	if !strings.Contains(res.Answer, "personality-role") {
		t.Fatalf("final answer lost: %q", res.Answer)
	}
}

func TestMapOffAblation(t *testing.T) {
	fp := &fakeProvider{script: []backend.ProviderResult{{FinalText: "plain"}}}
	prop := &fakeProposer{out: []extractor.FactProposal{{Statement: "should not be stored", Kind: "OBSERVED"}}}
	s, st := newRig(t, fp, prop, false)

	if _, err := s.Turn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fp.reqs[0].AppendSystemPrompt, "Working memory") {
		t.Fatal("map-off must not inject memory blocks")
	}
	if len(fp.reqs[0].Tools) != 0 {
		t.Fatal("map-off must not offer tools")
	}
	s.WaitExtraction()
	if facts, _ := st.QueryText("should not", 5); len(facts) != 0 {
		t.Fatal("map-off must not extract facts")
	}
}

func TestDedupAndOperatorCorrection(t *testing.T) {
	fp := &fakeProvider{script: []backend.ProviderResult{{FinalText: "a"}, {FinalText: "b"}}}
	prop := &fakeProposer{out: []extractor.FactProposal{
		{Statement: "bench results are stored in sqlite for the harness", Kind: "OBSERVED", Entities: []string{"bench-db"}, Confidence: 0.9},
	}}
	s, st := newRig(t, fp, prop, true)
	s.Turn(context.Background(), "bench results go to sqlite")
	first := s.WaitExtraction()
	if len(first) != 1 {
		t.Fatalf("want 1 fact, got %d", len(first))
	}
	// same fact re-extracted => dedup (no second row), original re-confirmed
	prop.out = []extractor.FactProposal{
		{Statement: "bench results are stored in sqlite for the harness", Kind: "OBSERVED", Entities: []string{"bench-db"}, Confidence: 0.9},
	}
	s.Turn(context.Background(), "as noted, sqlite for bench")
	s.WaitExtraction()
	all, _ := st.QueryEntity("bench-db", 10)
	if len(all) != 1 {
		t.Fatalf("dedup failed: %d facts for bench-db", len(all))
	}
	// contradiction candidate (same entity, overlapping-but-different statement)
	prop.out = []extractor.FactProposal{
		{Statement: "bench results are stored in postgres for the harness", Kind: "OBSERVED", Entities: []string{"bench-db"}, Confidence: 0.9},
	}
	fp.script = []backend.ProviderResult{{FinalText: "c"}}
	s.Turn(context.Background(), "correction: postgres")
	s.WaitExtraction()
	all, _ = st.QueryEntity("bench-db", 10)
	live := 0
	for _, f := range all {
		if f.Status != store.StatusRetracted {
			live++
			links, _ := st.Links(f.ID)
			if len(links[store.LinkContradicts]) == 0 {
				t.Fatal("contradiction link missing")
			}
		}
	}
	if live != 2 {
		t.Fatalf("equal-trust contradiction must keep both flagged (got %d live)", live)
	}
}

func TestSourceAttributionTrust(t *testing.T) {
	fp := &fakeProvider{script: []backend.ProviderResult{{FinalText: "a"}, {FinalText: "b"}, {FinalText: "c"}}}
	prop := &fakeProposer{}
	s, st := newRig(t, fp, prop, true)

	// user-sourced AND grounded in user text => OPERATOR_STATED, enters VERIFIED
	prop.out = []extractor.FactProposal{{Statement: "the render seat moves to ember-node", Kind: "OBSERVED", Source: "user", Entities: []string{"ember-node"}}}
	s.Turn(context.Background(), "decision: the render seat moves to ember-node today")
	ids := s.WaitExtraction()
	f, _ := st.Get(ids[0])
	if f.Trust != store.TrustOperatorStated || f.Status != store.StatusVerified {
		t.Fatalf("grounded user fact: want OPERATOR_STATED/VERIFIED, got %s/%s", f.Trust, f.Status)
	}

	// extractor CLAIMS user but statement is not grounded in user words => stays model trust
	prop.out = []extractor.FactProposal{{Statement: "the belief field diffusion runs on gpu shaders nightly", Kind: "OBSERVED", Source: "user", Entities: []string{"belief-field"}}}
	s.Turn(context.Background(), "ok sounds good, carry on")
	ids = s.WaitExtraction()
	f, _ = st.Get(ids[0])
	if f.Trust != store.TrustModelObserved || f.Status != store.StatusProposed {
		t.Fatalf("ungrounded user-claimed fact: want MODEL_OBSERVED/PROPOSED, got %s/%s", f.Trust, f.Status)
	}

	// assistant-sourced => model trust regardless of overlap
	prop.out = []extractor.FactProposal{{Statement: "the build is green after the fix", Kind: "OBSERVED", Source: "assistant", Entities: []string{"build"}}}
	s.Turn(context.Background(), "the build is green after the fix you said?")
	ids = s.WaitExtraction()
	f, _ = st.Get(ids[0])
	if f.Trust != store.TrustModelObserved {
		t.Fatalf("assistant fact: want MODEL_OBSERVED, got %s", f.Trust)
	}
}
