package toolrunner

import "errors"

// Sentinel errors. Execution-time failures a model can act on (bad args,
// path escape, staleness) are returned as an error Result (IsError=true),
// never as a Go error — a Go error from Family.Execute/Surface.Execute is
// reserved for "the harness itself is broken" (fail-closed, ground rule:
// a tool call from the model never panics the surface). Sentinels below
// are wrapped into those Result.Content messages and are exported so tests
// (and later hooks/audit code) can errors.Is against them.
var (
	// ErrPathEscape: a path resolves (lexically or via symlink) outside
	// every configured writable root. Spec: agora-spec-mcp.md §5a "Bound"
	// ("never follows symlinks out of the sandbox root").
	ErrPathEscape = errors.New("toolrunner: path escapes the writable roots")
	// ErrProtectedPath: a write/edit targets a protected dir (.git/.agora/
	// .cairn) even though it is lexically inside a writable root. Spec:
	// agora-spec-io.md §3a ("Protected even inside wd").
	ErrProtectedPath = errors.New("toolrunner: path is protected (.git/.agora/.cairn)")
	// ErrNotRead: edit_file/write_file(existing file) targets a path the
	// session has not read yet — the staleness guard's "re-read" case.
	// Spec: agora-spec-mcp.md §5a ("edit-tool staleness guard").
	ErrNotRead = errors.New("toolrunner: path has not been read this session, read it first")
	// ErrStale: the on-disk content changed since the session's last read
	// of this path — the staleness guard's "moved under you" case.
	ErrStale = errors.New("toolrunner: path changed on disk since it was last read, re-read it first")
	// ErrOldStringNotFound: edit_file's old_string does not occur in the
	// current file content.
	ErrOldStringNotFound = errors.New("toolrunner: old_string not found in file")
	// ErrOldStringNotUnique: edit_file's old_string occurs more than once
	// and replace_all was not set.
	ErrOldStringNotUnique = errors.New("toolrunner: old_string is not unique in file, pass replace_all or include more context")
	// ErrUnknownTool: Surface.Execute/Classify got a Call name no family
	// and no MCPSource claims.
	ErrUnknownTool = errors.New("toolrunner: unknown tool")
	// ErrBadArgs: a tool's Args did not unmarshal into its expected shape.
	ErrBadArgs = errors.New("toolrunner: malformed arguments")
)
