package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	tea "github.com/charmbracelet/bubbletea"
)

func rateLimitEventPayload(t *testing.T, rl contracts.RateLimit) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		RateLimit contracts.RateLimit `json:"rate_limit"`
	}{RateLimit: rl})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRecordRateLimit_StoresTheReading(t *testing.T) {
	m := testModel(newFakeBackend())
	if m.haveRateLimit {
		t.Fatal("haveRateLimit true before any event")
	}
	m.recordRateLimit(rateLimitEventPayload(t, contracts.RateLimit{
		Status: "allowed_warning", WindowType: "five_hour", Utilization: 82,
	}))
	if !m.haveRateLimit {
		t.Fatal("recordRateLimit did not set haveRateLimit")
	}
	if m.rateLimit.Utilization != 82 || m.rateLimit.WindowType != "five_hour" {
		t.Errorf("stored reading = %+v", m.rateLimit)
	}
}

// A second reading REPLACES the first — it is not summed. Unlike token
// usage, utilization is the provider's own current snapshot.
func TestRecordRateLimit_SecondReadingReplacesNotAccumulates(t *testing.T) {
	m := testModel(newFakeBackend())
	m.recordRateLimit(rateLimitEventPayload(t, contracts.RateLimit{WindowType: "five_hour", Utilization: 40}))
	m.recordRateLimit(rateLimitEventPayload(t, contracts.RateLimit{WindowType: "five_hour", Utilization: 45}))
	if m.rateLimit.Utilization != 45 {
		t.Fatalf("Utilization = %d; want 45 (replace, not 40+45)", m.rateLimit.Utilization)
	}
}

func TestRecordRateLimit_MalformedPayloadIsIgnored(t *testing.T) {
	m := testModel(newFakeBackend())
	m.recordRateLimit([]byte(`not json`))
	if m.haveRateLimit {
		t.Fatal("malformed payload set haveRateLimit")
	}
}

// The main event loop must actually reach recordRateLimit — proving the
// EvRateLimit case exists in the switch, not just the method in isolation.
func TestModel_EvRateLimit_UpdatesState(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	cmds := m.handleEvent(contracts.Event{
		Type:    contracts.EvRateLimit,
		Payload: rateLimitEventPayload(t, contracts.RateLimit{WindowType: "seven_day", Utilization: 10}),
	})
	for _, c := range cmds {
		runCmd(c)
	}
	if !m.haveRateLimit || m.rateLimit.Utilization != 10 {
		t.Fatalf("EvRateLimit through Update did not update state: have=%v rl=%+v", m.haveRateLimit, m.rateLimit)
	}
}

// /status must not show a "plan" row until a reading has actually
// arrived — the overwhelming majority of sessions (non-subscription
// providers) never receive one, and a permanent placeholder row would be
// clutter promising a signal that will never come.
func TestSlashStatus_NoPlanRowBeforeAnyReading(t *testing.T) {
	m := testModel(newFakeBackend())
	printed := &[]string{}
	m.cfg.Printer = capturingPrinter(printed)
	m.composer.InsertText("/status")
	m.press(tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Contains((*printed)[0], "plan:") {
		t.Fatalf("/status showed a plan row with no rate-limit event ever received:\n%s", (*printed)[0])
	}
}

func TestSlashStatus_ShowsPlanRowAfterAReading(t *testing.T) {
	m := testModel(newFakeBackend())
	m.recordRateLimit(rateLimitEventPayload(t, contracts.RateLimit{
		Status: "allowed_warning", WindowType: "five_hour", Utilization: 82,
	}))
	printed := &[]string{}
	m.cfg.Printer = capturingPrinter(printed)
	m.composer.InsertText("/status")
	m.press(tea.KeyMsg{Type: tea.KeyEnter})
	out := (*printed)[0]
	for _, want := range []string{"plan:", "five_hour", "82%", "allowed_warning"} {
		if !strings.Contains(out, want) {
			t.Errorf("/status missing %q; got:\n%s", want, out)
		}
	}
}

func TestRenderRateLimit(t *testing.T) {
	cases := []struct {
		name string
		rl   contracts.RateLimit
		want []string
		not  []string
	}{
		{
			name: "plain reading, allowed",
			rl:   contracts.RateLimit{Status: "allowed", WindowType: "five_hour", Utilization: 12},
			want: []string{"five_hour", "12%"},
			// "allowed" (the boring, common case) must not clutter the line —
			// only a NON-default status is worth calling out.
			not: []string{"allowed)"},
		},
		{
			name: "warning status",
			rl:   contracts.RateLimit{Status: "allowed_warning", WindowType: "seven_day", Utilization: 90},
			want: []string{"seven_day", "90%", "allowed_warning"},
		},
		{
			name: "overage in use",
			rl:   contracts.RateLimit{Status: "allowed", WindowType: "overage", Utilization: 5, UsingOverage: true},
			want: []string{"overage", "5%", "overage credits in use"},
		},
		{
			name: "no window type reported",
			rl:   contracts.RateLimit{Status: "allowed", Utilization: 50},
			want: []string{"usage", "50%"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderRateLimit(tc.rl)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("renderRateLimit(%+v) = %q; missing %q", tc.rl, got, w)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("renderRateLimit(%+v) = %q; must not contain %q", tc.rl, got, n)
				}
			}
		})
	}
}

func TestRenderRateLimit_ResetsAtRendersLocalTime(t *testing.T) {
	resetsAt := time.Date(2026, 7, 25, 14, 32, 0, 0, time.UTC)
	got := renderRateLimit(contracts.RateLimit{WindowType: "five_hour", Utilization: 50, ResetsAt: &resetsAt})
	if !strings.Contains(got, "resets") {
		t.Fatalf("renderRateLimit did not mention a reset time: %q", got)
	}
}
