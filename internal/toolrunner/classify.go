package toolrunner

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// PatchLineKind is the "kind" field of one patch-payload diff line
// (DEVIATIONS.md §5: "kind ∈ add/del/ctx"). Spec ambiguity, resolved: the
// only existing producer/consumer of this wire shape is internal/tui
// (subject.go's patchSubjectPayload decodes "lines" into []DiffLine, whose
// Kind field is tui.DiffLineKind — an int enum with NO custom MarshalJSON,
// so it marshals as a bare integer, not the string "add"/"del"/"ctx" the
// spec prose names). This type's int values are chosen to match that wire
// encoding exactly (DiffContext=0, DiffAdd=1, DiffDel=2) so a payload this
// classifier builds decodes correctly into tui.DiffLine without this
// package importing the tui package (execution logic should not depend on
// a UI package). If tui.DiffLineKind's iota order ever changes, this
// mapping must move with it.
type PatchLineKind int

const (
	PatchLineCtx PatchLineKind = iota
	PatchLineAdd
	PatchLineDel
)

// PatchLine is one line of the "patch" approval payload's "lines" array —
// byte-exact match to internal/tui's DiffLine wire shape (json tags
// "kind"/"oldNo"/"newNo"/"text").
type PatchLine struct {
	Kind  PatchLineKind `json:"kind"`
	OldNo int           `json:"oldNo"`
	NewNo int           `json:"newNo"`
	Text  string        `json:"text"`
}

// ExecPayload is the "exec" (and "gate") approval payload shape
// (DEVIATIONS.md §5).
type ExecPayload struct {
	Command string `json:"command"`
}

// PatchPayload is the "patch" approval payload shape (DEVIATIONS.md §5).
type PatchPayload struct {
	Path  string      `json:"path"`
	Lines []PatchLine `json:"lines"`
}

// EscalationPayload is the "escalation" approval payload shape
// (DEVIATIONS.md §5).
type EscalationPayload struct {
	Detail string `json:"detail"`
}

// ReadPayload is the "read" approval payload shape (NEX-782; DEVIATIONS.md
// §5-style convention — the payload carries the useful identifier for a
// modal that renders it, even though KindRead auto-allows in every preset
// but strict so this modal only ever actually renders under strict).
// read_file/list_dir have a single path; glob/grep have a pattern instead
// (grep optionally scoped to a path too) — Detail carries whichever
// identifier the call actually has, so one shape covers all four tools
// without a path field that's sometimes meaningless.
type ReadPayload struct {
	Detail string `json:"detail"`
}

// MCPToolPayload is the "mcp_tool" approval payload shape (DEVIATIONS.md
// §5).
// Args deliberately has NO omitempty: DEVIATIONS.md §5's shape is exactly
// {tool, args} — a nil Args must still marshal as "args":null (the field
// present, decodable as null) rather than dropping the key entirely.
type MCPToolPayload struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// mcpPrefix mirrors internal/mcp.ToolNamePrefix ("mcp__") without importing
// internal/mcp for one string constant (classify.go is a pure function
// with no dependency on the MCP subsystem's shape).
const mcpPrefix = "mcp__"

