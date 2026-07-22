// Package enginerunner implements subagent.AgentRunner by driving one child
// turnengine.Manager turn to completion — the turn-engine seam
// subagent.Manager's own doc comment (runner.go: "agent EXECUTION ... is
// out of this unit's scope") calls out as deliberately belonging elsewhere.
//
// Import-graph placement (why this is its own package, not a method on
// either turnengine.Manager or subagent.Manager): internal/subagent must
// NOT import internal/turnengine (subagent is a lower-level graph/registry/
// spawn-bookkeeping package with no turn-execution concept of its own —
// pulling turnengine in would drag bridle/toolrunner/approval into a
// package whose own tests currently need none of that, and would make
// subagent depend on the very seam its AgentRunner interface exists to
// abstract away). internal/turnengine, symmetrically, must NOT import
// internal/subagent's AgentRunner glue beyond the *subagent.Manager type it
// already needs for the agent() tool (manager.go's WithSubagents) — it has
// no reason to know HOW a child agent actually runs. A new leaf package
// that imports BOTH is the clean seam: nothing else needs to import
// enginerunner (it is wired at the composition root — daemon/cmd — where a
// *subagent.Manager is constructed with this package's Runner as its
// AgentRunner), so it cannot itself become a cycle.
package enginerunner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/subagent"
	"github.com/CarriedWorldUniverse/agora/internal/turnengine"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// Runner implements subagent.AgentRunner over a real turnengine.Manager:
// one attempt (one subagent.RunRequest) = one freshly-built child Manager,
// run for exactly one turn, then discarded. Subagents get no conversation
// history by default (agora-spec-subagents.md §2: "the prompt is the
// contract"), so there is nothing worth keeping a Manager alive for past
// its one turn — send_message continuation (spec §2, re-opening a
// *finished* agent) is OUT of this unit's scope; a future unit adding it
// would need to keep a per-agent Manager alive (or rebuild one with
// WithStore replay) instead of this one-shot construction.
type Runner struct {
	provider bridle.Provider
	store    contracts.ThreadStore

	// managerOpts is applied to every child Manager this Runner builds,
	// BEFORE the per-request options Run adds itself (WithStore/WithModel/
	// WithContextEngine(false) — see Run). It must NEVER include
	// turnengine.WithSubagents: that Option is manager.go's half of the
	// subagent depth guard (agora-spec-subagents.md §2 "Depth cap (default
	// 1)") — a child built with the agent tool wired in could spawn its own
	// children indefinitely. This package exposes no way to set it via
	// WithProfile/WithManagerOption misuse would have to explicitly import
	// turnengine and pass turnengine.WithSubagents(...) to WithManagerOption
	// to defeat this, which is documented here as something callers must
	// not do.
	managerOpts []turnengine.Option
}

// Option configures a Runner at construction.
type Option func(*Runner)

// WithProfile applies cfg (Model/AppendSystemPrompt/Policy/ScopeStore) to
// every child Manager this Runner builds. This is the mechanism for the
// "approval policy... permission profile... inherited" half of
// agora-spec-subagents.md §2 that subagent.RunRequest itself carries no
// field for (RunRequest has Model/Effort/Tools but no Policy/ScopeStore/
// Cwd — see runner.go): every child spawned through ONE Runner instance
// shares the same policy/scope store, which in production wiring is the
// parent turnengine.Manager's own profile — the composition root passes
// WithProfile(parentProfileConfig) when constructing the Runner that backs
// that parent's subagent.Manager.
func WithProfile(cfg turnengine.ProfileConfig) Option {
	return func(r *Runner) { r.managerOpts = append(r.managerOpts, turnengine.WithProfile(cfg)) }
}

// WithManagerOption threads an arbitrary turnengine.Option onto every child
// Manager this Runner builds (e.g. WithRoots, WithMaxSteps, WithClock for
// tests) — an escape hatch for knobs this package doesn't wrap with a
// dedicated Option. See the managerOpts field's doc comment: passing
// turnengine.WithSubagents here defeats the depth guard and must not be
// done.
func WithManagerOption(opt turnengine.Option) Option {
	return func(r *Runner) { r.managerOpts = append(r.managerOpts, opt) }
}

// New builds a Runner. provider is the bridle provider every child Manager
// runs its one turn on — production wiring passes the SAME provider the
// parent turnengine.Manager itself was built with (this unit's brief:
// "build a child turnengine.Manager (child threadID, the PARENT's provider
// + store)"; agora-spec-subagents.md §2's inheritance list). store is the
// contracts.ThreadStore child threads persist to — also the parent's own
// store: subagent.Manager.Spawn already calls store.Create for the child
// thread before this Runner's Run is ever invoked (manager.go's Spawn), so
// Run only needs to Append into an already-created thread.
func New(provider bridle.Provider, store contracts.ThreadStore, opts ...Option) *Runner {
	r := &Runner{provider: provider, store: store}
	for _, o := range opts {
		o(r)
	}
	return r
}

var _ subagent.AgentRunner = (*Runner)(nil)

// turnOutcome is Run's internal classification of how the child turn ended,
// derived from the terminal contracts.Event the child Manager emits.
type turnOutcome int

const (
	outcomeNone turnOutcome = iota
	outcomeCompleted
	outcomeInterrupted
	outcomeFailed
)

