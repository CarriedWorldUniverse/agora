package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

type uiHarness struct {
	m       *Model
	printed []string
	now     time.Time
}

func newUIHarness(t *testing.T) *uiHarness {
	t.Helper()
	h := &uiHarness{now: time.Unix(0, 0).UTC()}
	h.m = NewModel(Config{
		AgentID: "agora", Theme: PlainTheme(),
		Printer: capturingPrinter(&h.printed),
		Now:     func() time.Time { return h.now },
	})
	h.m.running = true
	h.m.turnStart = h.now
	h.m.currentModel = "test-model"
	return h
}

func (h *uiHarness) send(typ contracts.EventType, seq int64, it contracts.ItemType, p map[string]any) {
	b, _ := json.Marshal(p)
	h.m.handleEvent(contracts.Event{Type: typ, Item: &contracts.ItemRef{Seq: seq, Type: it}, Payload: b})
}

func (h *uiHarness) run(seq int64, cmd string, d time.Duration, errMsg string) {
	h.send(contracts.EvItemStarted, seq, contracts.ItemCommandExecution, map[string]any{"command": cmd})
	h.now = h.now.Add(d)
	body := map[string]any{}
	if errMsg != "" {
		body["error"] = errMsg
	}
	h.send(contracts.EvItemCompleted, seq, contracts.ItemCommandExecution, body)
}

func (h *uiHarness) out() string { return strings.Join(h.printed, "\n") }

// A fast success must stay silent. Printing an outcome per call would double
// the transcript to say "the thing that usually happens, happened" — the
// signal is in the exceptions.
func TestProgress_FastSuccessIsSilent(t *testing.T) {
	h := newUIHarness(t)
	h.run(1, "grep -rn foo .", 300*time.Millisecond, "")
	if got := h.out(); strings.Contains(got, "✓") {
		t.Errorf("fast success printed an outcome line:\n%s", got)
	}
	if !strings.Contains(h.out(), "$ grep -rn foo .") {
		t.Errorf("the command line itself is missing:\n%s", h.out())
	}
}

// A SLOW success is the thing that was previously invisible: after the fact
// there was no way to tell which step ate the minutes.
func TestProgress_SlowSuccessReportsDuration(t *testing.T) {
	h := newUIHarness(t)
	h.run(1, "go test ./...", 12400*time.Millisecond, "")
	got := h.out()
	if !strings.Contains(got, "✓") || !strings.Contains(got, "12.4s") {
		t.Errorf("slow success did not report its duration:\n%s", got)
	}
}

// Failed-fast and failed-after-two-minutes are different problems.
func TestProgress_FailureCarriesDuration(t *testing.T) {
	h := newUIHarness(t)
	h.run(1, "go build ./...", 900*time.Millisecond, "undefined: bar")
	got := h.out()
	if !strings.Contains(got, "✗") || !strings.Contains(got, "undefined: bar") {
		t.Errorf("failure not reported:\n%s", got)
	}
	if !strings.Contains(got, "900ms") {
		t.Errorf("failure did not carry its duration:\n%s", got)
	}
}

// An unmatched completion (no observed start) must not invent a duration.
func TestProgress_UnknownStartOmitsDuration(t *testing.T) {
	h := newUIHarness(t)
	h.send(contracts.EvItemCompleted, 99, contracts.ItemCommandExecution, map[string]any{"error": "boom"})
	got := h.out()
	if !strings.Contains(got, "boom") {
		t.Fatalf("failure not reported:\n%s", got)
	}
	if strings.Contains(got, "(") {
		t.Errorf("invented a duration for an item whose start was never seen:\n%s", got)
	}
}

// The stall indicator is the whole point of agora#152: a spinner cannot
// distinguish "working" from "wedged".
func TestStatusRow_StallIndicator(t *testing.T) {
	h := newUIHarness(t)
	h.run(1, "go test ./...", time.Second, "")

	h.now = h.now.Add(10 * time.Second)
	if seg := h.m.stallSegment(); seg != "" {
		t.Errorf("stall reported after only 10s: %q — a healthy turn must stay quiet", seg)
	}
	if strings.Contains(h.m.renderStatusRow(), "no activity") {
		t.Error("status row claimed a stall during normal operation")
	}

	h.now = h.now.Add(2 * time.Minute)
	row := h.m.renderStatusRow()
	if !strings.Contains(row, "no activity") {
		t.Errorf("no stall indicator after 2m of silence:\n%s", row)
	}
}

// Ordering is the design claim: liveness first, cost next, ambient context
// last. Asserted by relative position so the exact separators can change.
func TestStatusRow_OrderedByUrgency(t *testing.T) {
	h := newUIHarness(t)
	h.m.haveUsage = true
	h.m.sessIn, h.m.sessOut, h.m.sessCost = 5000, 1200, 0.42
	h.now = h.now.Add(3 * time.Minute)
	row := h.m.renderStatusRow()

	elapsed := strings.Index(row, "3m0s")
	cost := strings.Index(row, "$0.42")
	model := strings.Index(row, "test-model")
	if elapsed < 0 || cost < 0 || model < 0 {
		t.Fatalf("row missing an expected segment: %q", row)
	}
	if !(elapsed < cost && cost < model) {
		t.Errorf("segments out of priority order (want elapsed < cost < model): %q", row)
	}
}

// cache% is an idle-time curiosity: it informs no in-flight decision, and the
// running row is the one competing for attention.
func TestStatusRow_CacheOnlyWhenIdle(t *testing.T) {
	h := newUIHarness(t)
	h.m.haveUsage = true
	h.m.sessIn, h.m.sessCached, h.m.sessOut = 5000, 39000, 1200
	if strings.Contains(h.m.renderStatusRow(), "cache") {
		t.Error("cache% shown on the running row; it belongs to the idle row")
	}
	h.m.running = false
	if !strings.Contains(h.m.renderStatusRow(), "cache") {
		t.Error("cache% missing from the idle row")
	}
}
