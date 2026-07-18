package contracts

// ApprovalKind is what is being asked. One canonical set; every other
// vocabulary in the harness is a declared alias of this.
// Spec: agora-spec-approvals.md §1.
type ApprovalKind string

const (
	// KindExec: command execution beyond the sandbox-safe set.
	KindExec ApprovalKind = "exec"
	// KindPatch: file changes (protected paths + explicit patch-review modes;
	// writes inside wd are the sandbox's job).
	KindPatch ApprovalKind = "patch"
	// KindEscalation: anything outside the sandbox envelope (write outside wd,
	// protected paths, network beyond policy, wasm grant escape).
	KindEscalation ApprovalKind = "escalation"
	// KindMCPTool: MCP tool call per server/tool approval mode.
	KindMCPTool ApprovalKind = "mcp_tool"
	// KindQuestion: missing information. Resolves with a structured Answer,
	// not allow/deny; NEVER timeout-denied (parks/converts instead).
	// Spec: agora-spec-approvals.md §1, agora-spec-planning-questions.md §4–§6.
	KindQuestion ApprovalKind = "question"
	// KindPlan: the plan gate closing a planning posture. Payload = the plan
	// artifact + unresolved open questions; allow is refused while any remain.
	// Spec: agora-spec-planning-questions.md §3.
	KindPlan ApprovalKind = "plan"
	// KindGate: workflow approval gate (ctx.approval, workflow-engine v1).
	KindGate ApprovalKind = "gate"
	// KindRead: read-only fs tool calls (read_file/list_dir/glob/grep).
	// These are containment-bounded and protected-dir-excluded by the fs
	// family itself (fs.go: resolveContained + IsProtected checks run on
	// every call regardless of approval outcome) — safe to auto-allow in
	// dev/chat/headless without asking on every read, while a strict
	// profile can still gate them. Auto-allowing KindRead only skips the
	// APPROVAL prompt; it does NOT bypass containment or protected-path
	// enforcement, which live in the fs family and run unconditionally.
	// NEX-782.
	KindRead ApprovalKind = "read"
)

// Decision is the resolution of a permission-shaped approval.
// KindQuestion resolves with an Answer instead (question.go); a deny on a
// question means "declined to answer", with Message as the reason.
// Spec: agora-spec-approvals.md §1.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

// Scope is how long an allow persists.
// Spec: agora-spec-approvals.md §1.
type Scope string

const (
	ScopeOnce    Scope = "once"    // default
	ScopeSession Scope = "session" // per-thread
	ScopePrefix  Scope = "prefix"  // exec only: command-prefix allow
	ScopeHost    Scope = "host"    // network only: host-pattern allow
)

// PolicyValue is what gets decided before a human is asked.
// Spec: agora-spec-approvals.md §2.
type PolicyValue string

const (
	PolicyAuto   PolicyValue = "auto"   // allow if within envelope, no ask
	PolicyPrompt PolicyValue = "prompt" // ask
	PolicyDeny   PolicyValue = "deny"   // refuse flat
	// PolicyConvert is valid ONLY for KindQuestion: convert immediately per
	// the escalation ladder (park for threads, blocked:needs-input for pods)
	// — never auto-answered, never silently denied.
	// Spec: agora-spec-approvals.md §2 footnote †.
	PolicyConvert PolicyValue = "convert"
	// PolicyPerServer is valid ONLY for KindMCPTool: defer to the MCP server's
	// own approval_mode (agora-spec-mcp.md §1). The "per-server" cells of the
	// §2 preset table.
	PolicyPerServer PolicyValue = "per-server"
)

// PolicySet maps kind → policy value. A named preset is a PolicySet plus the
// sandbox envelope it presumes.
// Spec: agora-spec-approvals.md §2.
type PolicySet map[ApprovalKind]PolicyValue

// Built-in preset names. Definable presets extend these via config
// ([approval_presets.<name>]).
// Spec: agora-spec-approvals.md §2.
const (
	PresetPrompt        = "prompt"    // default dev
	PresetAutoSafe      = "auto-safe" // chat
	PresetStrict        = "strict"
	PresetNeverEscalate = "never-escalate" // headless/pod default
)

