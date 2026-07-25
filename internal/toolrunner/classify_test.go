package toolrunner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestClassifyExec(t *testing.T) {
	roots := newTestRoots(t)
	// In-sandbox command (no named path leaves the working dir) -> exec.
	kind, payload := Classify(Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "rm -rf tmp/x"})}, roots)
	if kind != contracts.KindExec {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindExec)
	}
	assertJSONExact(t, payload, `{"command":"rm -rf tmp/x"}`)
}

// TestClassifyExec_SandboxEscapes: sandbox-first exec (operator decree) —
// a command NAMING a path outside the working-dir subtree classifies as
// escalation (prompts), while sandbox-relative commands and in-sandbox
// absolute paths stay exec (auto under the default policy).
func TestClassifyExec_SandboxEscapes(t *testing.T) {
	roots := newTestRoots(t)
	outside := []string{
		"rm -rf /tmp/x",
		"cat /etc/passwd",
		"cp bin/agora ~/.local/bin/agora",
		"ls ~",
		"cat ../sibling/secret",
		"tar -C .. -xf a.tar",
		"go build -o /usr/local/bin/x ./cmd",
	}
	for _, cmd := range outside {
		kind, _ := Classify(Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: cmd})}, roots)
		if kind != contracts.KindEscalation {
			t.Errorf("Classify(%q) = %v, want escalation (names an outside path)", cmd, kind)
		}
	}
	inside := []string{
		"go test ./...",
		"make build",
		"git commit -m msg",
		"cat " + roots.WorkingDir + "/notes.txt",
		"curl https://example.com/api",
		"grep -rn TODO internal/",
		// PATH-style VAR=a:b:c is a LIST, not one path (adversarial review
		// of PR #94, finding 4) — every segment here is sandbox-relative
		// or a bare name, so this must stay exec, not false-positive as
		// escalation on the colon-joined blob.
		"PATH=bin:vendor/bin:$PATH make build",
	}
	for _, cmd := range inside {
		kind, _ := Classify(Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: cmd})}, roots)
		if kind != contracts.KindExec {
			t.Errorf("Classify(%q) = %v, want exec (sandbox-contained)", cmd, kind)
		}
	}
}

// TestClassifyExec_PathStyleAssignment_StillCatchesAnEscapingSegment:
// finding 4's fix must not weaken finding-4-adjacent coverage — a REAL
// escape hidden in one segment of a colon-joined PATH-style assignment
// still classifies as escalation.
func TestClassifyExec_PathStyleAssignment_StillCatchesAnEscapingSegment(t *testing.T) {
	roots := newTestRoots(t)
	kind, _ := Classify(Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "PATH=/tmp/evil:/usr/bin make build"})}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("Classify(PATH with an escaping segment) = %v, want escalation", kind)
	}
}

// TestClassifyExec_SymlinkEscape: adversarial review of PR #94, finding 1
// — a relative-looking token that is actually a symlink resolving OUTSIDE
// the sandbox must classify as escalation, not auto-run as exec. The
// companion case proves a symlink that stays INSIDE (or points at a
// nonexistent target) is unaffected — this must not become a blanket
// "no symlinks ever" false-positive generator.
func TestClassifyExec_SymlinkEscape(t *testing.T) {
	roots := newTestRoots(t)
	wd := roots.WorkingDir

	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	escapingLink := filepath.Join(wd, "innocuous")
	if err := os.Symlink(secret, escapingLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	kind, _ := Classify(Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "cat innocuous"})}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("Classify(cat innocuous) with an escaping symlink = %v, want escalation", kind)
	}

	// A symlink that resolves INSIDE the sandbox is a normal in-sandbox
	// reference — must stay exec.
	insideTarget := filepath.Join(wd, "real.txt")
	if err := os.WriteFile(insideTarget, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	sameDirLink := filepath.Join(wd, "alias")
	if err := os.Symlink(insideTarget, sameDirLink); err != nil {
		t.Fatal(err)
	}
	kind, _ = Classify(Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "cat alias"})}, roots)
	if kind != contracts.KindExec {
		t.Fatalf("Classify(cat alias) with an in-sandbox symlink = %v, want exec", kind)
	}

	// A token that doesn't exist on disk (nothing to leak) stays exec —
	// resolution failure is not treated as an escape.
	kind, _ = Classify(Call{Name: ToolRunCommand, Args: mustArgs(t, runCommandArgs{Command: "cat does-not-exist.txt"})}, roots)
	if kind != contracts.KindExec {
		t.Fatalf("Classify(cat does-not-exist.txt) = %v, want exec (nonexistent target is not a leak)", kind)
	}
}

