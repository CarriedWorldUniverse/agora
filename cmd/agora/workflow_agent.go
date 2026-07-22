package main

import (
	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/subagent"
	"github.com/CarriedWorldUniverse/agora/internal/subagent/enginerunner"
	"github.com/CarriedWorldUniverse/agora/internal/turnengine"
	"github.com/CarriedWorldUniverse/agora/internal/workflow"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// buildWorkflowInvoker composes the production ctx.agent() path: a real
// *subagent.Manager (U10) over turnengineRunner, exactly mirroring
// internal/workflow/subagent_adapter.go's documented wiring
// ("SubagentInvoker maps AgentInvoker onto a real *subagent.Manager"). store
// is shared with the run's own thread store so child-agent transcripts and
// the run's own thread live side by side, matching how internal/daemon
// wires one store across a whole Daemon. runThreadID is RegisterRoot'd with
// PresetNeverEscalate — contracts.approval.go's own doc comment: "headless/
// pod default" — since a CLI workflow run has no interactive approver
// attached to answer PresetPrompt's escalations.
func buildWorkflowInvoker(store contracts.ThreadStore, provider bridle.Provider, runThreadID, cwd string) *workflow.SubagentInvoker {
	// enginerunner is THE production AgentRunner (the same one engine.go
	// wires for interactive agent() spawns); a headless workflow run just
	// gives it a never-escalate profile — no interactive approver is
	// attached, so PresetPrompt would park a child's tool call forever.
	prof := turnengine.DevProfile()
	prof.Policy = contracts.BuiltinPresets()[contracts.PresetNeverEscalate]
	runner := enginerunner.New(provider, store, enginerunner.WithProfile(prof))
	mgr := subagent.NewManager(store, subagent.NewMemGraphStore(), subagent.NewRegistry(nil), runner)
	mgr.RegisterRoot(runThreadID, subagent.ParentContext{
		Cwd:    cwd,
		Policy: contracts.BuiltinPresets()[contracts.PresetNeverEscalate],
	})
	return &workflow.SubagentInvoker{Manager: mgr, ParentThread: runThreadID}
}
