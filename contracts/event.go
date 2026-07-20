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

	// EvProvisioned: a dispatch-controlled pod's provision message applied
	// atomically — payload {identity_fp, profile}. Spec: agora-spec-remote.md
	// §6a ("Provisioning is atomic: apply-all-or-reject, then `provisioned
	// {identity_fp, profile}` event").
	EvProvisioned EventType = "provisioned"

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
	// ApprovalRequest on approval.requested, QuestionAsked on question.asked,
	// ...). Decoded by consumers per Type.
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
	// InQuestionResponse answers a question.asked with an AnswerInput
	// (no attribution — the daemon stamps By). Spec: planning-questions §7.
	InQuestionResponse InputType = "question_response"
	// InConfig requires the admin capability, not plain interactive.
	InConfig InputType = "config"
	// InProvision makes a blank pod specific (admin capability).
	// Spec: agora-spec-remote.md §6a.
	InProvision InputType = "provision"
	InEnd       InputType = "end"
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
	// Provider selects the bridle provider for THIS turn (from a /model
	// registry entry). Nil = the engine's default provider (the Anthropic
	// subscription/claudesdk). A non-nil spec (e.g. the OpenAI-compatible
	// provider at a LiteLLM base_url) routes the turn to that provider natively
	// — this is what lets agora talk to non-Anthropic models through bridle
	// rather than forcing everything through the Anthropic API.
	Provider *ProviderSpec `json:"provider,omitempty"`
	// ID correlates approval_response/question_response to the request.
	ID       string   `json:"id,omitempty"`
	Decision Decision `json:"decision,omitempty"`
	Scope    Scope    `json:"scope,omitempty"`
	Message  string   `json:"message,omitempty"`
	// Answer is the client-supplied answer for question_response — an
	// AnswerInput with NO `by`: the daemon stamps attribution from the
	// authenticated connection (a client cannot forge who answered).
	Answer *AnswerInput `json:"answer,omitempty"`
	// Provision carries the pod-specialization message (InProvision, admin).
	Provision *Provision `json:"provision,omitempty"`
	// Key/Value for config messages.
	Key   string          `json:"key,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// ProviderSpec picks + configures a bridle provider for a single turn. Name is
// the provider kind ("openai" for an OpenAI-compatible endpoint); BaseURL is
// that endpoint (e.g. a LiteLLM gateway's /v1); APIKey is the already-resolved
// credential. Carried on Input.Provider so the turn engine can route a turn to
// a non-default provider without any Anthropic-shaped workaround.
type ProviderSpec struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
}

// Usage is the per-request token accounting, required on turn.completed.
// Spec: agora-spec-bridle.md §2 (usage event).
type Usage struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Cached    int64 `json:"cached,omitempty"`
	Reasoning int64 `json:"reasoning,omitempty"`
	// Cost is the provider-reported charge for the turn in USD (OpenRouter's
	// exact upstream cost via the openai provider). 0 = not reported — the
	// client may estimate from a configured price table instead (the
	// subscription claudesdk path reports no cost; ccusage-style notional
	// pricing comes from models.json).
	Cost float64 `json:"cost,omitempty"`
}
