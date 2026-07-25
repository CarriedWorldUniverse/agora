package turnengine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

func TestSink_RateLimit_TranslatesToEvRateLimit(t *testing.T) {
	resetsAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	got := emitAndCollect(t, bridle.RateLimit{
		Status:       "allowed_warning",
		WindowType:   "five_hour",
		Utilization:  82,
		ResetsAt:     resetsAt,
		UsingOverage: false,
		TS:           time.Now(),
	})
	if len(got) != 1 {
		t.Fatalf("got %d events; want 1", len(got))
	}
	ev := got[0]
	if ev.Type != contracts.EvRateLimit {
		t.Fatalf("Type = %q; want %q", ev.Type, contracts.EvRateLimit)
	}
	if ev.ThreadID != "th" || ev.TurnID != "tu" {
		t.Errorf("ThreadID/TurnID = %q/%q; want the sink's stamped th/tu", ev.ThreadID, ev.TurnID)
	}

	var p struct {
		RateLimit contracts.RateLimit `json:"rate_limit"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	rl := p.RateLimit
	if rl.Status != "allowed_warning" || rl.WindowType != "five_hour" || rl.Utilization != 82 {
		t.Errorf("payload = %+v; fields did not round-trip", rl)
	}
	if rl.ResetsAt == nil || !rl.ResetsAt.Equal(resetsAt) {
		t.Errorf("ResetsAt = %v; want %v", rl.ResetsAt, resetsAt)
	}
}

// A zero ResetsAt (the provider didn't report one) must serialize as an
// ABSENT field, not "0001-01-01T00:00:00Z" — the exact omitempty-on-a-
// struct gotcha contracts.RateLimit's pointer field exists to avoid.
func TestSink_RateLimit_ZeroResetsAtIsOmittedNotEpoch(t *testing.T) {
	got := emitAndCollect(t, bridle.RateLimit{Status: "allowed", WindowType: "seven_day", Utilization: 5})
	if len(got) != 1 {
		t.Fatalf("got %d events; want 1", len(got))
	}
	if body := string(got[0].Payload); strings.Contains(body, "0001-01-01") {
		t.Fatalf("payload leaked the zero-Time sentinel instead of omitting resets_at: %s", body)
	}

	var p struct {
		RateLimit contracts.RateLimit `json:"rate_limit"`
	}
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.RateLimit.ResetsAt != nil {
		t.Errorf("ResetsAt = %v; want nil when the provider reported none", p.RateLimit.ResetsAt)
	}
}
