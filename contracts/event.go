package contracts

import "encoding/json"

// EventType tags every output event on the io seam (pipe mode JSONL and the
// session protocol carry the same types).
// Spec: agora-spec-io.md §1 (output events), §0a (multi-attach additions).
type EventType string

const (
	EvThreadStarted EventType = "thread.started"
	EvTurnStarted   EventType = "turn.started"
	EvTurnCompleted EventType = "turn.completed"
	EvTurnFailed    EventType = "turn.failed"

	EvItemStarted   EventType = "item.started"
	EvItemUpdated   EventType = "item.updated"
	EvItemCompleted EventType = "item.completed"
	// EvAgentMessageDelta streams text; droppable by design (backpressure).
	EvAgentMessageDelta EventType = "item.agent_message.delta"

	// EvToolLoaded: a tool_search load brought new schemas into scope.
	// Spec: agora-spec-mcp.md §5.
	EvToolLoaded EventType = "tool.loaded"

	// Context events. Compaction pair = summarization episodes; curation pair
	// = view-only working-set LRU (no thread mutation).
	// Spec: agora-spec-context.md §2 contract #4, agora-spec-context-curation.md §7.
	EvCompactionStarted   EventType = "thread.compaction.started"
	EvCompactionCompleted EventType = "thread.compaction.completed"
	EvCurationDemoted     EventType = "thread.curation.demoted"
	EvCurationReadmitted  EventType = "thread.curation.readmitted"

	EvApprovalRequested EventType = "approval.requested"
	// EvApprovalResolved carries {by}; fans out after first-answer-wins.
	// Spec: agora-spec-io.md §0a.
	EvApprovalResolved EventType = "approval.resolved"

	// Question events. Non-blocking questions queue instead of blocking.
	// Spec: agora-spec-planning-questions.md §7.
	EvQuestionAsked    EventType = "question.asked"
	EvQuestionAnswered EventType = "question.answered"
	// Parked-thread lifecycle: a blocking question with no answer parks the
	// thread durably; the inbox reads the daemon's parked-question queue.
	EvThreadWaiting EventType = "thread.waiting"
	EvThreadResumed EventType = "thread.resumed"

	// Presence (session protocol only, not single-client pipe mode).
	EvClientAttached EventType = "client.attached"
	EvClientDetached EventType = "client.detached"

	EvError EventType = "error"
)

// ItemType enumerates transcript item kinds carried by item.* events.
// Spec: agora-spec-io.md §1; plan added per agora-spec-planning-questions.md §1.
type ItemType string

const (
	ItemAgentMessage     ItemType = "agent_message"
	ItemReasoning        ItemType = "reasoning"
	ItemCommandExecution ItemType = "command_execution"
	ItemFileChange       ItemType = "file_change"
	ItemMCPToolCall      ItemType = "mcp_tool_call"
	ItemPlan             ItemType = "plan"
	ItemAgentSpawn       ItemType = "agent_spawn"
	ItemWorkflowProgress ItemType = "workflow_progress"
)

// Event is the envelope every output event rides in. ThreadID/TurnID are set
// where scoped (chain components can multiplex sessions on them).
// Spec: agora-spec-io.md §1, §4 (chain composition rules).
type Event struct {
	Type     EventType `json:"type"`
	ThreadID string    `json:"thread_id,omitempty"`
	TurnID   string    `json:"turn_id,omitempty"`
	// Item is set on item.* events.
	Item *ItemRef `json:"item,omitempty"`
	// Payload carries the type-specific body (usage on turn.completed,
	// ApprovalRequest on approval.requested, QuestionPayload+id on
	// question.asked, ...). Decoded by consumers per Type.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ItemRef identifies an item within a thread on the wire.
type ItemRef struct {
	Seq  int64    `json:"seq"`
	Type ItemType `json:"type"`
}

// InputType tags input messages (stdin JSONL in pipe mode; the same shapes on
// the session protocol).
// Spec: agora-spec-io.md §1 (input messages).
type InputType string

const (
	InUserMessage      InputType = "user_message"
	InSteer            InputType = "steer"
	InInterrupt        InputType = "interrupt"
	InApprovalResponse InputType = "approval_response"
	// InQuestionResponse answers a question.asked with a structured Answer.
	// Spec: agora-spec-planning-questions.md §7.
	InQuestionResponse InputType = "question_response"
	// InConfig requires the admin capability, not plain interactive.
	InConfig InputType = "config"
	InEnd    InputType = "end"
)

// Input is the inbound envelope.
// Spec: agora-spec-io.md §1.
type Input struct {
	Type InputType `json:"type"`
	// Text for user_message/steer.
	Text string `json:"text,omitempty"`
	// Model/Effort: optional one-shot override on user_message (= %-override).
	Model  string `json:"model,omitempty"`
	Effort Effort `json:"effort,omitempty"`
	// ID correlates approval_response/question_response to the request.
	ID       string   `json:"id,omitempty"`
	Decision Decision `json:"decision,omitempty"`
	Scope    Scope    `json:"scope,omitempty"`
	Message  string   `json:"message,omitempty"`
	Answer   *Answer  `json:"answer,omitempty"`
	// Key/Value for config messages.
	Key   string          `json:"key,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Usage is the per-request token accounting, required on turn.completed.
// Spec: agora-spec-bridle.md §2 (usage event).
type Usage struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Cached    int64 `json:"cached,omitempty"`
	Reasoning int64 `json:"reasoning,omitempty"`
}