// BuiltinPresets are the four shipped policy sets — the columns of the §2
// preset table, exactly: exec | patch | escalation | mcp_tool | question | plan.
// KindGate is deliberately ABSENT: the table has no gate column because
// workflow gates always surface to the operator (ctx.approval always asks,
// workflows §2), so gate is not preset-governed. patch "auto" here means
// writes-inside-wd are the sandbox's job; protected paths still raise
// escalation (§2 note *). KindRead (NEX-782) is auto-allowed in every
// column EXCEPT strict — read-only fs tools are containment-bounded and
// protected-dir-excluded by the fs family itself, so nothing outside that
// column needs to ask on every read; strict still prompts, since it gates
// everything.
// Spec: agora-spec-approvals.md §2 (presets table).
func BuiltinPresets() map[string]PolicySet {
	return map[string]PolicySet{
		PresetPrompt: {
			KindExec: PolicyPrompt, KindPatch: PolicyAuto, KindEscalation: PolicyPrompt,
			KindMCPTool: PolicyPerServer, KindQuestion: PolicyPrompt, KindPlan: PolicyPrompt,
			KindRead: PolicyAuto,
		},
		PresetAutoSafe: {
			KindExec: PolicyAuto, KindPatch: PolicyAuto, KindEscalation: PolicyPrompt,
			KindMCPTool: PolicyPerServer, KindQuestion: PolicyPrompt, KindPlan: PolicyPrompt,
			KindRead: PolicyAuto,
		},
		PresetStrict: {
			KindExec: PolicyPrompt, KindPatch: PolicyPrompt, KindEscalation: PolicyPrompt,
			KindMCPTool: PolicyPrompt, KindQuestion: PolicyPrompt, KindPlan: PolicyPrompt,
			KindRead: PolicyPrompt,
		},
		PresetNeverEscalate: {
			KindExec: PolicyAuto, KindPatch: PolicyAuto, KindEscalation: PolicyDeny,
			KindMCPTool: PolicyPerServer, KindQuestion: PolicyConvert, KindPlan: PolicyDeny,
			KindRead: PolicyAuto,
		},
	}
}

// Stage identifies where in the canonical pipeline a decision was made:
// hooks → policy → approvers → queue → timeout.
// Spec: agora-spec-remote.md §8, agora-spec-approvals.md §2.
type Stage string

const (
	StageHook     Stage = "hook"
	StagePolicy   Stage = "policy"
	StageApprover Stage = "approver"
	StageQueue    Stage = "queue"
	StageTimeout  Stage = "timeout"
)

// ApprovalRequest is the harness-side record of an approval situation, fanned
// out to approver-capable clients as an approval.requested event.
// Spec: agora-spec-approvals.md §1, agora-spec-io.md §1.
type ApprovalRequest struct {
	ID   string       `json:"id"`
	Kind ApprovalKind `json:"kind"`
	// Payload is kind-specific: the command for exec, the diff footprint for
	// patch, a QuestionAsked for question, a PlanArtifact for plan, etc.
	Payload any `json:"payload"`
}

// ApprovalResolution records the decision with full attribution — the audit
// line. Every decision records stage + actor.
// Spec: agora-spec-approvals.md §4 (invariant 3).
type ApprovalResolution struct {
	ID       string   `json:"id"`
	Decision Decision `json:"decision"`
	Scope    Scope    `json:"scope,omitempty"`
	// Message: on deny, feedback to the model (invariant: deny is feedback).
	Message string `json:"message,omitempty"`
	// By is the deciding actor: hook name, preset name, or device/identity
	// fingerprint. Stage says which pipeline stage decided.
	By    string `json:"by"`
	Stage Stage  `json:"stage"`
}

// Invariants (compile-visible statement of agora-spec-approvals.md §4):
//  1. Policy deny is final for that call; hooks may only restrict further,
//     except an explicit PermissionRequest-hook allow (logged as operator bypass).
//  2. Timeout fallback is always deny for permission kinds and plan —
//     KindQuestion is the exception: it parks/converts, never fabricates.
//  3. Every decision is recorded with stage + actor.
//  4. Subagents inherit the parent's effective policy set; workflows may only
//     restrict, never widen.
//  5. Capability "approver" gates permission/plan/gate answering;
//     "interactive" answers questions; "admin" changes presets at runtime.
