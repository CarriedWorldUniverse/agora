package tui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	tea "github.com/charmbracelet/bubbletea"
)

func TestSlashDispatch_MatchesKnownVerbsOnly(t *testing.T) {
	known := []struct{ text, wantName, wantArgs string }{
		{"/mcp", "mcp", ""},
		{"/MCP", "mcp", ""},
		{"  /help  ", "help", ""},
		{"/mcp extra args", "mcp", "extra args"},
	}
	for _, tc := range known {
		c, args, ok := slashDispatch(tc.text)
		if !ok || c.name != tc.wantName || args != tc.wantArgs {
			t.Errorf("slashDispatch(%q) = (%q, %q, %v); want (%q, %q, true)", tc.text, c.name, args, ok, tc.wantName, tc.wantArgs)
		}
	}
	// Everything else starting with "/" must NOT intercept: unknown verbs,
	// near-misses, absolute paths, a bare slash — all stay ordinary
	// messages for the model (the documented near-miss behavior).
	pass := []string{"/quitter", "/mcps", "/usr/bin/gcc is broken", "/", "hello", ""}
	for _, s := range pass {
		if _, _, ok := slashDispatch(s); ok {
			t.Errorf("slashDispatch(%q) intercepted; want fall-through to user message", s)
		}
	}
}

// TestSubmitComposer_SlashMCP_PrintsReport: "/mcp" renders the server
// report to the transcript via the Printer and sends NOTHING to the
// backend — it's a local client command, not a model message.
func TestSubmitComposer_SlashMCP_PrintsReport(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.cfg.ListServers = func() ([]ServerInfo, error) {
		return []ServerInfo{{Name: "github", Transport: "stdio", Detail: "npx server-github", Enabled: true}}, nil
	}
	m.composer.InsertText("/mcp")
	m.press(tea.KeyMsg{Type: tea.KeyEnter})
	if len(printed) != 1 || !strings.Contains(printed[0], "github") {
		t.Fatalf("printed = %v; want one block containing the server", printed)
	}
	if len(backend.Sent) != 0 {
		t.Fatalf("backend got %d inputs; /mcp must not reach the model", len(backend.Sent))
	}
}

// TestSubmitComposer_UnknownSlash_Contained: the §6a (NEX-795) contract —
// an unrecognized /word NEVER reaches the model. It prints a local error
// (with a nearest-match suggestion when one is close enough) instead.
func TestSubmitComposer_UnknownSlash_Contained(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.composer.InsertText("/modek")
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	runCmd(cmd)
	if len(backend.Sent) != 0 {
		t.Fatalf("backend got %d inputs; unknown /modek must never reach the model: %+v", len(backend.Sent), backend.Sent)
	}
	if len(printed) != 1 || !strings.Contains(printed[0], "unknown command: /modek") {
		t.Fatalf("printed = %v; want a local unknown-command error", printed)
	}
	if !strings.Contains(printed[0], "did you mean /model?") {
		t.Fatalf("printed = %q; want a nearest-match suggestion for /model", printed[0])
	}
}

// TestSubmitComposer_UnknownSlash_NoCloseMatch: a typo too far from any
// known verb gets the plain error, no confident-but-wrong guess.
func TestSubmitComposer_UnknownSlash_NoCloseMatch(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.composer.InsertText("/qqqqqqqqqq")
	runCmd(m.submitComposer())
	if len(backend.Sent) != 0 {
		t.Fatalf("backend got %d inputs; want none", len(backend.Sent))
	}
	if len(printed) != 1 || !strings.Contains(printed[0], "unknown command: /qqqqqqqqqq") || strings.Contains(printed[0], "did you mean") {
		t.Fatalf("printed = %v; want a bare unknown-command error, no suggestion", printed)
	}
}

// TestSubmitComposer_EscapeHatch_BackslashSlash: "\/literal" sends to the
// model verbatim with the backslash stripped (§6a's documented escape
// hatch for a message that must literally start with "/").
func TestSubmitComposer_EscapeHatch_BackslashSlash(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	m.composer.InsertText(`\/literal command`)
	runCmd(m.submitComposer())
	if len(backend.Sent) != 1 || backend.Sent[0].Text != "/literal command" {
		t.Fatalf("Sent = %+v; want one user message with text \"/literal command\"", backend.Sent)
	}
}