// Classify is the pure approval-kind classifier (U-B2): given a Call and
// the session's writable Roots, it decides which contracts.ApprovalKind
// the call falls under and builds that kind's exact wire payload
// (DEVIATIONS.md §5 — the TUI approval modal renders these; a wrong shape
// renders blind). Classify does NOT decide allow/deny (that is
// internal/approval.Decide, wired in a later phase) and does no I/O — it
// only classifies + builds the payload from Call.Args and Roots.
//
// run_command -> exec; write_file/edit_file -> patch (or escalation, if
// classifyWriteTarget rejects the target); mcp__-prefixed -> mcp_tool;
// read_file/list_dir/glob/grep -> read (NEX-782). The read cases do NOT
// check Roots the way the write cases do: read-only fs tools are still
// containment-bounded and protected-dir-excluded, but that enforcement
// runs unconditionally in the fs family itself (fs.go) regardless of the
// approval outcome, so Classify has nothing useful to add here — it just
// carries the path/pattern through for the (strict-only) approval modal.
// Everything else still unrecognized falls to the default case below
// (KindEscalation) — the correct catch-all for genuinely-unknown tools.
//
// Malformed Args classify as KindEscalation with a payload explaining the
// parse failure, rather than panicking or returning a Go error — Classify
// always returns a decidable value (fail-closed, same convention as
// internal/approval.Decide never erroring).
func Classify(call Call, roots Roots) (contracts.ApprovalKind, any) {
	switch {
	case call.Name == ToolAgent:
		// agent() spawns a whole new agentic subtask — closer to
		// run_command's "beyond the sandbox-safe set" than to a read or a
		// file write. No dedicated ApprovalKind exists for it (adding one
		// ripples into contracts.PolicySet/BuiltinPresets and every preset
		// table/TUI modal — a cross-cutting change out of this unit's
		// scope), and re-using KindEscalation would DENY every spawn under
		// the never-escalate preset (headless/pod default) by construction
		// — the wrong default for a harness feature meant to let a model
		// delegate work. KindExec's per-preset defaults (prompt in
		// prompt/strict, auto in auto-safe/never-escalate) are the
		// reasonable middle ground, so agent() classifies as KindExec; its
		// payload reuses ExecPayload's {command} shape (the TUI's existing
		// exec approval modal renders it with no wire/UI change needed).
		var a agentArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed agent arguments: " + err.Error()}
		}
		agentType := a.AgentType
		if agentType == "" {
			agentType = "general-purpose"
		}
		return contracts.KindExec, ExecPayload{Command: "agent(" + agentType + "): " + a.Prompt}

	case taskToolNames[call.Name]:
		// The task list is the model's own bookkeeping: it touches no file,
		// no network, and no process — the state lives and dies inside the
		// family. KindRead is the honest classification (auto-allowed in
		// every preset but strict, where the operator has asked to see even
		// read-only calls), and it means a model can still track its own
		// work under the presets meant for headless runs.
		return contracts.KindRead, ReadPayload{Detail: call.Name}

	case call.Name == ToolWebFetch:
		// Network egress. Classified as KindExec, not KindEscalation, for
		// the same reason agent() is (see above): KindEscalation is DENIED
		// outright under the never-escalate preset, which would make
		// web_fetch permanently unusable headless. KindExec's per-preset
		// defaults (prompt in prompt/strict, auto in auto-safe/
		// never-escalate) are the right middle ground, and ExecPayload's
		// {command} shape renders in the existing TUI approval modal with
		// no wire change.
		//
		// The SSRF guard in web.go is NOT part of this decision — it always
		// applies, regardless of what the operator approves here. Approving
		// a fetch grants reaching a PUBLIC url, never an internal one.
		var a webFetchArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed web_fetch arguments: " + err.Error()}
		}
		return contracts.KindExec, ExecPayload{Command: "web_fetch: " + a.URL}

	case call.Name == ToolRunCommand:
		var a runCommandArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed run_command arguments: " + err.Error()}
		}
		// Sandbox-first exec (operator decree): a command that NAMES a path
		// outside the working-dir subtree classifies as escalation (prompts
		// under every preset), so the sandbox-auto default only auto-runs
		// commands whose named paths stay inside. AddDirs deliberately
		// count as OUTSIDE here — an added folder is reachable, not
		// implicitly trusted; the operator grants it via the prompt
		// (allow-always persists in the scope store).
		if off, outside := commandNamesOutsidePath(a.Command, roots); outside {
			return contracts.KindEscalation, EscalationPayload{
				Detail: "command references " + strconv.Quote(off) + " outside the sandbox: " + a.Command,
			}
		}
		return contracts.KindExec, ExecPayload{Command: a.Command}

	case call.Name == ToolRunBackground:
		var a runBackgroundArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed run_background arguments: " + err.Error()}
		}
		// Starts a real shell command, same risk as run_command — same
		// sandbox-first check, same classification. Only the LIFETIME
		// differs (until bg.kill/session end, not until it exits), which
		// has no bearing on what it's allowed to touch when it starts.
		if off, outside := commandNamesOutsidePath(a.Command, roots); outside {
			return contracts.KindEscalation, EscalationPayload{
				Detail: "command references " + strconv.Quote(off) + " outside the sandbox: " + a.Command,
			}
		}
		return contracts.KindExec, ExecPayload{Command: a.Command}

	case call.Name == ToolBgOutput, call.Name == ToolBgList, call.Name == ToolBgKill:
		// Bookkeeping bounded entirely to jobs THIS session's own
		// run_background already started and had approved — same
		// reasoning as the task family: no new privilege is exercised by
		// reading a job's buffered output or listing known ids, and
		// bg.kill can only ever terminate a job this registry itself
		// tracks (never an arbitrary PID), so it cannot escalate either —
		// worst case is a model stopping its own spawned job early.
		return contracts.KindRead, ReadPayload{Detail: call.Name}

	case strings.HasPrefix(call.Name, mcpPrefix):
		return contracts.KindMCPTool, MCPToolPayload{Tool: call.Name, Args: call.Args}

	case call.Name == ToolReadFile, call.Name == LegacyReadFile:
		var a readFileArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed read_file arguments: " + err.Error()}
		}
		return contracts.KindRead, ReadPayload{Detail: a.filePath()}

	case call.Name == ToolListDir, call.Name == LegacyListDir:
		var a listDirArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed list_dir arguments: " + err.Error()}
		}
		return contracts.KindRead, ReadPayload{Detail: a.filePath()}

	case call.Name == ToolGlob, call.Name == LegacyGlob:
		var a globArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed glob arguments: " + err.Error()}
		}
		return contracts.KindRead, ReadPayload{Detail: a.Pattern}

	case call.Name == ToolGrep, call.Name == LegacyGrep:
		var a grepArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed grep arguments: " + err.Error()}
		}
		return contracts.KindRead, ReadPayload{Detail: a.Pattern}

	case call.Name == contracts.ToolMemoryRead:
		var a memoryReadArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed memory.read arguments: " + err.Error()}
		}
		return contracts.KindRead, ReadPayload{Detail: a.Name}

	case call.Name == contracts.ToolMemoryList:
		return contracts.KindRead, ReadPayload{Detail: "memory.list"}

	case call.Name == contracts.ToolMemoryWrite:
		var a memoryWriteArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed memory.write arguments: " + err.Error()}
		}
		// Mirrors write_file's classification (mutating -> KindPatch), per
		// spec §3's tool-family scope, but does NOT run classifyWriteTarget:
		// the memory dir sits outside the session's fs Roots by design (§3
		// "the family carries its own grant"), so the roots-based
		// containment/protected-path checks write_file needs don't apply
		// here — MemoryFamily/internal/memory.Store enforce the memory dir's
		// own containment (validateSlug) unconditionally instead.
		return contracts.KindPatch, PatchPayload{
			Path:  a.Name + ".md",
			Lines: linesAsAdd(a.Body),
		}

	case call.Name == contracts.ToolMemoryDelete:
		var a memoryDeleteArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed memory.delete arguments: " + err.Error()}
		}
		// Same mutating-kind mirror as memory.write above; Classify does no
		// I/O (package doc), so there is no prior on-disk content available
		// here to render as removed lines — Lines is left empty, the path
		// alone identifies what's being deleted.
		return contracts.KindPatch, PatchPayload{Path: a.Name + ".md"}

	case call.Name == ToolWriteFile, call.Name == LegacyWriteFile:
		var a writeFileArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed write_file arguments: " + err.Error()}
		}
		if kind, payload, ok := classifyWriteTarget(a.filePath(), roots); !ok {
			return kind, payload
		}
		return contracts.KindPatch, PatchPayload{
			Path:  a.filePath(),
			Lines: linesAsAdd(a.Content),
		}

	case call.Name == ToolEditFile, call.Name == LegacyEditFile:
		var a editFileArgs
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return contracts.KindEscalation, EscalationPayload{Detail: "malformed edit_file arguments: " + err.Error()}
		}
		if kind, payload, ok := classifyWriteTarget(a.filePath(), roots); !ok {
			return kind, payload
		}
		return contracts.KindPatch, PatchPayload{
			Path:  a.filePath(),
			Lines: editDiffLines(a.OldString, a.NewString),
		}

	default:
		return contracts.KindEscalation, EscalationPayload{Detail: "unrecognized tool call: " + call.Name}
	}
}

