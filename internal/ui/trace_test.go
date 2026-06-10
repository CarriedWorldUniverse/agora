package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/opclient"
)

var traceBase = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

func turnFrame(id, label, status string, started time.Time, events ...opclient.TurnEvent) opclient.TurnFrame {
	return opclient.TurnFrame{
		TurnID:  id,
		Label:   label,
		Status:  status,
		Started: started,
		Events:  events,
	}
}

func toolEvent(name, input string, preview string, isErr bool) opclient.TurnEvent {
	tc := &opclient.ToolCall{Name: name, Input: json.RawMessage(input)}
	tc.Result = &struct {
		Preview string `json:"preview"`
		IsError bool   `json:"is_error"`
	}{Preview: preview, IsError: isErr}
	return opclient.TurnEvent{Kind: "tool_call", Tool: tc}
}

func TestTraceLog_RetainsLatestThreeReplacingByTurnID(t *testing.T) {
	var tl traceLog
	ids := []string{"t1", "t2", "t3", "t4", "t5"}
	for i, id := range ids {
		started := traceBase.Add(time.Duration(i) * time.Minute)
		// in_flight snapshot first…
		tl.apply(turnFrame(id, "", "in_flight", started))
		// …then the complete snapshot for the same TurnID (replace, not append).
		complete := turnFrame(id, "", "complete", started,
			opclient.TurnEvent{Kind: "text", Text: "reply for " + id})
		ended := started.Add(30 * time.Second)
		complete.Ended = &ended
		tl.apply(complete)
	}

	if len(tl.turns) != traceTurnCap {
		t.Fatalf("retained turns: want %d got %d", traceTurnCap, len(tl.turns))
	}
	for i, want := range []string{"t3", "t4", "t5"} {
		if got := tl.turns[i].TurnID; got != want {
			t.Fatalf("turns[%d]: want %s got %s", i, want, got)
		}
	}

	out := strings.Join(tl.lines(), "\n")
	for _, evicted := range []string{"t1", "t2"} {
		if strings.Contains(out, "reply for "+evicted) {
			t.Fatalf("evicted turn %s still rendered:\n%s", evicted, out)
		}
	}
	// Replace-not-duplicate: exactly one header per retained turn, and
	// it reflects the LATEST (complete) snapshot, not the in_flight one.
	if got := strings.Count(out, "── 12:04:00 turn main"); got != 1 {
		t.Fatalf("t5 header count: want 1 got %d in:\n%s", got, out)
	}
	if strings.Contains(out, "in flight") {
		t.Fatalf("stale in_flight snapshot survived replacement:\n%s", out)
	}
}

func TestTraceLog_EvictsOldestByStartedNotArrival(t *testing.T) {
	var tl traceLog
	// Arrival order deliberately differs from Started order: the OLDEST
	// by Started (t-old) arrives last and must be the one evicted.
	tl.apply(turnFrame("t-b", "", "in_flight", traceBase.Add(2*time.Minute)))
	tl.apply(turnFrame("t-c", "", "in_flight", traceBase.Add(3*time.Minute)))
	tl.apply(turnFrame("t-d", "", "in_flight", traceBase.Add(4*time.Minute)))
	tl.apply(turnFrame("t-old", "", "in_flight", traceBase))

	if len(tl.turns) != 3 {
		t.Fatalf("retained turns: want 3 got %d", len(tl.turns))
	}
	for _, turn := range tl.turns {
		if turn.TurnID == "t-old" {
			t.Fatalf("oldest-by-Started turn survived eviction: %+v", tl.turns)
		}
	}
	// Chronological order preserved: oldest first, newest last.
	for i, want := range []string{"t-b", "t-c", "t-d"} {
		if got := tl.turns[i].TurnID; got != want {
			t.Fatalf("turns[%d]: want %s got %s", i, want, got)
		}
	}
}

func TestTraceLog_IgnoresEmptyTurnID(t *testing.T) {
	var tl traceLog
	tl.apply(turnFrame("", "", "in_flight", traceBase))
	if len(tl.turns) != 0 {
		t.Fatalf("empty TurnID stored: %+v", tl.turns)
	}
}

func TestTraceLog_HeaderFormats(t *testing.T) {
	started := traceBase // 12:00:00

	inFlight := turnFrame("t1", "", "in_flight", started)
	if got := traceTurnHeader(inFlight); got != "── 12:00:00 turn main (in flight) ──" {
		t.Fatalf("in_flight header: got %q", got)
	}

	complete := turnFrame("t2", "compact", "complete", started)
	ended := started.Add(4*time.Minute + 12*time.Second)
	complete.Ended = &ended
	if got := traceTurnHeader(complete); got != "── 12:00:00 turn compact (complete 4m12s) ──" {
		t.Fatalf("complete header: got %q", got)
	}

	errored := turnFrame("t3", "filter-judge", "errored", started)
	errored.Error = "context deadline exceeded"
	if got := traceTurnHeader(errored); got != "── 12:00:00 turn filter-judge (errored: context deadline exceeded) ──" {
		t.Fatalf("errored header: got %q", got)
	}
}

