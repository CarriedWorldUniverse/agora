package main

import (
	"fmt"
	"os"

	bridle "github.com/CarriedWorldUniverse/bridle"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/skills"
	"github.com/CarriedWorldUniverse/agora/internal/subagent"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	"github.com/CarriedWorldUniverse/agora/internal/workflow"
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
	// Consolidated onto the shared child-runner constructor (spec §8.3): the
	// same never-escalate profile and the same parent-roots threading every
	// other lane gets, from one definition rather than a second copy that
	// happened to agree. A malformed cwd must not fail the run — fall back to
	// the process default, which is what this lane did anyway before #160.
	childRoots, rerr := toolrunner.NewRoots(cwd)
	if rerr != nil {
		fmt.Fprintf(os.Stderr, "agora workflow: build child roots for %q: %v (children fall back to the process cwd)\n", cwd, rerr)
	}
	runner := newSubagentRunner(provider, store, childRoots)
	// Custom agent types must be reachable from a workflow's ctx.agent()
	// too, not just interactive spawns — same discovery as engine.go's
	// seam. Warnings are dropped rather than printed here: a headless run
	// writes machine-readable output on stdout/stderr and engine.go already
	// reports them for the lanes an operator is watching.
	defs, _ := subagent.DiscoverAgentDefs(
		subagent.DefaultAgentRoots(skills.FindProjectRoot(cwd, nil), userHomeOrDot()))
	mgr := subagent.NewManager(store, subagent.NewMemGraphStore(), subagent.NewRegistry(defs), runner)
	mgr.RegisterRoot(runThreadID, subagent.ParentContext{
		Cwd:    cwd,
		Policy: contracts.BuiltinPresets()[contracts.PresetNeverEscalate],
	})
	return &workflow.SubagentInvoker{Manager: mgr, ParentThread: runThreadID}
}
