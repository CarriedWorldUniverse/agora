package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestComposer_InsertAndValue(t *testing.T) {
	c := NewComposer()
	c.InsertText("hello")
	if c.Value() != "hello" {
		t.Fatalf("Value() = %q", c.Value())
	}
}

func TestComposer_ActiveTrigger_SlashAtStart(t *testing.T) {
	c := NewComposer()
	c.InsertText("/mod")
	kind, query, start := c.ActiveTrigger()
	if kind != TriggerSlash || query != "mod" || start != 0 {
		t.Fatalf("got (%v,%q,%d)", kind, query, start)
	}
}

func TestComposer_ActiveTrigger_NotMidWord(t *testing.T) {
	c := NewComposer()
	c.InsertText("foo@bar")
	kind, _, _ := c.ActiveTrigger()
	if kind != TriggerNone {
		t.Fatalf("kind = %v, want TriggerNone (not a boundary)", kind)
	}
}

func TestComposer_ActiveTrigger_AfterWhitespaceIsBoundary(t *testing.T) {
	c := NewComposer()
	c.InsertText("hi @fi")
	kind, query, start := c.ActiveTrigger()
	if kind != TriggerAt || query != "fi" || start != 3 {
		t.Fatalf("got (%v,%q,%d)", kind, query, start)
	}
}

func TestComposer_ActiveTrigger_ClosesOnWhitespace(t *testing.T) {
	c := NewComposer()
	c.InsertText("/model gpt-5 ")
	kind, _, _ := c.ActiveTrigger()
	if kind != TriggerNone {
		t.Fatalf("kind = %v, want TriggerNone once whitespace follows", kind)
	}
}

func TestComposer_AllFourTriggerKinds(t *testing.T) {
	cases := map[string]TriggerKind{
		"/x": TriggerSlash, "@x": TriggerAt, "$x": TriggerSkill, "%x": TriggerOverride,
	}
	for input, want := range cases {
		c := NewComposer()
		c.InsertText(input)
		kind, _, _ := c.ActiveTrigger()
		if kind != want {
			t.Errorf("input %q: kind = %v, want %v", input, kind, want)
		}
	}
}

func TestComposer_CompleteToken_AtomicBackspaceDeletesWholeUnit(t *testing.T) {
	c := NewComposer()
	c.InsertText("/mo")
	c.CompleteToken("/model")
	if c.Value() != "/model " {
		t.Fatalf("Value() = %q", c.Value())
	}
	c.Backspace() // deletes the trailing space
	c.Backspace() // should delete the whole atomic "/model" token, not one rune
	if c.Value() != "" {
		t.Fatalf("Value() = %q, want empty (atomic token deleted as one unit)", c.Value())
	}
}

func TestComposer_LargePasteCollapsesToPlaceholder(t *testing.T) {
	c := NewComposer()
	big := strings.Repeat("x", pasteCollapseThreshold+1)
	c.InsertText(big)
	if strings.Contains(c.Value(), "xxxxxxxxxx") {
		t.Fatalf("large paste landed verbatim: %q", c.Value())
	}
	if !strings.HasPrefix(c.Value(), "[Pasted Content") {
		t.Fatalf("Value() = %q, want a placeholder", c.Value())
	}
}

func TestComposer_Submit_ExpandsPasteAtSubmit(t *testing.T) {
	c := NewComposer()
	c.InsertText("see: ")
	big := strings.Repeat("y", pasteCollapseThreshold+5)
	c.InsertText(big)
	c.InsertText(" thanks")
	text, sent := c.Submit()
	if !sent {
		t.Fatalf("Submit() sent = false")
	}
	if !strings.Contains(text, big) {
		t.Fatalf("Submit() = %q, want the full pasted text expanded back in", text)
	}
	if strings.Contains(text, "[Pasted Content") {
		t.Fatalf("Submit() still contains the placeholder: %q", text)
	}
}

func TestComposer_Submit_EmptyIsNoop(t *testing.T) {
	c := NewComposer()
	c.InsertText("   ")
	_, sent := c.Submit()
	if sent {
		t.Fatalf("Submit() sent = true for whitespace-only input")
	}
}

func TestComposer_QueueWhileRunning(t *testing.T) {
	c := NewComposer()
	c.SetRunning(true)
	c.InsertText("do the thing")
	text, sent := c.Submit()
	if sent || text != "" {
		t.Fatalf("Submit() while running = (%q,%v), want (\"\",false)", text, sent)
	}
	if got := c.Queued(); len(got) != 1 || got[0] != "do the thing" {
		t.Fatalf("Queued() = %v", got)
	}
	drained := c.DrainQueued()
	if len(drained) != 1 || len(c.Queued()) != 0 {
		t.Fatalf("DrainQueued() = %v, Queued() after = %v", drained, c.Queued())
	}
}

func TestComposer_History_UpDownRoundTrip(t *testing.T) {
	c := NewComposer()
	c.InsertText("first")
	c.Submit()
	c.InsertText("second")
	c.Submit()

	c.HistoryUp()
	if c.Value() != "second" {
		t.Fatalf("HistoryUp() once = %q", c.Value())
	}
	c.HistoryUp()
	if c.Value() != "first" {
		t.Fatalf("HistoryUp() twice = %q", c.Value())
	}
	c.HistoryDown()
	if c.Value() != "second" {
		t.Fatalf("HistoryDown() = %q", c.Value())
	}
	c.HistoryDown()
	if c.Value() != "" {
		t.Fatalf("HistoryDown() past the end = %q, want empty", c.Value())
	}
}

func TestParseOverride_ModelAndEffort(t *testing.T) {
	model, effort, rest, ok, err := ParseOverride("%frontier:high fix this race condition", func(a string) bool { return a == "frontier" })
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if model != "frontier" || effort != contracts.EffortHigh || rest != "fix this race condition" {
		t.Fatalf("got model=%q effort=%q rest=%q", model, effort, rest)
	}
}

func TestParseOverride_EffortOnly(t *testing.T) {
	model, effort, rest, ok, err := ParseOverride("%:high raise effort only", nil)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if model != "" || effort != contracts.EffortHigh || rest != "raise effort only" {
		t.Fatalf("got model=%q effort=%q rest=%q", model, effort, rest)
	}
}

func TestParseOverride_ModelOnlyDefaultsToHighEffort(t *testing.T) {
	_, effort, _, ok, err := ParseOverride("%frontier do the thing", func(a string) bool { return true })
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if effort != contracts.EffortHigh {
		t.Fatalf("effort = %q, want high (default)", effort)
	}
}

func TestParseOverride_NoOverridePrefix(t *testing.T) {
	_, _, rest, ok, err := ParseOverride("just a normal message", nil)
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	if rest != "just a normal message" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestParseOverride_UnknownAliasErrorsInline(t *testing.T) {
	_, _, _, ok, err := ParseOverride("%bogus-model hello", func(a string) bool { return false })
	if !ok || !errors.Is(err, ErrUnresolvableOverride) {
		t.Fatalf("ok=%v err=%v, want ok=true err=ErrUnresolvableOverride", ok, err)
	}
}

func TestParseOverride_UnknownEffortErrorsInline(t *testing.T) {
	_, _, _, ok, err := ParseOverride("%frontier:ludicrous go", func(a string) bool { return true })
	if !ok || !errors.Is(err, ErrUnresolvableOverride) {
		t.Fatalf("ok=%v err=%v, want ok=true err=ErrUnresolvableOverride", ok, err)
	}
}
