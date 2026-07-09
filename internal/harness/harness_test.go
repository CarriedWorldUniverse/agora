package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/backend"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/extractor"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/memory"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/render"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/store"
)

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

func newRig(t *testing.T, fp *fakeProvider, prop memory.Proposer, mapOn bool) (*Session, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := render.New(st)
	if err != nil {
		t.Fatal(err)
	}
	var eng *memory.Engine
	if mapOn {
		eng = memory.New(memory.Config{SessionID: "test"}, st, rend, prop, nil, nil)
	}
	s := NewSession(Config{Model: "fake", MapEnabled: mapOn}, fp, eng)
	t.Cleanup(func() { s.Close(); st.Close() })
	return s, st
}

func TestTurnInjectsMapBlocksAndExtractsAsync(t *testing.T) {
	fp := &fakeProvider{script: []backend.ProviderResult{{FinalText: "the answer"}}}
	prop := &fakeProposer{out: []extractor.FactProposal{
		{Statement: "the render node was renamed to forge-node", Kind: "OBSERVED", Source: "user", Force: extractor.ForceDecision, Entities: []string{"forge-node"}},
	}}
	s, st := newRig(t, fp, prop, true)

	res, err := s.Turn(context.Background(), "i renamed the render node to forge-node")
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "the answer" {
		t.Fatalf("answer: %q", res.Answer)
	}
	sys := fp.reqs[0].AppendSystemPrompt
	if !strings.Contains(sys, "Working memory (automatic)") || !strings.Contains(sys, "Working memory — core") {
		t.Fatalf("framing/core missing from system prompt:\n%s", sys)
	}
	if len(fp.reqs[0].Tools) != 2 {
		t.Fatalf("want recall+inspect tools, got %d", len(fp.reqs[0].Tools))
	}
	ids := s.WaitExtraction()
	if len(ids) != 1 {
		t.Fatalf("want 1 extracted fact, got %d", len(ids))
	}
	f, _ := st.Get(ids[0])
	if f.Status != store.StatusVerified || f.Trust != store.TrustOperatorStated {
		t.Fatalf("grounded decision: want OPERATOR_STATED/VERIFIED, got %s/%s", f.Trust, f.Status)
	}
}

func TestRecallToolRoundTrip(t *testing.T) {
	fp := &fakeProvider{}
	s, st := newRig(t, fp, &fakeProposer{}, true)
	id, err := st.AssertFact(store.Fact{
		Statement: "pool workers are named personality-role", Kind: store.KindObserved,
		Trust: store.TrustOperatorStated, Performative: true,
		Provenance: []store.Span{{SessionID: "x", Turn: 1, Start: 0, End: 5}},
		Entities:   []string{"pool-workers"},
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
	last := fp.reqs[len(fp.reqs)-1]
	found := false
	for _, m := range last.Messages {
		if m.Role == "tool_result" && strings.Contains(m.Content, id) {
			found = true
		}
	}
	if !found {
		t.Fatal("tool_result with recalled fact not threaded back")
	}
}

func TestMapOffAblation(t *testing.T) {
	fp := &fakeProvider{script: []backend.ProviderResult{{FinalText: "plain"}}}
	s, st := newRig(t, fp, nil, false)
	if _, err := s.Turn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fp.reqs[0].AppendSystemPrompt, "Working memory") {
		t.Fatal("map-off must not inject memory blocks")
	}
	if len(fp.reqs[0].Tools) != 0 {
		t.Fatal("map-off must not offer tools")
	}
	if facts, _ := st.QueryText("", 10, ""); len(facts) != 0 {
		t.Fatal("map-off must not extract")
	}
}

func TestToolBudgetExhaustionStillAnswers(t *testing.T) {
	fp := &toolLoopProvider{}
	st, _ := store.Open(":memory:")
	rend, _ := render.New(st)
	eng := memory.New(memory.Config{SessionID: "t"}, st, rend, &fakeProposer{}, nil, nil)
	s := NewSession(Config{Model: "fake", MapEnabled: true}, fp, eng)
	t.Cleanup(func() { s.Close(); st.Close() })
	res, err := s.Turn(context.Background(), "keep digging")
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "forced final answer" {
		t.Fatalf("want forced final answer, got %q", res.Answer)
	}
}

type toolLoopProvider struct{}

func (p *toolLoopProvider) Name() backend.ProviderID { return "fake" }
func (p *toolLoopProvider) Capabilities() backend.ProviderCapabilities {
	return backend.ProviderCapabilities{Category: backend.CategoryDirectAPI, SupportsCustomTools: true}
}
func (p *toolLoopProvider) RunTurn(_ context.Context, req backend.ProviderRequest, _ backend.EventSink) (backend.ProviderResult, error) {
	if len(req.Tools) == 0 {
		return backend.ProviderResult{FinalText: "forced final answer"}, nil
	}
	return backend.ProviderResult{ToolCalls: []backend.ToolInvocation{{ID: "x", Name: "recall", Args: json.RawMessage(`{"query":"more"}`)}}}, nil
}
