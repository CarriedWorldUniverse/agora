package ctxmgr

// ToolClass is a keyed artifact's role, per the §6 [context.keys] mapping.
// Spec: agora-spec-context-curation.md §2, §6.
type ToolClass string

const (
	// ClassRead: a full-content read of the key. The TOOL RESULT carries
	// the newest truth.
	ClassRead ToolClass = "read"
	// ClassWrite: a full-content write. The TOOL CALL's args carry the
	// newest truth (the model authored it) — §2 "a write's ARGS are the
	// newest truth of the file".
	ClassWrite ToolClass = "write"
	// ClassEdit: a mutation without full content (patch/edit tools). Never
	// carries truth; invalidates the live copy (§2 staleness rule).
	ClassEdit ToolClass = "edit"
)

// Key identifies one artifact: (domain, id), e.g. ("file", "src/a.py").
// Spec §2's "(tool_class, key), e.g. (file, "src/a.py")" — read here as
// the artifact DOMAIN read/write/edit tools for the same thing share
// (retention identity is the artifact, not the operation): §2 "one live
// copy per key in the whole assembly, wherever it lives" requires a
// read_file and a write_file on the same path to collide onto ONE ledger
// entry, which they can only do if Class (read/write/edit — a per-event
// operation kind, see ToolClass) is NOT part of the identity. Unkeyed
// tools (commands) never produce a Key — they are tier-4 recent-window
// items, never ledger entries.
type Key struct {
	Domain string
	ID     string
}

// String is the deterministic sort/map key ("domain:id").
func (k Key) String() string {
	return k.Domain + ":" + k.ID
}

// KeyMapping is one [context.keys] table row: which tool names map to
// which artifact domain, operation class, and key arg.
type KeyMapping struct {
	// Domain groups tools that share one artifact identity (e.g. "file"
	// for read_file/write_file/apply_patch — all three key the same path
	// into the same ledger entry).
	Domain string
	Class  ToolClass
	KeyArg string
}
