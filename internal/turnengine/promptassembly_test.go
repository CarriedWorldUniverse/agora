package turnengine

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// TestComposeDevSystemPrompt_OrderedSegmentsAndDeterministic pins the §1
// segment order (core contract, profile block, environment — identity is
// out of this unit's scope, see devSystemPrompt's doc comment) and the §3
// "pure function; byte-stable when inputs are stable" contract: two calls
// with the same model produce byte-identical output.
func TestComposeDevSystemPrompt_OrderedSegmentsAndDeterministic(t *testing.T) {
	const model = "claude-sonnet-5"

	a := composeDevSystemPrompt(model)
	b := composeDevSystemPrompt(model)
	if a != b {
		t.Fatalf("composeDevSystemPrompt is not deterministic for stable inputs:\nfirst call:\n%s\n\nsecond call:\n%s", a, b)
	}

	// Core-contract marker: SystemPromptAppend mode drops tool-discipline
	// (the host CLI restates it) but keeps the rest — "## approvals" is the
	// first surviving builtin/core.md section header (see CoreSectionOrder).
	idxCore := strings.Index(a, "## approvals")
	if idxCore < 0 {
		t.Fatalf("composed prompt missing the core-contract marker %q:\n%s", "## approvals", a)
	}

	idxProfile := strings.Index(a, devSystemPrompt)
	if idxProfile < 0 {
		t.Fatalf("composed prompt missing the dev profile block:\n%s", a)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	idxWD := strings.Index(a, "working_dir: "+wd)
	if idxWD < 0 {
		t.Fatalf("composed prompt missing working_dir=%q in the environment segment:\n%s", wd, a)
	}
	idxModel := strings.Index(a, "model: "+model)
	if idxModel < 0 {
		t.Fatalf("composed prompt missing model=%q in the environment segment:\n%s", model, a)
	}

	if !(idxCore < idxProfile && idxProfile < idxWD && idxWD < idxModel) {
		t.Fatalf("segments out of §1 order (want core < profile < environment, wd before model within it): core=%d profile=%d wd=%d model=%d\n%s",
			idxCore, idxProfile, idxWD, idxModel, a)
	}
}

// TestManager_DevProfile_ComposedPromptReachesProvider is the Manager-level
// mirror of TestManager_ContextEngine_WorkingStateInSystemPrompt: a fake
// provider captures ProviderRequest.AppendSystemPrompt on a zero-option
// (DevProfile) Manager, and the composed core+profile content must arrive
// alongside ctxmap's framing appended on top (bridleadapter.Attach reads
// TurnRequest.AppendSystemPrompt as its `sys` base and appends Framing+Core
// after it — see internal/turnengine/manager.go's attachContextEngine doc
// comment) — the composed prompt must not clobber that append.
func TestManager_DevProfile_ComposedPromptReachesProvider(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "hi"})
	m := NewManager("th_prompt_reaches_provider", provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001"}}))
	if m.eng == nil {
		t.Fatal("NewManager: context engine did not construct (m.eng nil) — expected default-ON")
	}

	runOneManagerTurn(t, m)

	sys := provider.LastRequest().AppendSystemPrompt
	if !strings.Contains(sys, "## approvals") {
		t.Fatalf("ProviderRequest.AppendSystemPrompt missing the core-contract marker; got:\n%s", sys)
	}
	if !strings.Contains(sys, devSystemPrompt) {
		t.Fatalf("ProviderRequest.AppendSystemPrompt missing the dev profile block; got:\n%s", sys)
	}
	if !strings.Contains(sys, "Working memory (automatic)") {
		t.Fatalf("ProviderRequest.AppendSystemPrompt missing ctxmap's framing (composed prompt clobbered it); got:\n%s", sys)
	}
	// The composed content must come BEFORE ctxmap's framing — bridleadapter
	// appends onto whatever AppendSystemPrompt already carries.
	if strings.Index(sys, devSystemPrompt) > strings.Index(sys, "Working memory (automatic)") {
		t.Fatalf("composed prompt did not precede ctxmap's framing; got:\n%s", sys)
	}
}

// TestManager_DevProfile_AppendSystemPromptStableAcrossTurns is the
// cache-stability regression: two turns on ONE Manager must see byte-
// identical AppendSystemPrompt. composeDevSystemPrompt runs exactly once,
// at DevProfile()/NewManager construction time (see its doc comment's
// CACHE WARNING, NEX-793) — a per-turn recompute (even one that only
// changes "date") would bust the claudesdk/anthropic system-prompt cache
// on every turn.
func TestManager_DevProfile_AppendSystemPromptStableAcrossTurns(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "first"}, fake.Step{Text: "second"})
	m := NewManager("th_prompt_stable_across_turns", provider, WithIDGen(&FakeIDGen{IDs: []string{"tu_0001", "tu_0002"}}))

	in := make(chan contracts.Input, 2)
	out := make(chan contracts.Event, 32)
	runErr := make(chan error, 1)
	go func() { runErr <- m.Run(context.Background(), in, out) }()

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "one"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn 1 never completed")
	}
	sys1 := provider.LastRequest().AppendSystemPrompt

	in <- contracts.Input{Type: contracts.InUserMessage, Text: "two"}
	if !drainToTurnCompleted(t, out, testTimeout) {
		t.Fatal("turn 2 never completed")
	}
	sys2 := provider.LastRequest().AppendSystemPrompt

	if sys1 != sys2 {
		t.Fatalf("AppendSystemPrompt churned across turns on the same Manager (cache-busting regression):\nturn 1:\n%s\n\nturn 2:\n%s", sys1, sys2)
	}

	in <- contracts.Input{Type: contracts.InEnd}
	expectClosed(t, out, testTimeout)
	if err := <-runErr; err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
}