// TestSubmitComposer_EscapeHatch_LeadingSpace: a leading space also opts a
// literal "/"-starting message out of containment, unmodified.
func TestSubmitComposer_EscapeHatch_LeadingSpace(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	m.composer.InsertText(" /literal command")
	runCmd(m.submitComposer())
	if len(backend.Sent) != 1 || backend.Sent[0].Text != " /literal command" {
		t.Fatalf("Sent = %+v; want one user message with text \" /literal command\"", backend.Sent)
	}
}

// TestSubmitComposer_RegistryNameSugar: "/<registry-name>" (e.g. "/kimi")
// dispatches exactly like "/model kimi" — the shortcut form the operator
// types instinctively (§6a) — and does not reach the model.
func TestSubmitComposer_RegistryNameSugar(t *testing.T) {
	backend := newFakeBackend()
	reg := ModelRegistry{"kimi": {Model: "kimi-k3", BaseURL: "http://x:4000/v1"}}
	m := testModelWithRegistry(backend, reg)
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.composer.InsertText("/kimi")
	runCmd(m.submitComposer())
	if len(backend.Sent) != 0 {
		t.Fatalf("backend got %d inputs; /kimi sugar must not reach the model", len(backend.Sent))
	}
	if m.currentModel != "kimi-k3" {
		t.Fatalf("currentModel = %q, want kimi-k3 (as /model kimi would set)", m.currentModel)
	}
	if len(printed) != 1 || !strings.Contains(printed[0], "model set to kimi") {
		t.Fatalf("printed = %v; want the same confirmation /model kimi prints", printed)
	}
}

// TestSubmitComposer_SlashHelp: "/help" prints the command list including
// the exit verbs (which live outside the dispatch table).
func TestSubmitComposer_SlashHelp(t *testing.T) {
	m := testModel(newFakeBackend())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.composer.InsertText("/help")
	m.press(tea.KeyMsg{Type: tea.KeyEnter})
	if len(printed) != 1 {
		t.Fatalf("printed = %v; want one block", printed)
	}
	for _, want := range []string{"/help", "/mcp", "/quit"} {
		if !strings.Contains(printed[0], want) {
			t.Errorf("help block missing %q:\n%s", want, printed[0])
		}
	}
}

func TestRenderMCPReport_States(t *testing.T) {
	th := PlainTheme()

	// Nil loader (e.g. a connection where cmd didn't wire the seam).
	out := renderMCPReport(nil, th)
	if len(out) != 2 || !strings.Contains(out[1], "not available") {
		t.Errorf("nil loader: got %v", out)
	}

	// Loader error (malformed config must not masquerade as "none").
	out = renderMCPReport(func() ([]ServerInfo, error) { return nil, errors.New("bad json") }, th)
	if len(out) != 2 || !strings.Contains(out[1], "bad json") {
		t.Errorf("loader error: got %v", out)
	}

	// Empty config.
	out = renderMCPReport(func() ([]ServerInfo, error) { return nil, nil }, th)
	if len(out) != 2 || !strings.Contains(out[1], "none configured") {
		t.Errorf("empty: got %v", out)
	}
}

// TestRenderMCPReport_Golden pins the populated report's rendered shape
// (PlainTheme: byte-stable across terminals).
func TestRenderMCPReport_Golden(t *testing.T) {
	out := renderMCPReport(func() ([]ServerInfo, error) {
		return []ServerInfo{
			{Name: "github", Transport: "stdio", Detail: "npx -y @modelcontextprotocol/server-github", Enabled: true},
			{Name: "old-tool", Transport: "stdio", Detail: "uvx old-tool", Enabled: false},
			{Name: "remote", Transport: "streamable_http", Detail: "https://example.com/mcp", Enabled: true},
		}, nil
	}, PlainTheme())
	assertGolden(t, "slash_mcp", out)
}

