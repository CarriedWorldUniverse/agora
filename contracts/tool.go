package contracts

import (
	"encoding/json"
	"time"
)

// ToolSpec is the one tool format agora emits; bridle translates to each
// provider's function-calling shape and back without remangling names.
// Spec: agora-spec-bridle.md §3, agora-spec-mcp.md §2 (naming).
type ToolSpec struct {
	// Name is agora's model-visible name: native families keep bare names;
	// MCP tools are `mcp__<server>__<tool>` (max 64; hash-suffixed on
	// collision).
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolState: core tools have full schemas injected at start; deferred tools
// are name+description only until a tool_search load (session-sticky,
// survives compaction as tool state, not transcript).
// Spec: agora-spec-mcp.md §5.
type ToolState string

const (
	ToolCore     ToolState = "core"
	ToolDeferred ToolState = "deferred"
)

// Native tool family names — pluggable ToolRunner families a profile enables.
// Spec: agora-spec.md §Profiles, agora-spec-mcp.md §5a.
const (
	FamilyFS       = "fs"
	FamilyExec     = "exec"
	FamilyWeb      = "web"
	FamilyBrowser  = "browser"
	FamilyComputer = "computer"
	// FamilyAgent exposes the agent() subagent-spawn tool.
	// Spec: agora-spec-subagents.md §2.
	FamilyAgent = "agent"
)

// Harness-intrinsic core tools: engine-registered, always core, present in
// every profile. `question` is ladder-resolved per context, not profile-gated.
// Spec: agora-spec-mcp.md §5a, agora-spec-planning-questions.md §1/§4.
const (
	ToolPlan       = "plan"
	ToolQuestion   = "question"
	ToolToolSearch = "tool_search"
	// memory.* family per agora-spec-memory.md §3.
	ToolMemoryRead   = "memory.read"
	ToolMemoryWrite  = "memory.write"
	ToolMemoryList   = "memory.list"
	ToolMemoryDelete = "memory.delete"
)

// FSChange is the fs-watcher signal: path-keyed, content-hash-identified
// (identical bytes = no-op, not an invalidation), coalesced per path.
// Consumers: the edit-tool staleness guard and the curation staleness gate.
// Spec: agora-spec-mcp.md §5a ("emits {path, kind, at} keyed by path").
type FSChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // modified | created | deleted
	// At is the quiescence time of the coalesced change — the staleness
	// consumers need recency/ordering, per the spec's emitted tuple.
	At time.Time `json:"at"`
	// ContentHash of the on-disk bytes after the change ("" for deleted).
	ContentHash string `json:"content_hash,omitempty"`
}