func TestClassifyMCPTool(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: "mcp__github__search", Args: json.RawMessage(`{"q":"foo"}`)}, roots)
	if kind != contracts.KindMCPTool {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindMCPTool)
	}
	assertJSONExact(t, payload, `{"tool":"mcp__github__search","args":{"q":"foo"}}`)
}

// TestClassifyMCPToolNilArgs: DEVIATIONS.md §5's mcp_tool shape is exactly
// {tool, args} — a nil/omitted args must still marshal the key as
// "args":null, not drop it, since the TUI's decode side (and any other
// consumer) expects the field to always be present.
func TestClassifyMCPToolNilArgs(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: "mcp__github__search"}, roots)
	if kind != contracts.KindMCPTool {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindMCPTool)
	}
	assertJSONExact(t, payload, `{"tool":"mcp__github__search","args":null}`)
}

func TestClassifyWriteFileInsideRoots(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: "a.txt", Content: "hello\nworld"})}, roots)
	if kind != contracts.KindPatch {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindPatch)
	}
	assertJSONExact(t, payload, `{"path":"a.txt","lines":[{"kind":1,"oldNo":0,"newNo":1,"text":"hello"},{"kind":1,"oldNo":0,"newNo":2,"text":"world"}]}`)
}

func TestClassifyWriteFileOutsideRoots(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: "../escape.txt", Content: "x"})}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
	p, ok := payload.(EscalationPayload)
	if !ok || p.Detail == "" {
		t.Fatalf("payload = %#v, want non-empty EscalationPayload", payload)
	}
}

func TestClassifyWriteFileProtectedPath(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolWriteFile, Args: mustArgs(t, writeFileArgs{Path: ".git/config", Content: "x"})}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
	p, ok := payload.(EscalationPayload)
	if !ok || p.Detail == "" {
		t.Fatalf("payload = %#v, want non-empty EscalationPayload", payload)
	}
}

func TestClassifyEditFileDiff(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolEditFile, Args: mustArgs(t, editFileArgs{
		Path:      "f.go",
		OldString: "func Foo() {\n\treturn 1\n}",
		NewString: "func Foo() {\n\treturn 2\n}",
	})}, roots)
	if kind != contracts.KindPatch {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindPatch)
	}
	assertJSONExact(t, payload, `{"path":"f.go","lines":[`+
		`{"kind":0,"oldNo":1,"newNo":1,"text":"func Foo() {"},`+
		`{"kind":2,"oldNo":2,"newNo":0,"text":"\treturn 1"},`+
		`{"kind":1,"oldNo":0,"newNo":2,"text":"\treturn 2"},`+
		`{"kind":0,"oldNo":3,"newNo":3,"text":"}"}]}`)
}