// commandNamesOutsidePath scans a shell command's tokens for filesystem
// paths that leave the WorkingDir subtree: absolute paths, ~-prefixed
// paths, ..-relative escapes, and — unlike the rest of this file's
// classify-time checks — a SYMLINK-AWARE resolution of every candidate
// that looks lexically inside. It is a POLICY-layer heuristic (which
// approval kind the call classifies as), not full containment: the exec
// family still runs with cwd=WorkingDir and this only judges tokens that
// look like paths at all (env-var indirection, subshells still evade it).
//
// The symlink resolution is the one deliberate exception to this file's
// "Classify does no I/O" convention (finding, adversarial review of
// PR #94): for a WRITE, that convention is safe because fs.go's
// execution-time resolveContained is a REAL symlink-aware backstop no
// matter what classify-time decided — an auto-approved write to a symlink
// escaping the sandbox still gets refused when it actually runs. Exec has
// no equivalent backstop (exec.go only sets cmd.Dir=cwd; validating
// arbitrary shell-command path arguments at execution time is not
// generally possible), so classify-time IS the only boundary here — a
// purely lexical check would auto-run `cat innocuous` where `innocuous`
// is a symlink to anything on the host, with no prompt. A resolution
// failure (token doesn't exist, permission denied, ...) stays inside:
// nothing on disk means nothing to leak, and matches the file's existing
// "a false prompt is recoverable, a false auto-run is not" rule — it's
// only ever the RESOLVED existing target landing outside that flips this.
func commandNamesOutsidePath(command string, roots Roots) (offending string, outside bool) {
	wd := roots.WorkingDir
	sep := string(filepath.Separator)
	outsideOf := func(p string) bool { return p != wd && !strings.HasPrefix(p+sep, wd+sep) }

	// isCandidateOutside judges ONE path candidate (already stripped of any
	// VAR=/--flag= prefix): the lexical check, then — for anything that
	// looks inside — the symlink-aware resolution (see the function doc).
	isCandidateOutside := func(tok string) bool {
		if tok == "" || strings.Contains(tok, "://") {
			return false
		}
		var p string
		switch {
		case tok == "~" || strings.HasPrefix(tok, "~/"):
			return true
		case strings.HasPrefix(tok, "/"):
			p = filepath.Clean(tok)
		case tok == ".." || strings.HasPrefix(tok, "../") || strings.Contains(tok, "/../") || strings.HasSuffix(tok, "/.."):
			p = filepath.Clean(filepath.Join(wd, tok))
		default:
			// A bare relative token: lexically inside by construction, but
			// may be a symlink whose target escapes — still a candidate
			// for the resolution check below, not skipped outright.
			p = filepath.Join(wd, tok)
		}
		if outsideOf(p) {
			return true
		}
		resolved, err := filepath.EvalSymlinks(p)
		return err == nil && outsideOf(resolved)
	}

	for _, raw := range strings.Fields(command) {
		tok := strings.Trim(raw, "\"'`(),;&|<>")
		value := tok
		isAssignment := false
		// --flag=/path and VAR=/path forms: judge the value part.
		if eq := strings.IndexByte(tok, '='); eq >= 0 && eq+1 < len(tok) {
			value = strings.Trim(tok[eq+1:], "\"'`")
			isAssignment = true
		}
		if !isAssignment {
			if isCandidateOutside(value) {
				return raw, true
			}
			continue
		}
		// PATH-style assignments (VAR=a:b:c, e.g. `PATH=/usr/local/go/bin:$PATH
		// make build`) are a list, not one path — judging the whole
		// colon-joined blob as a single candidate made this a common false
		// positive (adversarial review of PR #94, finding 4). Judge each
		// ':'-separated segment on its own; an empty segment (leading/
		// trailing/doubled ':') is not a path and is skipped.
		for _, seg := range strings.Split(value, ":") {
			if seg != "" && isCandidateOutside(seg) {
				return raw, true
			}
		}
	}
	return "", false
}

