package toolrunner

import (
	"context"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// planningToolNames is the set PlanningFamily.Handles checks against —
// contracts.ToolQuestion/contracts.ToolPlan, the two harness-intrinsic
// core tools this family advertises (contracts/tool.go: "present in every
// profile").
var planningToolNames = map[string]bool{
	contracts.ToolQuestion: true,
	contracts.ToolPlan:     true,
}

// PlanningFamily is the harness-intrinsic `question`/`plan` tool family
// (agora-spec-planning-questions.md §1/§4): it exists so
// toolrunner.Surface.Specs advertises these two tools to the model (and so
// Surface.Handles reports them as OWNED, gating them through the turn
// engine's BeforeToolCall hook — see turnengine/approval.go).
//
// Unlike FSFamily/ExecFamily, this family's Execute is never actually
// expected to run: `question`/`plan` have no ordinary "run a command"
// side effect — asking a question needs the park/wait rendezvous and
// `plan` needs the approval-gate/PlanLog wiring, both of which require
// harness state (thread id, event emission, waiter registries) this pure
// package intentionally has no access to. The turn engine's own
// BeforeToolCall hook intercepts every `question`/`plan` call BEFORE
// Surface.Execute is ever reached and resolves it there via bridle's
// Deny+Result short-circuit (the same mechanism ctxmap's own
// recall/inspect/read_raw tools use — see approval.go's beforeToolCall
// doc comment) — so Execute below is defensive dead code, not the real
// implementation.
type PlanningFamily struct{}

// NewPlanningFamily builds the planning tool family.
func NewPlanningFamily() *PlanningFamily { return &PlanningFamily{} }

func (f *PlanningFamily) Name() string { return contracts.FamilyPlanning }

func (f *PlanningFamily) Handles(name string) bool { return planningToolNames[name] }

func (f *PlanningFamily) Specs() []contracts.ToolSpec {
	return []contracts.ToolSpec{
		{
			Name: contracts.ToolQuestion,
			Description: "Ask a question when information is missing — never fabricate an answer. " +
				"blocking:true pauses this turn until answered (interactive threads park until a client " +
				"answers); blocking:false files the question and the turn continues immediately.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"payload": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"text":    map[string]any{"type": "string", "description": "The question itself."},
							"context": map[string]any{"type": "string", "description": "Why this is unclear / what's been tried."},
							"evidence": map[string]any{
								"type":  "array",
								"items": map[string]any{"type": "string"},
							},
							"options": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"label":       map[string]any{"type": "string"},
										"description": map[string]any{"type": "string"},
									},
									"required": []string{"label"},
								},
							},
							"multi_select": map[string]any{"type": "boolean"},
							"free_text":    map[string]any{"type": "boolean"},
						},
						"required": []string{"text"},
					},
					"blocking": map[string]any{
						"type":        "boolean",
						"description": "true = pause and wait for an answer; false = file and continue.",
					},
				},
				"required": []string{"payload", "blocking"},
			}),
		},
		{
			Name: contracts.ToolPlan,
			Description: "Create/update the current plan (design/spec/plan phases). Every call appends a " +
				"new plan revision (never rewritten). submit:true raises the plan approval gate; " +
				"submit:false (the default) just records the update, no gate — the plan object is always " +
				"available.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"phase": map[string]any{"type": "string", "enum": []string{"design", "spec", "plan"}},
					"steps": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"open_questions": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "object"},
						"description": "question-shaped open items; the plan gate refuses allow while any remain.",
					},
					"artifacts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"work_items": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"summary":             map[string]any{"type": "string"},
								"acceptance_criteria": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
								"suggested_executor":  map[string]any{"type": "string"},
							},
							"required": []string{"summary"},
						},
					},
					"submit": map[string]any{"type": "boolean"},
				},
			}),
		},
	}
}

// Execute is defensive dead code — see the type doc comment. A call that
// somehow reaches here (a future refactor of the BeforeToolCall wiring
// that forgets the Deny+Result short-circuit) fails closed with a clear
// error rather than silently no-opping.
func (f *PlanningFamily) Execute(_ context.Context, call Call) (Result, error) {
	return errorResult(fmt.Errorf("toolrunner: %s: executed outside the planning approval gate — this should never happen", call.Name)), nil
}