func TestClassifyEditFileOutsideRoots(t *testing.T) {
	roots := newTestRoots(t)
	kind, _ := Classify(Call{Name: ToolEditFile, Args: mustArgs(t, editFileArgs{Path: absOutsideRoots(), OldString: "a", NewString: "b"})}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
}

func TestClassifyMalformedArgsIsEscalation(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolRunCommand, Args: json.RawMessage(`not json`)}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
	p, ok := payload.(EscalationPayload)
	if !ok || p.Detail == "" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestClassifyUnknownToolIsEscalation(t *testing.T) {
	roots := newTestRoots(t)
	kind, _ := Classify(Call{Name: "nonexistent_tool"}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
}

// TestClassifyReadTools: read_file/list_dir/glob/grep all classify as
// KindRead (NEX-782), carrying the call's path/pattern in ReadPayload.
func TestClassifyReadTools(t *testing.T) {
	roots := newTestRoots(t)

	kind, payload := Classify(Call{Name: ToolReadFile, Args: mustArgs(t, readFileArgs{Path: "a.txt"})}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("read_file kind = %v, want %v", kind, contracts.KindRead)
	}
	if p, ok := payload.(ReadPayload); !ok || p.Detail != "a.txt" {
		t.Fatalf("read_file payload = %#v, want ReadPayload{Detail: \"a.txt\"}", payload)
	}

	kind, payload = Classify(Call{Name: ToolListDir, Args: mustArgs(t, listDirArgs{Path: "sub"})}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("list_dir kind = %v, want %v", kind, contracts.KindRead)
	}
	if p, ok := payload.(ReadPayload); !ok || p.Detail != "sub" {
		t.Fatalf("list_dir payload = %#v, want ReadPayload{Detail: \"sub\"}", payload)
	}

	kind, payload = Classify(Call{Name: ToolGlob, Args: mustArgs(t, globArgs{Pattern: "**/*.go"})}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("glob kind = %v, want %v", kind, contracts.KindRead)
	}
	if p, ok := payload.(ReadPayload); !ok || p.Detail != "**/*.go" {
		t.Fatalf("glob payload = %#v, want ReadPayload{Detail: \"**/*.go\"}", payload)
	}

	kind, payload = Classify(Call{Name: ToolGrep, Args: mustArgs(t, grepArgs{Pattern: "TODO"})}, roots)
	if kind != contracts.KindRead {
		t.Fatalf("grep kind = %v, want %v", kind, contracts.KindRead)
	}
	if p, ok := payload.(ReadPayload); !ok || p.Detail != "TODO" {
		t.Fatalf("grep payload = %#v, want ReadPayload{Detail: \"TODO\"}", payload)
	}
}

// TestClassifyReadFileMalformedArgsIsEscalation: read tools follow the
// same malformed-Args-> KindEscalation fail-closed convention as every
// other case.
func TestClassifyReadFileMalformedArgsIsEscalation(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolReadFile, Args: json.RawMessage(`not json`)}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("kind = %v, want %v", kind, contracts.KindEscalation)
	}
	p, ok := payload.(EscalationPayload)
	if !ok || p.Detail == "" {
		t.Fatalf("payload = %#v", payload)
	}
}

// assertJSONExact marshals payload and compares it byte-for-byte against
// want (both parsed and re-marshaled through the same encoder so key
// ordering from struct field order is deterministic) — the DEVIATIONS.md
// §5 shapes are load-bearing for the TUI modal, so this checks the exact
// wire bytes a producer would emit, not just semantic equivalence.
func assertJSONExact(t *testing.T, payload any, want string) {
	t.Helper()
	got, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if string(got) != want {
		t.Fatalf("payload JSON =\n%s\nwant\n%s", got, want)
	}
}

// run_background shares run_command's sandbox-first classification
// exactly — same escape check, same auto-vs-prompt split. Mirrors
// TestClassifyExec/TestClassifyExec_SandboxEscapes rather than assuming
// the shared code path makes a separate test redundant.
func TestClassifyRunBackground_SandboxEscapes(t *testing.T) {
	roots := newTestRoots(t)

	kind, payload := Classify(Call{Name: ToolRunBackground, Args: mustArgs(t, runBackgroundArgs{Command: "npm run dev"})}, roots)
	if kind != contracts.KindExec {
		t.Fatalf("in-sandbox run_background: kind = %v, want %v", kind, contracts.KindExec)
	}
	assertJSONExact(t, payload, `{"command":"npm run dev"}`)

	outside := []string{"cat /etc/passwd", "npm run dev --prefix ~/other-project"}
	for _, cmd := range outside {
		kind, _ := Classify(Call{Name: ToolRunBackground, Args: mustArgs(t, runBackgroundArgs{Command: cmd})}, roots)
		if kind != contracts.KindEscalation {
			t.Errorf("Classify(run_background, %q) = %v, want escalation", cmd, kind)
		}
	}
}

func TestClassifyRunBackground_MalformedArgs(t *testing.T) {
	roots := newTestRoots(t)
	kind, payload := Classify(Call{Name: ToolRunBackground, Args: json.RawMessage(`not json`)}, roots)
	if kind != contracts.KindEscalation {
		t.Fatalf("malformed run_background args: kind = %v, want escalation (fail closed)", kind)
	}
	if _, ok := payload.(EscalationPayload); !ok {
		t.Fatalf("payload = %T, want EscalationPayload", payload)
	}
}

func TestClassify_BackgroundControlToolsAreReadKind(t *testing.T) {
	roots := newTestRoots(t)
	for _, name := range []string{ToolBgOutput, ToolBgList, ToolBgKill} {
		kind, payload := Classify(Call{Name: name, Args: json.RawMessage(`{}`)}, roots)
		if kind != contracts.KindRead {
			t.Errorf("Classify(%s) kind = %q; want %q", name, kind, contracts.KindRead)
		}
		if _, ok := payload.(ReadPayload); !ok {
			t.Errorf("Classify(%s) payload is %T; want ReadPayload", name, payload)
		}
	}
}