// classifyWriteTarget checks path against roots' containment/protection
// rules for a write/edit target. ok=false means the caller should return
// (kind, payload) as-is (an escalation); ok=true means the write is a
// normal in-root, unprotected patch and the caller builds the KindPatch
// payload itself.
func classifyWriteTarget(path string, roots Roots) (contracts.ApprovalKind, any, bool) {
	if !roots.ContainsLexical(path) {
		return contracts.KindEscalation, EscalationPayload{Detail: "write to \"" + path + "\" is outside the writable roots"}, false
	}
	if roots.IsProtected(path) {
		return contracts.KindEscalation, EscalationPayload{Detail: "write to protected path \"" + path + "\""}, false
	}
	return "", nil, true
}

// linesAsAdd is write_file's payload fallback (brief: "if computing a full
// diff is heavy for write_file, emit the new content as add lines") — there
// is no prior on-disk content in Call.Args to diff against, so every line
// of the new content is rendered as an add. Line numbers are 1-based and
// local to the new content (write_file replaces the whole file, so "newNo"
// here is also the absolute line number in the result; "oldNo" is 0 for
// every line, matching tui.DiffLine's "0 = blank" convention for added
// lines with no old-side counterpart).
func linesAsAdd(content string) []PatchLine {
	rows := strings.Split(content, "\n")
	out := make([]PatchLine, len(rows))
	for i, r := range rows {
		out[i] = PatchLine{Kind: PatchLineAdd, OldNo: 0, NewNo: i + 1, Text: r}
	}
	return out
}

