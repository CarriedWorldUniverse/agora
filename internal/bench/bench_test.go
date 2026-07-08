package bench

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/backend"
	"github.com/CarriedWorldUniverse/agora/internal/extractor"
	"github.com/CarriedWorldUniverse/agora/internal/harness"
	"github.com/CarriedWorldUniverse/agora/internal/render"
	"github.com/CarriedWorldUniverse/agora/internal/store"
)

type scriptProvider struct{ answers []string }

func (s *scriptProvider) Name() backend.ProviderID { return "fake" }
func (s *scriptProvider) Capabilities() backend.ProviderCapabilities {
	return backend.ProviderCapabilities{Category: backend.CategoryDirectAPI}
}
func (s *scriptProvider) RunTurn(context.Context, backend.ProviderRequest, backend.EventSink) (backend.ProviderResult, error) {
	if len(s.answers) == 0 {
		return backend.ProviderResult{FinalText: "ok"}, nil
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return backend.ProviderResult{FinalText: a, Usage: backend.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

type scriptProposer struct{ perTurn [][]extractor.FactProposal }

func (s *scriptProposer) Propose(extractor.Turn, []extractor.Turn, map[string]string) ([]extractor.FactProposal, error) {
	if len(s.perTurn) == 0 {
		return nil, nil
	}
	p := s.perTurn[0]
	s.perTurn = s.perTurn[1:]
	return p, nil
}

func TestRunWorkloadWithProbes(t *testing.T) {
	w := &Workload{
		ID: "smoke", Version: 1, PressureTier: "fits-in-window",
		Turns: []WorkloadTurn{
			{Message: "the broker moves to li1 today"},
			{Message: "unrelated chatter", PadTo: 50},
			{Message: "where does the broker live?",
				Probe: &Probe{Type: "contains", Want: "li1"}},
			{Message: "check the store",
				Probe: &Probe{Type: "fact_in_store", Want: "the broker moves to li1"}},
			{Message: "audit",
				Probe: &Probe{Type: "no_live_contradiction"}},
		},
	}
	prov := &scriptProvider{answers: []string{"noted", "ok", "the broker is on li1", "done", "done"}}
	prop := &scriptProposer{perTurn: [][]extractor.FactProposal{
		{{Statement: "the broker moves to li1", Kind: "OBSERVED", Source: "user", Entities: []string{"broker"}}},
	}}
	mk := func() (*harness.Session, *store.Store, func(), error) {
		st, err := store.Open(":memory:")
		if err != nil {
			return nil, nil, nil, err
		}
		rend, err := render.New(st)
		if err != nil {
			return nil, nil, nil, err
		}
		sess := harness.NewSession(harness.Config{Model: "fake", MapEnabled: true}, prov, st, rend, prop)
		return sess, st, func() { sess.Close(); st.Close() }, nil
	}

	fp := Fingerprint{HarnessRev: "test", BackendModel: "fake", MapEnabled: true}
	rec, err := Run(context.Background(), w, fp, 0, mk)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Probes) != 3 {
		t.Fatalf("want 3 probe results, got %d", len(rec.Probes))
	}
	for _, p := range rec.Probes {
		if !p.Pass {
			t.Fatalf("probe %s/%s failed: %s", p.Type, p.Want, p.Detail)
		}
	}
	if rec.PassRate != 1.0 {
		t.Fatalf("pass rate: %f", rec.PassRate)
	}
	if rec.FactCount != 1 {
		t.Fatalf("fact count: %d", rec.FactCount)
	}

	// bench.db round trip + per-fingerprint aggregation
	db, err := OpenDB(filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Append(*rec); err != nil {
		t.Fatal(err)
	}
	rates, err := db.PassRates("smoke")
	if err != nil {
		t.Fatal(err)
	}
	if rates[fp.Hash()] != 1.0 {
		t.Fatalf("aggregated pass rate: %+v", rates)
	}
}

func TestFillerDeterministic(t *testing.T) {
	a, b := filler("w1", 3, 100), filler("w1", 3, 100)
	if a != b {
		t.Fatal("filler must be deterministic per (workload, turn)")
	}
	if filler("w1", 4, 100) == a {
		t.Fatal("different turns must produce different filler")
	}
}

func TestFingerprintDiscriminates(t *testing.T) {
	a := Fingerprint{HarnessRev: "r1", MapEnabled: true}
	b := a
	b.MapEnabled = false
	if a.Hash() == b.Hash() {
		t.Fatal("fingerprint must change with config")
	}
	var back Fingerprint
	j, _ := json.Marshal(a)
	json.Unmarshal(j, &back)
	if back.Hash() != a.Hash() {
		t.Fatal("fingerprint must round-trip")
	}
}