// TestSlashStatus_ContainsCoreFields: model + thread id + usage are present
// after at least one completed turn.
func TestSlashStatus_ContainsCoreFields(t *testing.T) {
	backend := newFakeBackend()
	m := NewModel(Config{Backend: backend, AgentID: "anvil-builder", Theme: PlainTheme(), ThreadID: "agora-abc123",
		Now: func() time.Time { return time.Unix(0, 0).UTC() }, ModelRegistry: testRegistry()})
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.handleEvent(contracts.Event{Type: contracts.EvTurnStarted, TurnID: "t1"})
	usagePayload, err := json.Marshal(map[string]any{"usage": map[string]any{"input": 100, "output": 20}})
	if err != nil {
		t.Fatal(err)
	}
	m.handleEvent(contracts.Event{Type: contracts.EvTurnCompleted, TurnID: "t1", Payload: usagePayload})

	m.composer.SetValue("/status")
	runCmd(m.submitComposer())
	if len(backend.Sent) != 0 {
		t.Fatalf("/status sent %v to the model, want nothing", backend.Sent)
	}
	if len(printed) != 1 {
		t.Fatalf("printed = %v; want one block", printed)
	}
	out := printed[0]
	for _, want := range []string{"anvil-builder", "claude-sonnet-5", "agora-abc123", "in-process"} {
		if !strings.Contains(out, want) {
			t.Errorf("/status output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no completed turns yet") {
		t.Errorf("/status shows no-usage placeholder after a completed turn:\n%s", out)
	}
}

// TestSlashCopy_OSC52AndEmpty covers both branches: a finalized agent
// message emits a correctly-encoded OSC 52 sequence, and an empty
// transcript reports "nothing to copy" rather than an escape sequence.
func TestSlashCopy_OSC52AndEmpty(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/copy")
	runCmd(m.submitComposer())
	if len(printed) != 1 || printed[0] != "nothing to copy" {
		t.Fatalf("printed = %v; want [\"nothing to copy\"] before any reply", printed)
	}

	printed = nil
	m.handleEvent(contracts.Event{Type: contracts.EvTurnStarted, TurnID: "t1"})
	m.handleEvent(contracts.Event{Type: contracts.EvAgentMessageDelta, Payload: deltaPayload(t, "hello there\n")})
	m.handleEvent(contracts.Event{Type: contracts.EvTurnCompleted, TurnID: "t1"})
	printed = nil // drop the streamed/finalized transcript prints, isolate /copy's own output
	m.composer.SetValue("/copy")
	runCmd(m.submitComposer())
	if len(printed) != 2 {
		t.Fatalf("printed = %v; want [osc52, \"copied N chars\"]", printed)
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte("hello there"))
	wantOSC := "\x1b]52;c;" + wantB64 + "\x07"
	if printed[0] != wantOSC {
		t.Fatalf("osc52 = %q, want %q", printed[0], wantOSC)
	}
	if printed[1] != "copied 11 chars" {
		t.Fatalf("count line = %q, want \"copied 11 chars\"", printed[1])
	}
}

// forkBackend is a fakeBackend that also implements ThreadForker.
type forkBackend struct {
	*fakeBackend
	gotThread string
	gotSeq    int64
	newID     string
	err       error
}

func (f *forkBackend) ForkThread(threadID string, seq int64) (string, error) {
	f.gotThread, f.gotSeq = threadID, seq
	if f.err != nil {
		return "", f.err
	}
	return f.newID, nil
}

// TestSlashFork_ForksAtLatestSeq: /fork calls the seam with the thread id
// and the highest item Seq this attachment has observed, and prints the
// relaunch hint.
func TestSlashFork_ForksAtLatestSeq(t *testing.T) {
	backend := &forkBackend{fakeBackend: newFakeBackend(), newID: "th_abc123"}
	m := NewModel(Config{Backend: backend, Theme: PlainTheme(), ThreadID: "agora-parent",
		Now: func() time.Time { return time.Unix(0, 0).UTC() }, ModelRegistry: testRegistry()})
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.handleEvent(contracts.Event{Type: contracts.EvItemCompleted, Item: &contracts.ItemRef{Seq: 7, Type: contracts.ItemAgentMessage}})

	m.composer.SetValue("/fork")
	runCmd(m.submitComposer())
	if backend.gotThread != "agora-parent" || backend.gotSeq != 7 {
		t.Fatalf("ForkThread called with (%q, %d); want (agora-parent, 7)", backend.gotThread, backend.gotSeq)
	}
	if len(printed) != 1 || !strings.Contains(printed[0], "th_abc123") || !strings.Contains(printed[0], "agora -thread th_abc123") {
		t.Fatalf("printed = %v; want the forked id + relaunch hint", printed)
	}
	if len(backend.Sent) != 0 {
		t.Fatalf("/fork sent %v to the model, want nothing", backend.Sent)
	}
}

// TestSlashFork_UnsupportedBackend: a backend without the ThreadForker seam
// degrades to a plain message, same pattern as /resume.
func TestSlashFork_UnsupportedBackend(t *testing.T) {
	m := testModelWithRegistry(newFakeBackend(), testRegistry()) // plain fake: no ThreadForker
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.composer.SetValue("/fork")
	runCmd(m.submitComposer())
	if len(printed) != 1 || !strings.Contains(printed[0], "not supported") {
		t.Fatalf("printed = %v; want a not-supported message", printed)
	}
}

// TestSlashNew_PrintsRelaunchCommand: v1 doesn't swap threads in place — it
// prints the relaunch command with a freshly generated id.
func TestSlashNew_PrintsRelaunchCommand(t *testing.T) {
	m := testModelWithRegistry(newFakeBackend(), testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.composer.SetValue("/new")
	runCmd(m.submitComposer())
	if len(printed) != 1 || !strings.Contains(printed[0], "start fresh: agora -thread ") {
		t.Fatalf("printed = %v; want the relaunch hint", printed)
	}
}

// TestSlashInit_CreatesAGENTSmd: /init writes a starter AGENTS.md with the
// three expected section headers, and does not touch the backend.
func TestSlashInit_CreatesAGENTSmd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/init")
	runCmd(m.submitComposer())

	if len(backend.Sent) != 0 {
		t.Fatalf("/init sent %v to the model, want nothing", backend.Sent)
	}
	if len(printed) != 1 || !strings.Contains(printed[0], "created AGENTS.md") {
		t.Fatalf("printed = %v; want a created-AGENTS.md confirmation", printed)
	}

	path := filepath.Join(dir, "AGENTS.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("AGENTS.md not written: %v", err)
	}
	for _, want := range []string{"# AGENTS.md", "## Build & test", "## Conventions", "## Gotchas"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("AGENTS.md missing %q:\n%s", want, got)
		}
	}
}

// TestSlashInit_AlreadyExists: a second /init leaves the file byte-identical
// and reports it already exists instead of overwriting.
func TestSlashInit_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "AGENTS.md")
	original := []byte("operator's own AGENTS.md content\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/init")
	runCmd(m.submitComposer())

	if len(backend.Sent) != 0 {
		t.Fatalf("/init sent %v to the model, want nothing", backend.Sent)
	}
	if len(printed) != 1 || printed[0] != "AGENTS.md already exists" {
		t.Fatalf("printed = %v; want [\"AGENTS.md already exists\"]", printed)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("AGENTS.md was modified: got %q, want unchanged %q", got, original)
	}
}

