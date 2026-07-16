package contracts

// QuestionSource is who raised the question. HARNESS-STAMPED, never supplied
// by the model or a client — the model cannot forge its own provenance.
// Spec: agora-spec-planning-questions.md §4 (elicitation subsumed as mcp_server).
type QuestionSource string

const (
	QuestionFromAgent     QuestionSource = "agent"
	QuestionFromMCPServer QuestionSource = "mcp_server"
	QuestionFromWorkflow  QuestionSource = "workflow"
)

// QuestionOption is one enumerable choice on a structured question card.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionArgs is the CONTENT of a question — exactly and only what the model's
// harness-intrinsic `question` tool supplies. It carries NO attribution:
// Source is stamped by the harness (QuestionAsked), never here. Keeping the
// forgeable field out of the model-facing type makes the never-fabricate
// boundary a compile-time property, not a runtime hope.
// Spec: agora-spec-planning-questions.md §4.
type QuestionArgs struct {
	Text    string `json:"text"`
	Context string `json:"context,omitempty"`
	// Evidence carries refs (paths, PR urls, item seqs) — evidence-first cards.
	Evidence    []string         `json:"evidence,omitempty"`
	Options     []QuestionOption `json:"options,omitempty"`
	MultiSelect bool             `json:"multi_select,omitempty"`
	FreeText    bool             `json:"free_text,omitempty"`
}

// QuestionAsked is the wire envelope for the question.asked event AND the
// payload of a KindQuestion ApprovalRequest: {id, source, blocking, payload}.
// ID correlates the card to its answer and to a plan's open-questions; Source
// is harness-stamped; Blocking is the value the `question` tool call carried
// (blocking:true parks/converts, blocking:false queues). Args is the model's
// content.
// Spec: agora-spec-io.md §1, agora-spec-planning-questions.md §4/§7.
type QuestionAsked struct {
	ID       string         `json:"id"`
	Source   QuestionSource `json:"source"`
	Blocking bool           `json:"blocking"`
	Args     QuestionArgs   `json:"payload"`
}

// AnswerInput is the client/tool-supplied answer — NO attribution. This is
// what rides in Input (question_response) and what a QuestionAsked hook returns.
// A client physically cannot claim a `by` because the field does not exist here.
// Spec: agora-spec-planning-questions.md §4, §6 invariant 1/4.
type AnswerInput struct {
	// Choice holds selected option labels (≥1 when options were offered;
	// >1 only when MultiSelect).
	Choice []string `json:"choice,omitempty"`
	// Text is the free-text component.
	Text string `json:"text,omitempty"`
}

// Answer is the ATTRIBUTED answer record carried in question.answered events
// and the persistence audit line. By is stamped by the daemon from the
// answering connection's authenticated identity — NEVER copied from client
// input. Constructed server-side as Answer{AnswerInput, By: connIdentity}.
// "Your call" delegation is a valid Answer (an actor really answered), not a
// bypass of never-fabricate.
// Spec: agora-spec-remote.md §5 ("by = device identity"),
// agora-spec-planning-questions.md §6 invariant 4.
type Answer struct {
	AnswerInput
	// By is the answering identity/device fingerprint — the audit line.
	By string `json:"by"`
}

// BlockedNeedsInput is the typed result a one-shot dispatch job terminates
// with instead of parking: the job dies honestly, the lease releases, and the
// dispatcher re-dispatches with the answer folded into the brief.
// Spec: agora-spec-planning-questions.md §5 (ladder, dispatch row), §8.
type BlockedNeedsInput struct {
	Question QuestionAsked `json:"question"`
	// ThreadID lets a re-dispatch resume the same thread where useful.
	ThreadID string `json:"thread_id,omitempty"`
}
