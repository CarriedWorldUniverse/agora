package contracts

import "time"

// ThreadMeta is line 1 of a thread's JSONL file — never rewritten (wd updates
// append wd_changed items; latest wins at resume).
// Spec: agora-spec-persistence.md §1.
type ThreadMeta struct {
	ThreadID     string    `json:"thread_id"`
	CreatedAt    time.Time `json:"created_at"`
	IdentityFP   string    `json:"identity_fp"`
	IdentityName string    `json:"identity_name,omitempty"`
	Profile      string    `json:"profile"`
	WorkingDir   string    `json:"working_dir"`
	ProjectRoot  string    `json:"project_root,omitempty"`
	// ParentThread links subagent children (ordinary threads).
	ParentThread string `json:"parent_thread,omitempty"`
	// ForkOf: fork-by-reference — reads chain through the parent to Seq.
	ForkOf *ForkRef `json:"fork_of,omitempty"`
	Title  string   `json:"title,omitempty"`
}

// ForkRef identifies the fork point in a parent thread.
type ForkRef struct {
	ThreadID string `json:"thread_id"`
	Seq      int64  `json:"seq"`
}

// ThreadItemType enumerates persisted item kinds. Superset of the wire
// ItemType: persistence also records approvals, questions/answers, park
// markers, plan revisions, hook outcomes, compaction markers, wd changes,
// and provisioning events.
// Spec: agora-spec-persistence.md §1.
type ThreadItemType string

const (
	TIUserMessage      ThreadItemType = "user_message"
	TIAgentMessage     ThreadItemType = "agent_message"
	TIReasoning        ThreadItemType = "reasoning"
	TIToolCall         ThreadItemType = "tool_call"
	TIToolResult       ThreadItemType = "tool_result"
	TIApprovalRequest  ThreadItemType = "approval_request"
	TIApprovalDecision ThreadItemType = "approval_decision"
	TIQuestionAsked    ThreadItemType = "question_asked"
	TIQuestionAnswered ThreadItemType = "question_answered"
	TIParked           ThreadItemType = "parked"
	TIResumed          ThreadItemType = "resumed"
	TIPlanRevision     ThreadItemType = "plan_revision"
	TIHookOutcome      ThreadItemType = "hook_outcome"
	TICompactionMarker ThreadItemType = "compaction_marker"
	TIWDChanged        ThreadItemType = "wd_changed"
	TIProvisioning     ThreadItemType = "provisioning"
	// TITurnUsage records a turn's closing usage payload (input/output/
	// cached/cache_write/reasoning tokens + cost — the Usage shape below)
	// as the turn's final Append-batch item, so ccusage-style session/cost
	// history is reconstructable from the JSONL alone.
	// Spec: agora-spec-persistence.md §1 ("Per-turn usage persists").
	TITurnUsage ThreadItemType = "turn_usage"
)

// ThreadItem is one append-only line. Never rewritten: compaction adds a
// marker; curation is an assembly-time view (context contract #1).
// Spec: agora-spec-persistence.md §1.
type ThreadItem struct {
	Seq  int64          `json:"seq"`
	TS   time.Time      `json:"ts"`
	Type ThreadItemType `json:"type"`
	// Identity/device attribution: who acted (instance identity) and, for
	// remote-originated input, which device (remote §5 audit trail).
	Identity string `json:"identity,omitempty"`
	Device   string `json:"device,omitempty"`
	Payload  any    `json:"payload,omitempty"`
}

// ListFilter selects threads for /resume and queries.
// Spec: agora-spec-persistence.md §2 (indexed fields), agora-spec-io.md §3a.
type ListFilter struct {
	WorkingDir  string
	ProjectRoot string
	IdentityFP  string
	Archived    *bool
	// Text searches items_fts when the mirror has it.
	Text string
}

// ThreadStore is the storage-neutral persistence seam. LocalStore
// (JSONL+SQLite) and MemStore (tests, ephemeral pods) implement it; a future
// casket/broker-synced store is an implementation, not a redesign.
// Spec: agora-spec-persistence.md §3.
type ThreadStore interface {
	Create(meta ThreadMeta) error
	// Resume returns a replay iterator over items (tail-first fast-open is an
	// implementation concern; the contract is full replay order by Seq).
	Resume(threadID string) (ItemIterator, error)
	Append(threadID string, items []ThreadItem) error
	Meta(threadID string) (ThreadMeta, error)
	List(f ListFilter) ([]ThreadMeta, error)
	// Fork creates a new thread referencing (threadID, seq) — no copying.
	Fork(threadID string, seq int64) (ThreadMeta, error)
	Archive(threadID string) error
	Delete(threadID string) error
}

// ItemIterator streams a thread's items in Seq order.
type ItemIterator interface {
	Next() (ThreadItem, bool)
	Err() error
	Close() error
}