// TestDiffArgsDisallowed_RejectsShellMetacharacters pins the validator
// slash.go's runSlashDiff checks before ever building an argv/exec.Cmd — the
// exec path itself (never invoking a shell, exec.CommandContext splits argv
// directly) is exercised by the non-git-repo and real-repo tests below.
func TestDiffArgsDisallowed_RejectsShellMetacharacters(t *testing.T) {
	bad := []string{";rm -rf", "a && b", "a | b", "a & b", "$HOME", "a > b", "a < b", "a `b`", "a\nb"}
	for _, args := range bad {
		if !diffArgsDisallowed.MatchString(args) {
			t.Errorf("diffArgsDisallowed(%q) = false, want true (rejected)", args)
		}
	}
	good := []string{"", "--staged", "-- path/to/file.go", "HEAD~1"}
	for _, args := range good {
		if diffArgsDisallowed.MatchString(args) {
			t.Errorf("diffArgsDisallowed(%q) = true, want false (allowed)", args)
		}
	}
}

// TestSlashDiff_RejectsUnsupportedArgument: end-to-end through submitComposer
// — a shell-metacharacter argument never reaches exec.Command (if it did,
// this test's temp cwd has no git repo and no `;rm -rf` target to prove it
// against, but the validator test above pins that no exec happens at all)
// and nothing reaches the model.
func TestSlashDiff_RejectsUnsupportedArgument(t *testing.T) {
	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/diff ;rm -rf")
	runCmd(m.submitComposer())

	if len(backend.Sent) != 0 {
		t.Fatalf("/diff sent %v to the model, want nothing", backend.Sent)
	}
	if len(printed) != 1 || printed[0] != "unsupported /diff argument" {
		t.Fatalf("printed = %v; want [\"unsupported /diff argument\"]", printed)
	}
}

