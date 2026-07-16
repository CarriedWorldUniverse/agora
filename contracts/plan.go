package contracts

// PlanPhase names which planning stage the artifact currently represents.
// Planning covers the design → spec → plan stages of the work.
// Spec: agora-spec-planning-questions.md §1.
type PlanPhase string

const (
	PhaseDesign PlanPhase = "design"
	PhaseSpec   PlanPhase = "spec"
	PhasePlan   PlanPhase = "plan"
)

// WorkItem is one decomposed unit of an approved plan, carrying OBSERVABLE
// acceptance criteria — planning is where DoD gets authored; downstream
// acceptance gates are only as good as criteria written here.
// Spec: agora-spec-planning-questions.md §1.
type WorkItem struct {
	Summary            string   `json:"summary"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	// SuggestedExecutor is a routing hint (agent_type or model alias).
	SuggestedExecutor string `json:"suggested_executor,omitempty"`
}

// PlanArtifact is the plan object: a first-class thread item, updatable via
// the harness-intrinsic `plan` tool; every update appends a new revision
// (never rewritten). All fields optional — small work uses Steps only.
// Spec: agora-spec-planning-questions.md §1.
type PlanArtifact struct {
	Phase PlanPhase `json:"phase,omitempty"`
	Steps []string  `json:"steps,omitempty"`
	// OpenQuestions BLOCK the plan gate: allow is refused while any remain
	// unresolved (§3 / invariant 6). Each carries an ID so a specific
	// question is correlated to a specific answer — "some question got
	// answered" must NOT satisfy the gate; the gate tracks the ID SET.
	OpenQuestions []QuestionAsked `json:"open_questions,omitempty"`
	// Artifacts are spec/design doc refs produced by the planning phases.
	Artifacts []string   `json:"artifacts,omitempty"`
	WorkItems []WorkItem `json:"work_items,omitempty"`
	// Submit=true on a `plan` tool call signals readiness and raises the
	// KindPlan approval — the model proposes exit; the operator disposes.
	// Spec: agora-spec-planning-questions.md §3 (exit authority).
	Submit bool `json:"submit,omitempty"`
}

// PlanExit names what an approved plan gate triggers.
// Spec: agora-spec-planning-questions.md §2 (one posture, two exits).
type PlanExit string

const (
	// ExitInline: posture drops entirely; the same session executes.
	ExitInline PlanExit = "inline"
	// ExitDelegate: orchestrate mode — the approved decomposition feeds
	// agent()/workflows/dispatch; post-gate execution is governed by the
	// subagents-§5 redirect discipline, not the planning overlay.
	ExitDelegate PlanExit = "delegate"
)
