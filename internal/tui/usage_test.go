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
	// Counts are disjoint (contracts.Usage): total submitted = in + cached.
	applyUsage(m, contracts.Usage{Input: 500, Output: 200, Cached: 500, Cost: 0.004})
	applyUsage(m, contracts.Usage{Input: 500, Output: 300, Cached: 2500, Cost: 0.001})

	if m.sessIn != 1000 || m.sessOut != 500 || m.sessCached != 3000 {
		t.Fatalf("totals = in %d out %d cached %d, want 1000/500/3000", m.sessIn, m.sessOut, m.sessCached)
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

	// 2k uncached + 8k cache-read (disjoint), 1k output, NO provider cost:
	// (2k·$15 + 8k·$1.5 + 1k·$75)/1e6 = (30000+12000+75000)/1e6 = $0.117
	applyUsage(m, contracts.Usage{Input: 2_000, Output: 1_000, Cached: 8_000})
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
	// Disjoint counts: 1M uncached + 1M cache-read + 1M output = 3 + 0.3 + 15
	if got := p.Cost(1_000_000, 1_000_000, 0, 1_000_000); got != 18.3 {
		t.Fatalf("Cost = %v, want 18.3", got)
	}
	// cache_write unset → writes priced at Anthropic's 1.25× input premium
	if got := p.Cost(0, 0, 1_000_000, 0); got != 3.75 {
		t.Fatalf("Cost of 1M cache-writes = %v, want 3.75 (1.25× input default)", got)
	}
	// explicit cache_write rate wins over the default
	pw := &ModelPricing{Input: 3, Output: 15, CacheWrite: 6}
	if got := pw.Cost(0, 0, 1_000_000, 0); got != 6 {
		t.Fatalf("Cost with configured cache_write = %v, want 6", got)
	}
}

// TestUsage_AnthropicDisjointCachePercent is the "cache percentage comes
// back wrong for claude" regression (2026-07-21). The Anthropic lane
// reports input/cached/cache_write DISJOINT — a warm turn is a tiny
// uncached count next to a large cache-read count. Dividing cached by
// input alone printed absurd percentages (3800%); the denominator must be
// the total submitted prompt.
func TestUsage_AnthropicDisjointCachePercent(t *testing.T) {
	m := testModelWithRegistry(newFakeBackend(), testRegistry())
	applyUsage(m, contracts.Usage{Input: 50, Output: 40, Cached: 1900, CacheWrite: 50})
	row := m.renderStatusRow()
	for _, want := range []string{"↑2.0k", "cache 95%"} {
		if !strings.Contains(row, want) {
			t.Fatalf("status row missing %q:\n%s", want, row)
		}
	}
	if strings.Contains(row, "cache 3800%") {
		t.Fatalf("cached/input regression is back:\n%s", row)
	}
}