func TestTraceLog_ToolCallLine(t *testing.T) {
	ev := toolEvent("Bash", `{"command": "go test ./..."}`, "ok: 42 passed", false)
	got := traceEventLine(ev)
	want := `  ▸ Bash({"command":"go test ./..."}) → ok: 42 passed`
	if got != want {
		t.Fatalf("tool_call line:\nwant %q\ngot  %q", want, got)
	}
}

func TestTraceLog_ToolCallInputTruncated(t *testing.T) {
	long := `{"path":"` + strings.Repeat("a", 100) + `"}`
	ev := toolEvent("Read", long, "done", false)
	got := traceEventLine(ev)
	if !strings.Contains(got, "…") {
		t.Fatalf("long input not truncated: %q", got)
	}
	open := strings.Index(got, "(")
	close := strings.LastIndex(got, ")")
	if open < 0 || close < open {
		t.Fatalf("malformed tool line: %q", got)
	}
	if n := len([]rune(got[open+1 : close])); n > traceInputMax {
		t.Fatalf("input summary %d runes, want ≤%d: %q", n, traceInputMax, got)
	}
}

func TestTraceLog_ToolCallErrorResult(t *testing.T) {
	ev := toolEvent("Bash", `{"command":"false"}`, "exit status 1", true)
	got := traceEventLine(ev)
	if !strings.Contains(got, "→ ERR exit status 1") {
		t.Fatalf("error result not flagged: %q", got)
	}
}

func TestTraceLog_ToolCallPendingResult(t *testing.T) {
	ev := opclient.TurnEvent{Kind: "tool_call", Tool: &opclient.ToolCall{
		Name: "Bash", Input: json.RawMessage(`{"command":"sleep 5"}`),
	}}
	got := traceEventLine(ev)
	if !strings.HasSuffix(got, "→ …") {
		t.Fatalf("pending result marker missing: %q", got)
	}
}

func TestTraceLog_ToolResultPreviewTruncated(t *testing.T) {
	ev := toolEvent("Bash", `{}`, strings.Repeat("x", 100), false)
	got := traceEventLine(ev)
	tail := got[strings.Index(got, "→ ")+len("→ "):]
	if n := len([]rune(tail)); n > traceResultMax {
		t.Fatalf("result preview %d runes, want ≤%d: %q", n, traceResultMax, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long preview not truncated: %q", got)
	}
}

func TestTraceLog_TextEventOneLinedAndTruncated(t *testing.T) {
	text := "first line\nsecond line " + strings.Repeat("y", 100)
	got := traceEventLine(opclient.TurnEvent{Kind: "text", Text: text})
	if strings.Contains(got, "\n") {
		t.Fatalf("text event not one-lined: %q", got)
	}
	if !strings.HasPrefix(got, "  · first line second line") {
		t.Fatalf("text event prefix/joining wrong: %q", got)
	}
	body := strings.TrimPrefix(got, "  · ")
	if n := len([]rune(body)); n > traceTextMax {
		t.Fatalf("text body %d runes, want ≤%d", n, traceTextMax)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long text not truncated: %q", got)
	}
}

func TestTraceLog_StepEvent(t *testing.T) {
	got := traceEventLine(opclient.TurnEvent{Kind: "step", Step: 3})
	if got != "  step 3" {
		t.Fatalf("step line: got %q", got)
	}
}

func TestTraceLog_OrphanResultEvent(t *testing.T) {
	got := traceEventLine(opclient.TurnEvent{Kind: "tool_result_orphan", Text: "late result body"})
	if got != "  ▸ (orphan) late result body" {
		t.Fatalf("orphan line: got %q", got)
	}
}

func TestTraceLog_LinesChronologicalNewestLast(t *testing.T) {
	var tl traceLog
	tl.apply(turnFrame("new", "", "in_flight", traceBase.Add(time.Minute)))
	tl.apply(turnFrame("old", "", "in_flight", traceBase))
	lines := tl.lines()
	if len(lines) != 2 {
		t.Fatalf("lines: want 2 got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "12:00:00") || !strings.Contains(lines[1], "12:01:00") {
		t.Fatalf("turns not chronological oldest-first: %v", lines)
	}
}