// Run implements subagent.AgentRunner: builds one child turnengine.Manager
// for req.AgentID (the child thread subagent.Manager.Spawn already
// Created), drives exactly one turn with req.Prompt as the user_message,
// and returns the child's final agent_message text as RunResult.Output — a
// bare JSON string (toolrunner's decodeAgentOutput documents this as the
// non-schema convention the agent() tool expects).
//
// Depth guard: the child Manager is built WITHOUT turnengine.WithSubagents
// (see manager.go's WithSubagents doc comment and this package's
// managerOpts field doc comment), so it never gets the agent() tool itself
// — agora-spec-subagents.md §2's "Depth cap (default 1 — subagents can't
// spawn subagents unless enabled)" holds structurally here, on top of (not
// instead of) subagent.Manager's own depthCap bookkeeping.
//
// Cancellation (spec §2a): ctx is passed straight through to the child
// Manager's Run — a cancelled ctx (the parent turn's interrupt propagating
// through a foreground/synchronous spawn, which v1's agent() tool always
// is — see toolrunner.AgentFamily) tears the child's in-flight turn down
// the same way any turnengine.Manager interrupt does, and Run reports that
// as an error (ctx.Err()), matching AgentRunner's doc comment: "Run must
// respect ctx cancellation promptly."
//
// Schema-forced output (req.Schema != nil) is NOT implemented — a
// documented cut: Run ignores req.Schema and always returns plain-text
// Output. subagent.Manager's own runWithSchemaRetry (schema.go) would then
// see non-schema-shaped output on every retry attempt and exhaust
// ErrSchemaGiveUp; schema.go's retry path itself is exercised entirely
// against subagent's own fake-runner tests, never against this one, so
// nothing regresses — but a caller that sets SpawnOpts.Schema against a
// subagent.Manager backed by this Runner will always fail, not silently
// get plain text back.
func (r *Runner) Run(ctx context.Context, req subagent.RunRequest) (subagent.RunResult, error) {
	opts := append([]turnengine.Option{}, r.managerOpts...)
	if r.store != nil {
		opts = append(opts, turnengine.WithStore(r.store))
	}
	if req.Model != "" {
		opts = append(opts, turnengine.WithModel(req.Model))
	}
	// The context engine's working-state block (files touched/recent steps)
	// is per-turn scaffolding a ONE-TURN child has no use for — skip
	// building it rather than pay its setup cost for nothing.
	opts = append(opts, turnengine.WithContextEngine(false))

	mgr := turnengine.NewManager(req.AgentID, r.provider, opts...)

	in := make(chan contracts.Input, 1)
	out := make(chan contracts.Event, 256)
	runDone := make(chan error, 1)
	go func() { runDone <- mgr.Run(ctx, in, out) }()

	select {
	case in <- contracts.Input{Type: contracts.InUserMessage, Text: req.Prompt}:
	case <-ctx.Done():
		close(in)
		<-runDone
		return subagent.RunResult{}, ctx.Err()
	}

	var finalText, errMsg string
	outcome := outcomeNone
	endSent := false

drain:
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				break drain
			}
			switch ev.Type {
			case contracts.EvItemCompleted:
				if ev.Item != nil && ev.Item.Type == contracts.ItemAgentMessage {
					var p struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(ev.Payload, &p) == nil {
						finalText = p.Text
					}
				}
			case contracts.EvError:
				var p struct {
					Message string `json:"message"`
				}
				if json.Unmarshal(ev.Payload, &p) == nil && p.Message != "" {
					errMsg = p.Message
				}
			case contracts.EvTurnCompleted:
				outcome = outcomeCompleted
			case contracts.EvTurnFailed:
				var p struct {
					Interrupted bool `json:"interrupted"`
				}
				if json.Unmarshal(ev.Payload, &p) == nil && p.Interrupted {
					outcome = outcomeInterrupted
				} else {
					outcome = outcomeFailed
				}
			}
			// The terminal event (turn.completed/turn.failed) has landed —
			// tell the child Manager's Run loop to stop so it returns
			// rather than sit waiting for a next user_message that will
			// never come (this Runner drives exactly one turn per Run
			// call). Sent exactly once; Run's InEnd case no-ops
			// stopInFlight when no turn is in flight, which is always true
			// by the time this fires (see manager.go's turnDone-case doc
			// comment on terminal-event/bookkeeping-reset atomicity).
			if outcome != outcomeNone && !endSent {
				endSent = true
				select {
				case in <- contracts.Input{Type: contracts.InEnd}:
				case <-ctx.Done():
				}
			}
		case <-ctx.Done():
			break drain
		}
	}
	<-runDone // Run always closes out and returns — manager.go's Run doc comment

	if ctx.Err() != nil {
		return subagent.RunResult{}, ctx.Err()
	}
	switch outcome {
	case outcomeCompleted:
		output, err := json.Marshal(finalText)
		if err != nil {
			return subagent.RunResult{}, fmt.Errorf("subagent enginerunner: agent %s: marshal output: %w", req.AgentID, err)
		}
		return subagent.RunResult{Output: output}, nil
	case outcomeInterrupted:
		return subagent.RunResult{}, fmt.Errorf("subagent enginerunner: agent %s: turn interrupted", req.AgentID)
	case outcomeFailed:
		if errMsg == "" {
			errMsg = "unknown error"
		}
		return subagent.RunResult{}, fmt.Errorf("subagent enginerunner: agent %s: turn failed: %s", req.AgentID, errMsg)
	default:
		// Defensive only: Run's out channel closed without ever emitting a
		// terminal turn event — should not happen (every path through
		// runOneTurn ends in exactly one terminal() call), but never
		// silently report success over it.
		return subagent.RunResult{}, fmt.Errorf("subagent enginerunner: agent %s: child turn ended with no terminal event", req.AgentID)
	}
}
