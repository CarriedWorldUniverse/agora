// Turn-scoped trace log: a small diagnostics buffer over observe
// TurnFrame snapshots. The broker re-emits a FULL snapshot per turn on
// every change (replace-by-TurnID), so the log stores the latest frame
// per TurnID — most recent traceTurnCap turns, evicting the oldest by
// Started — and renders compact one-liners for the trace pane.
//
// All labels are kept (main/compact/filter-judge): the pane is
// diagnostics, unlike presence which only watches in-flight main turns.
package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/opclient"
)

// traceTurnCap is how many distinct turns the trace log retains.
const traceTurnCap = 3

// One-liner truncation bounds (display columns are precious; full
// payloads stay on the broker side).
const (
	traceInputMax  = 60 // tool-call input summary
	traceResultMax = 60 // tool result preview
	traceTextMax   = 80 // text event first-line
)

// traceLog holds the latest snapshot per TurnID, ordered oldest-first
// by Started so lines() renders chronologically top-to-bottom.
type traceLog struct {
	turns []opclient.TurnFrame
}

// apply folds one turn snapshot into the log: replace in place when the
// TurnID is already tracked, otherwise append and evict the oldest by
// Started past traceTurnCap. Empty TurnIDs are dropped (nothing to key on).
func (t *traceLog) apply(turn opclient.TurnFrame) {
	if turn.TurnID == "" {
		return
	}
	for i := range t.turns {
		if t.turns[i].TurnID == turn.TurnID {
			t.turns[i] = turn
			t.sortByStarted()
			return
		}
	}
	t.turns = append(t.turns, turn)
	t.sortByStarted()
	if len(t.turns) > traceTurnCap {
		t.turns = t.turns[len(t.turns)-traceTurnCap:]
	}
}

func (t *traceLog) sortByStarted() {
	sort.SliceStable(t.turns, func(i, j int) bool {
		return t.turns[i].Started.Before(t.turns[j].Started)
	})
}

// lines renders the retained turns as compact one-liners, newest turn
// LAST. Turn events carry no timestamps — ordering within a turn is the
// only chronology shown.
func (t *traceLog) lines() []string {
	var out []string
	for _, turn := range t.turns {
		out = append(out, traceTurnHeader(turn))
		for _, ev := range turn.Events {
			out = append(out, traceEventLine(ev))
		}
	}
	return out
}

// traceTurnHeader renders the per-turn header rule:
//
//	── HH:MM:SS turn <label> (in flight|complete 4m12s|errored: <err>) ──
func traceTurnHeader(turn opclient.TurnFrame) string {
	label := turn.Label
	if label == "" {
		label = "main"
	}
	var state string
	switch turn.Status {
	case "in_flight":
		state = "in flight"
	case "complete":
		state = "complete"
		if turn.Ended != nil {
			state += " " + formatTurnDuration(turn.Ended.Sub(turn.Started))
		}
	case "errored":
		state = "errored: " + turn.Error
	default:
		state = turn.Status
	}
	return fmt.Sprintf("── %s turn %s (%s) ──", turn.Started.Format("15:04:05"), label, state)
}

// formatTurnDuration renders a completed turn's wall time at whole-second
// granularity ("4m12s", "37s").
func formatTurnDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Truncate(time.Second).String()
}

// traceEventLine renders one TurnEvent as a single indented line:
//
//	▸ Name({"input":…}) → result preview     tool_call
//	· first ~80 chars of text                text
//	step N                                   step marker
//	▸ (orphan) preview                       tool_result_orphan
func traceEventLine(ev opclient.TurnEvent) string {
	switch ev.Kind {
	case "tool_call":
		return "  ▸ " + traceToolLine(ev.Tool)
	case "step":
		return fmt.Sprintf("  step %d", ev.Step)
	case "tool_result_orphan":
		return strings.TrimRight("  ▸ (orphan) "+truncateOneLine(orphanPreview(ev), traceResultMax), " ")
	default: // "text" and anything unrecognised renders its Text
		return "  · " + truncateOneLine(ev.Text, traceTextMax)
	}
}

// traceToolLine renders the call + result halves of a tool event. A nil
// Result means the call is still running ("→ …"); IsError flags the
// preview as the error text.
func traceToolLine(tc *opclient.ToolCall) string {
	if tc == nil {
		return "(tool?)"
	}
	line := tc.Name + "(" + truncateOneLine(compactJSON(tc.Input), traceInputMax) + ")"
	switch {
	case tc.Result == nil:
		line += " → …"
	case tc.Result.IsError:
		line += " → ERR " + truncateOneLine(tc.Result.Preview, traceResultMax)
	default:
		line += " → " + truncateOneLine(tc.Result.Preview, traceResultMax)
	}
	return strings.TrimRight(line, " ")
}

// orphanPreview pulls the best preview text from a tool_result_orphan
// event: Text when set, else any attached result preview.
func orphanPreview(ev opclient.TurnEvent) string {
	if ev.Text != "" {
		return ev.Text
	}
	if ev.Tool != nil && ev.Tool.Result != nil {
		return ev.Tool.Result.Preview
	}
	return ""
}

// compactJSON minifies raw JSON for the input summary; invalid JSON
// falls back to the trimmed raw bytes (diagnostics beat strictness).
func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return buf.String()
}

// truncateOneLine collapses all whitespace runs (incl. newlines) to
// single spaces and bounds the result to max display runes, ellipsis
// included.
func truncateOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if max > 0 && len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}
