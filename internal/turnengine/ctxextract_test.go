package turnengine

import (
	"context"
	"strings"
	"testing"

	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/ctxmap/extractor"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

func TestParseFacts_PlainArray(t *testing.T) {
	facts, err := parseFacts(`[{"statement":"the cache size is 64","kind":"OBSERVED","source":"user","entities":["cache"],"confidence":0.9}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Statement != "the cache size is 64" || facts[0].Kind != "OBSERVED" {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestParseFacts_FencedAndPreambled(t *testing.T) {
	// A model that wraps the array in prose + a ```json fence must still parse.
	out := "Sure, here are the facts:\n```json\n[{\"statement\":\"x\",\"kind\":\"OBSERVED\",\"source\":\"assistant\"}]\n```\nDone."
	facts, err := parseFacts(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Statement != "x" {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestParseFacts_ThinkPreamble(t *testing.T) {
	// Reasoning that lands in the content (not a separate stream) is stripped.
	out := "<think>let me find durable facts... the number is 7</think>\n[{\"statement\":\"the number is 7\",\"kind\":\"OBSERVED\",\"source\":\"user\"}]"
	facts, err := parseFacts(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Statement != "the number is 7" {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestParseFacts_NoArrayIsEmptyNotError(t *testing.T) {
	// A question / chit-chat turn yields no facts — that's a valid zero result,
	// not a failure that would spam the extraction worker with errors.
	facts, err := parseFacts("No durable facts in this turn.")
	if err != nil {
		t.Fatalf("want nil err for a no-facts reply, got %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("want 0 facts, got %+v", facts)
	}
}

func TestParseVerdict_PrecedenceAndThink(t *testing.T) {
	cases := map[string]extractor.PairVerdict{
		"SAME":                         extractor.PairSame,
		"CONTRADICTS":                  extractor.PairContradicts,
		"DISTINCT":                     extractor.PairDistinct,
		"not the SAME — CONTRADICTS":   extractor.PairContradicts, // CONTRADICTS wins over SAME
		"<think>hmm</think>\nDISTINCT": extractor.PairDistinct,
	}
	for in, want := range cases {
		got, err := parseVerdict(in)
		if err != nil || got != want {
			t.Fatalf("parseVerdict(%q) = %q,%v; want %q", in, got, err, want)
		}
	}
	if _, err := parseVerdict("I'm not sure"); err == nil {
		t.Fatal("want error when no verdict word is present")
	}
}

// TestActiveModelExtractor_CallsActiveProvider: Propose must issue a one-shot
// call to whatever provider active() returns and parse its FinalText — this is
// the "the configured model does the extraction" contract.
func TestActiveModelExtractor_CallsActiveProvider(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: `[{"statement":"teal is the favorite color","kind":"PREFERENCE","source":"user"}]`})
	ext := &activeModelExtractor{active: func() (bridle.Provider, string) { return provider, "kimi-k3" }}

	facts, err := ext.Propose(extractor.Turn{User: "my favorite color is teal", Assistant: "noted"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Kind != "PREFERENCE" {
		t.Fatalf("facts = %+v", facts)
	}
	// The extraction call went to the active provider on the active model, and
	// the current turn's text is in the (user) prompt.
	req := provider.LastRequest()
	if req.Model != "kimi-k3" {
		t.Fatalf("extraction model = %q, want the active model kimi-k3", req.Model)
	}
	if len(req.Messages) == 0 || !strings.Contains(req.Messages[len(req.Messages)-1].Content, "favorite color is teal") {
		t.Fatalf("extraction prompt missing the current turn: %+v", req.Messages)
	}
}

func TestActiveModelExtractor_NoActiveProviderErrors(t *testing.T) {
	ext := &activeModelExtractor{active: func() (bridle.Provider, string) { return nil, "" }}
	if _, err := ext.Propose(extractor.Turn{User: "hi", Assistant: "hi"}, nil, nil); err == nil {
		t.Fatal("want error when there is no active provider")
	}
}

// subprocessProvider reports the subprocess-stream category so we can assert the
// guard that keeps extraction off the claudesdk (sidecar) path.
type subprocessProvider struct{}

func (subprocessProvider) Name() bridle.ProviderID { return "claudesdk" }
func (subprocessProvider) Capabilities() bridle.ProviderCapabilities {
	return bridle.ProviderCapabilities{Category: bridle.CategorySubprocessStream}
}
func (subprocessProvider) RunTurn(_ context.Context, _ bridle.ProviderRequest, _ bridle.EventSink) (bridle.ProviderResult, error) {
	panic("extraction must NOT call RunTurn on a subprocess provider")
}

func TestActiveModelExtractor_SkipsSubprocessProvider(t *testing.T) {
	ext := &activeModelExtractor{active: func() (bridle.Provider, string) { return subprocessProvider{}, "claude-sonnet-5" }}
	// Must not panic (RunTurn never called) and must return an error so the
	// engine degrades this turn to plain-transcript instead of extracting.
	if _, err := ext.Propose(extractor.Turn{User: "remember 7", Assistant: "ok"}, nil, nil); err == nil {
		t.Fatal("want a skip error for a subprocess (claudesdk) provider")
	}
}
