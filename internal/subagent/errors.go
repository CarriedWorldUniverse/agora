package subagent

import "errors"

// Sentinel errors, checkable via errors.Is regardless of which code path
// produced them (house style, mirrors internal/persistence, internal/approval).
var (
	// ErrAgentDefEmptyDescription: an agent def's description is required —
	// it is what the calling model reads to decide whether to delegate
	// (spec §1: "the description is written for the calling model").
	ErrAgentDefEmptyDescription = errors.New("subagent: agent def description is required and must be non-empty")
	// ErrAgentDefEmptyName: an agent def must resolve to a non-empty name
	// (frontmatter name, or the fallback filename/dirname).
	ErrAgentDefEmptyName = errors.New("subagent: agent def name is required and must be non-empty")

	// ErrEdgeExists: AddEdge called for a (parent,child) pair already in the graph.
	ErrEdgeExists = errors.New("subagent: graph edge already exists")
	// ErrNonTerminalOutcome: RecordOutcome called with NodeRunning (or any
	// non-terminal status). Persisting "running" would be a lie after a
	// crash — see Edge.Outcome (agora#158).
	ErrNonTerminalOutcome = errors.New("subagent: outcome status is not terminal")
	// ErrEdgeNotFound: CloseEdge/Edge lookup on a pair not in the graph.
	ErrEdgeNotFound = errors.New("subagent: graph edge not found")

	// ErrNodeNotFound: an operation referenced an agent_id the Manager has
	// no record of.
	ErrNodeNotFound = errors.New("subagent: agent not found")
	// ErrDepthCapExceeded: a spawn would exceed the configured subagent
	// depth cap (spec §2: "Depth cap (default 1 — subagents can't spawn
	// subagents unless enabled)").
	ErrDepthCapExceeded = errors.New("subagent: depth cap exceeded")
	// ErrNotFinished: send_message/Continue was called on an agent that is
	// still running — spec §2: continuation "re-opens a *finished* agent";
	// no steering of running agents in v1.
	ErrNotFinished = errors.New("subagent: agent has not finished, cannot continue")

	// ErrSchemaGiveUp: a schema-forced agent() call exhausted its retry cap
	// without producing schema-valid output (spec §2: "validated, retried
	// on mismatch").
	ErrSchemaGiveUp = errors.New("subagent: gave up on schema-forced output after max retries")

	// ErrSpawnCapExceeded: a spawn would exceed the configured per-session
	// spawn cap (spec §2: cap total agent() calls per session, distinct from
	// the depth cap and the concurrency cap).
	ErrSpawnCapExceeded = errors.New("subagent: spawn cap exceeded")
)