// editDiffLines builds a minimal (not necessarily optimal — a full Myers
// diff is out of scope for a pure, disk-free classifier) line diff between
// old_string and new_string: common leading/trailing lines render as ctx,
// the differing middle renders as del (old) then add (new). Spec
// ambiguity, resolved: Classify has no on-disk file content to compute
// absolute file line numbers against (it only sees Call.Args, by design —
// see the package doc), so oldNo/newNo here are 1-based and LOCAL to the
// old_string/new_string snippets, not absolute file line numbers. A later
// phase with access to the file (the fs family, which already has it) can
// upgrade this to real file-relative numbers without changing the wire
// shape.
func editDiffLines(oldString, newString string) []PatchLine {
	oldLines := strings.Split(oldString, "\n")
	newLines := strings.Split(newString, "\n")

	prefix := commonPrefixLen(oldLines, newLines)
	suffix := commonSuffixLen(oldLines[prefix:], newLines[prefix:])

	var out []PatchLine
	oldNo, newNo := 1, 1

	for i := 0; i < prefix; i++ {
		out = append(out, PatchLine{Kind: PatchLineCtx, OldNo: oldNo, NewNo: newNo, Text: oldLines[i]})
		oldNo++
		newNo++
	}

	oldMidEnd := len(oldLines) - suffix
	newMidEnd := len(newLines) - suffix
	for i := prefix; i < oldMidEnd; i++ {
		out = append(out, PatchLine{Kind: PatchLineDel, OldNo: oldNo, NewNo: 0, Text: oldLines[i]})
		oldNo++
	}
	for i := prefix; i < newMidEnd; i++ {
		out = append(out, PatchLine{Kind: PatchLineAdd, OldNo: 0, NewNo: newNo, Text: newLines[i]})
		newNo++
	}

	for i := oldMidEnd; i < len(oldLines); i++ {
		out = append(out, PatchLine{Kind: PatchLineCtx, OldNo: oldNo, NewNo: newNo, Text: oldLines[i]})
		oldNo++
		newNo++
	}

	return out
}

func commonPrefixLen(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func commonSuffixLen(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}
