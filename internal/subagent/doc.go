// Package subagent — child funnel sessions, the agent() tool, the agent
// graph, cancellation propagation, and continuation.
//
// Build unit: U10 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-subagents.md.
//
// Scope (ground rule 6, subagents spec §2/§2a/§3): agent definitions
// (markdown+frontmatter, §1), the agent() spawn tool's SEMANTICS (spawn,
// background/foreground, schema-forced structured output with retry,
// cancellation propagation, continuation), and the parent/child agent
// graph store (§3). The actual agent EXECUTION — model calls, the tool
// loop — is the turn-engine's job; it is stubbed here behind the
// AgentRunner interface. The workflow engine (U14) consumes this package;
// it is not built here.
//
// Files:
//   - agentdef.go: AgentDef (frontmatter markdown) parsing + builtins.
//   - graph.go / graph_file.go: GraphStore (parent/child edges), in-memory
//     and JSONL-persisted implementations, BFS traversal.
//   - runner.go: AgentRunner seam + request/result shapes.
//   - schema.go: minimal schema validation + the forced-retry loop.
//   - inherit.go: spec §2 inheritance resolution (model/effort/tools/policy).
//   - manager.go: Manager — spawn, continuation, node registry, notifications.
//   - cancel.go: spec §2a cancellation matrix.
package subagent
