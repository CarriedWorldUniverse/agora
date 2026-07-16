package contracts

// QuestionSource is who raised the question.
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

// QuestionPayload is the self-contained card: answerable cold, hours later,
// without the surrounding chat. It is the payload of an ApprovalRequest with
// KindQuestion, and the argument of the harness-intrinsic `question` tool.
// Spec: agora-spec-planning-questions.md §4.
type QuestionPayload struct {
	Text    string `json:"text"`
	Context string `json:"context,omitempty"`
	// Evidence carries refs (paths, PR urls, item seqs) — evidence-first cards.
	Evidence    []string         `json:"evidence,omitempty"`
	Options     []QuestionOption `json:"options,omitempty"`
	MultiSelect bool             `json:"multi_select,omitempty"`
	FreeText    bool             `json:"free_text,omitempty"`
	// Blocking: true = the caller cannot proceed (parks/converts per the
	// escalation ladder); false = file-and-continue (inbox batching).
	Blocking bool           `json:"blocking"`
	Source   QuestionSource `json:"source"`
}

// Answer resolves a question. Never fabricated: an Answer exists only when an
// actor (attributed in By) actually provided one. "Your call" delegation is a
// valid Answer, not a bypass.
// Spec: agora-spec-planning-questions.md §4, §6 (invariants).
type Answer struct {
	// Choice holds selected option labels (≥1 when options were offered;
	// >1 only when MultiSelect).
	Choice []string `json:"choice,omitempty"`
	// Text is the free-text component.
	Text string `json:"text,omitempty"`
	// By is the answering identity/device fingerprint — the audit line.
	By string `json:"by"`
}

// BlockedNeedsInput is the typed result a one-shot dispatch job terminates
// with instead of parking: the job dies honestly, the lease releases, and the
// dispatcher re-dispatches with the answer folded into the brief.
// Spec: agora-spec-planning-questions.md §5 (ladder, dispatch row), §8.
type BlockedNeedsInput struct {
	Question QuestionPayload `json:"question"`
	// ThreadID lets a re-dispatch resume the same thread where useful.
	ThreadID string `json:"thread_id,omitempty"`
}
