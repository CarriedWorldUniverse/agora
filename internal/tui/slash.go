package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// This file is the slash-command dispatch (agora-spec-tui.md §6's v1 set
// grows here verb by verb). The composer hands submitted text to
// slashDispatch BEFORE it falls through to a user_message: an exact,
// case-insensitive match on the first token intercepts; EVERYTHING else
// starting with "/" — unknown verbs, near-misses like "/quitter", absolute
// paths like "/usr/bin" — still goes to the model as an ordinary message,
// preserving the documented isExitCommand near-miss behavior (exit_test.go).
// The richer affordances §4/§6 spec (leading-/ opens a filtered menu,
// completed command becomes an atomic token, per-command metadata) are the
// later UI pass this table is designed to grow into.

// ServerInfo is one configured MCP server as the TUI needs it for /mcp —
// deliberately NOT mcp.ServerConfig, so internal/tui keeps no dependency
// on internal/mcp; cmd/agora adapts the loader's output into this shape.
type ServerInfo struct {
	Name      string
	Transport string // "stdio" | "streamable_http" | ...
	Detail    string // command line (stdio) or URL (http)
	Enabled   bool
}

// slashCommand is one known /verb. Run returns the tea.Cmd the command
// produces (usually a single Printer call carrying the rendered block).
// Exit verbs (/quit, /exit, /q) are NOT in this table — they keep their
// existing isExitCommand path and semantics (trim/case tolerance pinned by
// exit_test.go).
type slashCommand struct {
	name string
	desc string
	run  func(m *Model, args string) tea.Cmd
}

// slashCommandTable is the dispatch table, built per call (not a
// package-level var) because runSlashHelp itself iterates the table — a
// package var would be an initialization cycle. §6's full v1 set (/model,
// /status, /skills, ...) slots in here as each verb's backing state gets
// wired.
func slashCommandTable() []slashCommand {
	return []slashCommand{
		{name: "help", desc: "list available commands", run: runSlashHelp},
		{name: "mcp", desc: "list configured MCP servers", run: runSlashMCP},
	}
}

// slashDispatch parses submitted composer text of the form "/name args…".
// The bool is true only when the first token exactly names a known verb
// (case-insensitive); args is everything after that token, trimmed.
func slashDispatch(text string) (cmd slashCommand, args string, ok bool) {
	t := strings.TrimSpace(text)
	fields := strings.Fields(t)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return slashCommand{}, "", false
	}
	name := strings.TrimPrefix(fields[0], "/")
	for _, c := range slashCommandTable() {
		if strings.EqualFold(c.name, name) {
			return c, strings.TrimSpace(t[len(fields[0]):]), true
		}
	}
	return slashCommand{}, "", false
}

// runSlashHelp prints the available commands (including the exit verbs,
// which live outside the table) as one transcript block.
func runSlashHelp(m *Model, _ string) tea.Cmd {
	var b strings.Builder
	b.WriteString(m.cfg.Theme.Header.Render("commands"))
	row := func(verb, desc string) {
		b.WriteString(fmt.Sprintf("\n  %s  %s", m.cfg.Theme.Accent.Render(verb), m.cfg.Theme.Muted.Render(desc)))
	}
	for _, c := range slashCommandTable() {
		row("/"+c.name, c.desc)
	}
	row("/quit, /exit, /q", "quit agora")
	return m.cfg.Printer(b.String())
}

// runSlashMCP prints the configured-MCP-servers report as one block.
func runSlashMCP(m *Model, _ string) tea.Cmd {
	return m.cfg.Printer(strings.Join(renderMCPReport(m.cfg.ListServers, m.cfg.Theme), "\n"))
}

// renderMCPReport builds the /mcp transcript block. Honest v1: the engine
// exposes no LIVE MCP connection state yet — no mcp.Manager is constructed
// in the turn path, and no server-status event exists on the session wire
// — so this reports CONFIGURED servers (via cfg.ListServers, which
// cmd/agora wires to the .mcp.json loader) and says so plainly, rather
// than implying a connection that was never attempted.
func renderMCPReport(list func() ([]ServerInfo, error), th Theme) []string {
	header := th.Header.Render("MCP servers")
	if list == nil {
		return []string{header, th.Muted.Render("  server list not available on this connection")}
	}
	servers, err := list()
	if err != nil {
		return []string{header, th.Danger.Render("  error reading MCP config: " + err.Error())}
	}
	if len(servers) == 0 {
		return []string{header, th.Muted.Render("  none configured — add servers to .mcp.json")}
	}
	out := []string{header}
	for _, s := range servers {
		name := s.Name
		if !s.Enabled {
			name += " (disabled)"
		}
		out = append(out, fmt.Sprintf("  %s  %s", name, th.Muted.Render(s.Transport+" · "+s.Detail)))
	}
	out = append(out, th.Muted.Render("  configured only — live connection state is not exposed by the engine yet"))
	return out
}
