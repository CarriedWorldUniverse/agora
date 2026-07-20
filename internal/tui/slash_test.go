package tui

import (
	"errors"
	"strings"
	"testing"

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

// TestSubmitComposer_UnknownSlash_StillSent: the near-miss contract — an
// unrecognized /word is an ordinary message for the model, not an error
// and not swallowed (same rule isExitCommand pins for "/quitter").
func TestSubmitComposer_UnknownSlash_StillSent(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	m.composer.InsertText("/quitter")
	// submitComposer returns an echo+send batch since #72 — drain it so the
	// send actually fires (kimi's branch predated the batch).
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	runCmd(cmd)
	if len(backend.Sent) != 1 || backend.Sent[0].Text != "/quitter" {
		t.Fatalf("Sent = %+v; want one user message with text /quitter", backend.Sent)
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
