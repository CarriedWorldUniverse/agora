package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// applyUsage feeds a turn.completed event carrying u through the TUI's event
// handler, exactly as the backend channel would.
func applyUsage(m *Model, u contracts.Usage) {
	b, err := json.Marshal(struct {
		Usage contracts.Usage `json:"usage"`
	}{u})
	if err != nil {
		panic(err)
	}
	m.handleEvent(contracts.Event{Type: contracts.EvTurnCompleted, Payload: b})
}

func TestUsage_AccumulatesAcrossTurns(t *testing.T) {
	m := testModelWithRegistry(newFakeBackend(), testRegistry())
	applyUsage(m, contracts.Usage{Input: 1000, Output: 200, Cached: 500, Cost: 0.004})
	applyUsage(m, contracts.Usage{Input: 3000, Output: 300, Cached: 2500, Cost: 0.001})

	if m.sessIn != 4000 || m.sessOut != 500 || m.sessCached != 3000 {
		t.Fatalf("totals = in %d out %d cached %d, want 4000/500/3000", m.sessIn, m.sessOut, m.sessCached)
	}
	if m.sessCost != 0.005 {
		t.Fatalf("sessCost = %v, want 0.005 (provider-reported costs summed)", m.sessCost)
	}
	row := m.renderStatusRow()
	for _, want := range []string{"↑4.0k", "↓500", "cache 75%", "$0.0050"} {
		if !strings.Contains(row, want) {
			t.Fatalf("status row missing %q:\n%s", want, row)
		}
	}
}

func TestUsage_NoUsageNoSegment(t *testing.T) {
	m := testModelWithRegistry(newFakeBackend(), testRegistry())
	row := m.renderStatusRow()
	if strings.Contains(row, "↑") || strings.Contains(row, "$") {
		t.Fatalf("status row shows usage before any turn completed:\n%s", row)
	}
}

func TestUsage_PricingFallbackWhenProviderReportsNoCost(t *testing.T) {
	reg := testRegistry()
	reg["opus"] = ModelEntry{Model: "claude-opus-4-8",
		Pricing: &ModelPricing{Input: 15, Output: 75, CachedInput: 1.5}}
	m := testModelWithRegistry(newFakeBackend(), reg)
	m.turnModelID = "claude-opus-4-8" // the model the turn ran on

	// 10k input of which 8k cached, 1k output, NO provider cost:
	// (2k·$15 + 8k·$1.5 + 1k·$75)/1e6 = (30000+12000+75000)/1e6 = $0.117
	applyUsage(m, contracts.Usage{Input: 10_000, Output: 1_000, Cached: 8_000})
	if got := m.sessCost; got < 0.1169 || got > 0.1171 {
		t.Fatalf("sessCost = %v, want ≈0.117 from the price table", got)
	}
}

func TestUsage_ProviderCostBeatsPricingTable(t *testing.T) {
	reg := testRegistry()
	reg["kimi"] = ModelEntry{Model: "kimi-k3", BaseURL: "http://x/v1",
		Pricing: &ModelPricing{Input: 999, Output: 999}} // absurd table that must NOT be used
	m := testModelWithRegistry(newFakeBackend(), reg)
	m.turnModelID = "kimi-k3"

	applyUsage(m, contracts.Usage{Input: 1000, Output: 100, Cost: 0.002})
	if m.sessCost != 0.002 {
		t.Fatalf("sessCost = %v, want the provider-reported 0.002, not a table estimate", m.sessCost)
	}
}

func TestUsage_NoCostNoPricingOmitsDollar(t *testing.T) {
	m := testModelWithRegistry(newFakeBackend(), testRegistry())
	m.turnModelID = "claude-sonnet-5" // registry entry has no pricing
	applyUsage(m, contracts.Usage{Input: 500, Output: 50})
	row := m.renderStatusRow()
	if !strings.Contains(row, "↑500") {
		t.Fatalf("tokens missing from row:\n%s", row)
	}
	if strings.Contains(row, "$") {
		t.Fatalf("row claims a cost with nothing priced:\n%s", row)
	}
}

func TestModelPricing_Cost(t *testing.T) {
	p := &ModelPricing{Input: 3, Output: 15, CachedInput: 0.3}
	// 1M fresh input + 1M cached + 1M output = 3 + 0.3 + 15
	if got := p.Cost(2_000_000, 1_000_000, 1_000_000); got != 18.3 {
		t.Fatalf("Cost = %v, want 18.3", got)
	}
	// cached > input is clamped (defensive; providers shouldn't report it)
	if got := p.Cost(100, 200, 0); got != 200*0.3/1e6 {
		t.Fatalf("Cost with cached>input = %v, want cached-only pricing", got)
	}
}