// TestSlashDiff_NonGitRepo_FriendlyError: no crash, no model send, a
// friendly one-liner when the cwd isn't a git repo at all.
func TestSlashDiff_NonGitRepo_FriendlyError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Chdir(t.TempDir())

	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/diff")
	runCmd(m.submitComposer())

	if len(backend.Sent) != 0 {
		t.Fatalf("/diff sent %v to the model, want nothing", backend.Sent)
	}
	if len(printed) != 1 || printed[0] != "not a git repository" {
		t.Fatalf("printed = %v; want [\"not a git repository\"]", printed)
	}
}

// TestSlashDiff_EmptyRepo_NoChanges: an initialized git repo with nothing
// staged/modified reports the friendly "no changes" line, no crash.
func TestSlashDiff_EmptyRepo_NoChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	t.Chdir(dir)

	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/diff")
	runCmd(m.submitComposer())

	if len(backend.Sent) != 0 {
		t.Fatalf("/diff sent %v to the model, want nothing", backend.Sent)
	}
	if len(printed) != 1 || printed[0] != "no changes" {
		t.Fatalf("printed = %v; want [\"no changes\"]", printed)
	}
}

// TestSlashDiff_RendersModifiedFile: a real git repo with one tracked file
// modified — /diff renders it through DiffCell's own gutter/line-number
// shape and reaches neither an error path nor the model.
func TestSlashDiff_RendersModifiedFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package hello\n\nfunc old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "hello.go")
	run("commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package hello\n\nfunc newer() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	backend := newFakeBackend()
	m := testModelWithRegistry(backend, testRegistry())
	m.width = 120 // wide enough that the gutter/content doesn't hard-wrap the asserted text
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/diff")
	runCmd(m.submitComposer())

	if len(backend.Sent) != 0 {
		t.Fatalf("/diff sent %v to the model, want nothing", backend.Sent)
	}
	if len(printed) != 1 {
		t.Fatalf("printed = %v; want one block", printed)
	}
	out := printed[0]
	for _, want := range []string{"hello.go", "func old() {}", "func newer() {}"} {
		if !strings.Contains(out, want) {
			t.Errorf("/diff output missing %q:\n%s", want, out)
		}
	}
}

// TestParseUnifiedDiff_FallsBackWhenNoFileHeader: input with no recognizable
// "diff --git" header at all can't be placed into a DiffCell (no path, no
// line numbers to anchor to) — parseUnifiedDiff reports ok=false so
// renderDiffOutput falls back to plain text instead of silently dropping it.
func TestParseUnifiedDiff_FallsBackWhenNoFileHeader(t *testing.T) {
	_, ok := parseUnifiedDiff("some output with no diff --git header at all\n")
	if ok {
		t.Fatal("parseUnifiedDiff ok=true for input with no file header; want false (fallback)")
	}
}

// TestRenderDiffOutput_PlainFallback_Sanitized: the plain-text fallback path
// still sanitizes (finding #3's boundary rule applies here too, since this
// content is a subprocess's own stdout — not agent content today, but the
// same rule keeps the invariant "nothing unsanitized reaches the terminal"
// simple and unconditional).
func TestRenderDiffOutput_PlainFallback_Sanitized(t *testing.T) {
	out := renderDiffOutput("no diff --git header here\x1b[31mred\x1b[0m\n", 60, PlainTheme())
	if strings.Contains(out, "\x1b") {
		t.Fatalf("plain fallback did not sanitize: %q", out)
	}
	if !strings.Contains(out, "red") {
		t.Fatalf("plain fallback dropped content: %q", out)
	}
}

// TestSlashClear_ClearsScreenAndReprintsHeader.
func TestSlashClear_ClearsScreenAndReprintsHeader(t *testing.T) {
	m := testModelWithRegistry(newFakeBackend(), testRegistry())
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)
	m.composer.SetValue("/clear")
	cmd := m.submitComposer()
	if cmd == nil {
		t.Fatal("submitComposer(/clear) returned nil cmd")
	}
	// The header is printed synchronously (m.cfg.Printer(header) is built
	// eagerly as one of tea.Batch's arguments); the returned cmd, when run,
	// carries the tea.ClearScreen message (compactCmds collapses a batch
	// with one non-nil member — capturingPrinter's nil — down to the single
	// remaining cmd, so this is NOT a BatchMsg here).
	if msg := cmd(); msg == nil {
		t.Fatal("/clear cmd produced no message")
	}
	if len(printed) != 1 {
		t.Fatalf("printed = %v; want the re-printed session header", printed)
	}
}
