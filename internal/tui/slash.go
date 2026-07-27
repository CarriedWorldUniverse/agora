package tui

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"

	tea "github.com/charmbracelet/bubbletea"
)

// This file is the slash-command dispatch (agora-spec-tui.md §6's v1 set
// grows here verb by verb). The composer hands submitted text to
// slashDispatch BEFORE it falls through to a user_message: an exact,
// case-insensitive match on the first token intercepts.
//
// §6a containment (NEX-795): unlike the near-miss behavior this file used
// to document, EVERYTHING starting with "/" is now intercepted one way or
// another — slashDispatch itself still only matches known verbs (that
// contract is unchanged, and pinned by TestSlashDispatch_MatchesKnownVerbsOnly),
// but submitComposer (model.go) never lets an unmatched "/word" fall through
// to the model as an ordinary message anymore. A near-miss like "/quitter"
// or an absolute path like "/usr/bin/gcc" now prints a local "unknown
// command" error (with a nearest-match suggestion) instead of being
// role-played by the model as a fake CLI. See model.go's containment block
// right after the slashDispatch call for the registry-name-sugar and
// escape-hatch (leading space / "\/") handling that sits around it.
//
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

// PermissionInfo is one saved approval grant, for /permissions. Mirrors
// approval.DisplayGrant, restated here so internal/tui keeps no dependency
// on the approval package (the same split ServerInfo uses for mcp).
type PermissionInfo struct {
	Kind      string
	Scope     string
	Key       string
	GrantedAt string
	// Global marks a grant that applies in EVERY project, not just this
	// one — worth showing differently, since it is the wider authority.
	Global bool
}

// HookInfo is one discovered lifecycle hook as the TUI needs it for /hooks —
// deliberately NOT hooks.ResolvedHandler, so internal/tui keeps no dependency
// on internal/hooks; cmd/agora adapts the runner's output into this shape
// (the same split ServerInfo uses for mcp).
type HookInfo struct {
	Event   string
	Key     string // PositionalKey — what hooks-state.json records trust under
	Command string
	Matcher string
	// Trust is the resolved trust state ("Trusted"/"Untrusted"/"Modified"/…).
	Trust string
	// Runnable is the fail-closed gate: false means this hook will NOT fire.
	Runnable bool
	// Hash is the content hash to record in hooks-state.json to trust it.
	Hash string
	// StatePath is where that file lives, so the block can say it exactly.
	StatePath string
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

// printAsync defers a handler's real work off the bubbletea Update
// goroutine and prints whatever it produces.
//
// Bubble Tea's contract is that Update returns PROMPTLY and the tea.Cmd it
// returns does the blocking work on its own goroutine. A slash handler that
// reads the disk, walks the thread store, or spawns a subprocess BEFORE
// constructing its Cmd runs that work inside Update's call stack, and for
// as long as it takes the program renders nothing and processes no
// keystrokes — the terminal is frozen (agora#138: /diff's git subprocess is
// bounded only by a 5s timeout, so index-lock contention froze the UI for
// the full five seconds).
//
// work MUST NOT touch Model state: it runs on another goroutine, and the
// Model belongs to Update. Capture the specific config values it needs
// (theme, a lookup func) by value at call time, as the handlers here do —
// never close over m. State changes belong in a tea.Msg the Update loop
// applies (see statusErrMsg).
func printAsync(p Printer, work func() string) tea.Cmd {
	return func() tea.Msg {
		cmd := p(work())
		if cmd == nil {
			return nil
		}
		return cmd()
	}
}

// slashCommandTable is the dispatch table, built per call (not a
// package-level var) because runSlashHelp itself iterates the table — a
// package var would be an initialization cycle. §6's full v1 set (/model,
// /status, /skills, ...) slots in here as each verb's backing state gets
// wired.
func slashCommandTable() []slashCommand {
	return []slashCommand{
		{name: "status", desc: "show agent, model, thread, dir, backend, usage", run: runSlashStatus},
		{name: "effort", desc: "show or set this session's reasoning-effort tier", run: runSlashEffort},
		{name: "fork", desc: "branch the thread at the current point", run: runSlashFork},
		{name: "copy", desc: "copy the last agent message to the clipboard", run: runSlashCopy},
		{name: "clear", desc: "clear the screen and start a new active cell", run: runSlashClear},
		{name: "compact", desc: "compact the thread context (between turns)", run: runSlashCompact},
		{name: "new", desc: "print the command to start a fresh thread", run: runSlashNew},
		{name: "mcp", desc: "list configured MCP servers", run: runSlashMCP},
		{name: "hooks", desc: "list discovered lifecycle hooks and their trust state", run: runSlashHooks},
		{name: "permissions", desc: "list or revoke saved approval grants", run: runSlashPermissions},
		{name: "mode", desc: "show this session's approval posture", run: runSlashMode},
		{name: "init", desc: "create AGENTS.md", run: runSlashInit},
		{name: "diff", desc: "show git diff", run: runSlashDiff},
		{name: "help", desc: "list available commands", run: runSlashHelp},
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

// helpOrder is the §6 "menu order = frequency, not alphabetical" list.
// /model and /resume aren't in slashCommandTable (they're special-cased in
// submitComposer ahead of slashDispatch, since they parse multi-word forms
// like "/model opus" and "/resume all") — this list carries their
// descriptions directly so /help still shows them in the right spot.
var helpOrder = []struct{ name, fallbackDesc string }{
	{"model", "switch or list available models"},
	{"effort", "show or set this session's reasoning-effort tier"},
	{"status", "show session status"},
	{"resume", "list persisted threads for this dir"},
	{"fork", "branch the thread at the current point"},
	{"diff", "show git diff"},
	{"copy", "copy the last agent message to the clipboard"},
	{"clear", "clear the screen"},
	{"compact", "compact the thread context"},
	{"new", "start a fresh thread"},
	{"init", "create AGENTS.md"},
	{"mcp", "list configured MCP servers"},
	{"hooks", "list lifecycle hooks and their trust state"},
	{"permissions", "list or revoke saved approval grants"},
	{"mode", "show this session's approval posture"},
	{"help", "list available commands"},
}

// runSlashHelp prints the available commands (including the exit verbs,
// which live outside the table) as one transcript block, in helpOrder.
func runSlashHelp(m *Model, _ string) tea.Cmd {
	var b strings.Builder
	b.WriteString(m.cfg.Theme.Header.Render("commands"))
	row := func(verb, desc string) {
		b.WriteString(fmt.Sprintf("\n  %s  %s", m.cfg.Theme.Accent.Render(verb), m.cfg.Theme.Muted.Render(desc)))
	}
	descs := map[string]string{}
	for _, c := range slashCommandTable() {
		descs[c.name] = c.desc
	}
	for _, o := range helpOrder {
		desc := o.fallbackDesc
		if d, ok := descs[o.name]; ok {
			desc = d
		}
		row("/"+o.name, desc)
	}
	row("%model[:effort]", "one-shot override for THIS message only, e.g. %:xhigh or %sonnet:high")
	row("/quit, /exit, /q", "quit agora")
	return m.cfg.Printer(b.String())
}

// runSlashMCP prints the configured-MCP-servers report as one block.
func runSlashMCP(m *Model, _ string) tea.Cmd {
	// .mcp.json is read on the Cmd goroutine, not in Update — see printAsync.
	list, th := m.cfg.ListServers, m.cfg.Theme
	return printAsync(m.cfg.Printer, func() string {
		return strings.Join(renderMCPReport(list, th), "\n")
	})
}

// renderMCPReport builds the /mcp transcript block. Honest v1: this
// reports CONFIGURED servers (via cfg.ListServers, which cmd/agora wires
// to the .mcp.json loader) and says so plainly, rather than implying a
// connection that was never attempted.
//
// The reason is NOT that MCP is unwired — it is (buildMCPSource ->
// WithMCPSource -> toolrunner.NewSurface, at the shared engine seam). The
// reason is that no server-status event exists on the SESSION WIRE, so a
// client — which is what the TUI is, even in the in-process lane — has no
// way to learn whether a given server actually started, or is still up.
// Surfacing live state means adding that event, not reaching for the
// source; a client that read the source directly would report the daemon
// lane's servers wrongly (they live in the daemon's process, not this one).
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

func runSlashHooks(m *Model, _ string) tea.Cmd {
	// Hook discovery walks the config layers and content-hashes each
	// handler — real disk work, so it runs on the Cmd goroutine.
	list, th := m.cfg.ListHooks, m.cfg.Theme
	return printAsync(m.cfg.Printer, func() string {
		return strings.Join(renderHooksReport(list, th), "\n")
	})
}

// renderHooksReport builds the /hooks transcript block.
//
// This verb exists because hook trust is fail-closed and had no surface: a
// handler with no recorded hash never runs, and until NEX-825 nothing said
// so — a configured hook silently did nothing. DiscoverHooks now emits a
// warning, but in TUI mode that goes to stderr during engine construction,
// where the alt-screen swallows it; the operator still could not SEE why
// their hook was inert. So the report belongs here, on demand, where it can
// also hand over the exact hooks-state.json entry that would enable each one.
func renderHooksReport(list func() ([]HookInfo, error), th Theme) []string {
	header := th.Header.Render("lifecycle hooks")
	if list == nil {
		return []string{header, th.Muted.Render("  hook list not available on this connection")}
	}
	hooks, err := list()
	if err != nil {
		return []string{header, th.Danger.Render("  error reading hooks: " + err.Error())}
	}
	if len(hooks) == 0 {
		return []string{header, th.Muted.Render("  none discovered — add handlers to .agora/hooks.json (project) or ~/.agora/hooks.json (user)")}
	}
	out := []string{header}
	var blocked int
	for _, h := range hooks {
		mark := th.Muted.Render("will not run")
		if h.Runnable {
			mark = th.Accent.Render("active")
		} else {
			blocked++
		}
		matcher := h.Matcher
		if matcher == "" {
			matcher = "*"
		}
		out = append(out,
			fmt.Sprintf("  %-14s %-9s %s", h.Event, h.Trust, mark),
			th.Muted.Render("      matcher "+matcher+" · "+h.Command))
	}
	if blocked > 0 {
		out = append(out,
			"",
			th.Muted.Render(fmt.Sprintf("  %d hook(s) will not run: trust is fail-closed, so a handler only fires", blocked)),
			th.Muted.Render("  once its content hash is recorded. To allow one, add to "+hooks[0].StatePath+":"))
		for _, h := range hooks {
			if h.Runnable {
				continue
			}
			out = append(out, th.Muted.Render(fmt.Sprintf("      %q: {\"enabled\": true, \"trusted_hash\": %q}", h.Key, h.Hash)))
		}
		out = append(out, th.Muted.Render("  Editing a trusted hook changes its hash and revokes the grant."))
	}
	return out
}

// runSlashPermissions shows the approval grants that outlive this session,
// and revokes one with `/permissions revoke <kind> <scope> <key>`.
//
// A permission store the operator cannot inspect is a liability: grants
// accumulate silently and there is no way to answer "what has this thing
// been allowed to do?". Listing is therefore the default, and revoke is
// deliberately explicit (all three fields) rather than an index into the
// printed list, which would shift under a concurrent grant.
func runSlashPermissions(m *Model, args string) tea.Cmd {
	th := m.cfg.Theme
	header := th.Header.Render("Saved permissions")

	if m.cfg.ListPermissions == nil {
		return m.cfg.Printer(strings.Join([]string{
			header, th.Muted.Render("  not available on this connection"),
		}, "\n"))
	}

	if fields := strings.Fields(args); len(fields) > 0 {
		if !strings.EqualFold(fields[0], "revoke") {
			return m.cfg.Printer(strings.Join([]string{
				header, th.Danger.Render("  usage: /permissions [revoke <kind> <scope> <key>]"),
			}, "\n"))
		}
		return m.revokePermission(fields[1:], header)
	}

	// permissions.json is read on the Cmd goroutine — see printAsync.
	listPermissions := m.cfg.ListPermissions
	return printAsync(m.cfg.Printer, func() string {
		return renderPermissionsReport(listPermissions, th, header)
	})
}

// renderPermissionsReport builds the /permissions listing. Split out of
// runSlashPermissions so the store read happens inside a tea.Cmd rather
// than in Update's call stack (agora#138); it takes everything it needs by
// value and never touches the Model.
func renderPermissionsReport(listPermissions func() ([]PermissionInfo, error), th Theme, header string) string {
	grants, err := listPermissions()
	if err != nil {
		return strings.Join([]string{
			header, th.Danger.Render("  error reading saved permissions: " + err.Error()),
		}, "\n")
	}
	if len(grants) == 0 {
		return strings.Join([]string{
			header, th.Muted.Render("  none saved — approvals granted with a wider scope than once appear here"),
		}, "\n")
	}

	out := []string{header}
	for _, g := range grants {
		line := fmt.Sprintf("  %s %s %s", g.Kind, g.Scope, g.Key)
		if g.Global {
			line += th.Muted.Render("  [all projects]")
		}
		if g.GrantedAt != "" {
			line += th.Muted.Render("  " + g.GrantedAt)
		}
		out = append(out, line)
	}
	out = append(out, th.Muted.Render("  revoke with: /permissions revoke <kind> <scope> <key>"))
	return strings.Join(out, "\n")
}

// revokePermission handles the `revoke` subcommand's arguments.
func (m *Model) revokePermission(rest []string, header string) tea.Cmd {
	th := m.cfg.Theme
	if m.cfg.RevokePermission == nil {
		return m.cfg.Printer(strings.Join([]string{
			header, th.Muted.Render("  revoking is not available on this connection"),
		}, "\n"))
	}
	// The key may contain spaces (a command prefix like "go test ./..."),
	// so only kind and scope are split off — everything after is the key.
	if len(rest) < 3 {
		return m.cfg.Printer(strings.Join([]string{
			header, th.Danger.Render("  usage: /permissions revoke <kind> <scope> <key>"),
		}, "\n"))
	}
	kind, scope := rest[0], rest[1]
	key := strings.Join(rest[2:], " ")

	// The revoke rewrites permissions.json — done on the Cmd goroutine.
	revoke := m.cfg.RevokePermission
	return printAsync(m.cfg.Printer, func() string {
		return renderRevokeResult(revoke, th, header, kind, scope, key)
	})
}

// renderRevokeResult performs the revoke and renders its outcome. Split out
// of revokePermission so the store write happens inside a tea.Cmd rather
// than in Update's call stack (agora#138).
func renderRevokeResult(revoke func(kind, scope, key string) (bool, error), th Theme, header, kind, scope, key string) string {
	removed, err := revoke(kind, scope, key)
	switch {
	case err != nil:
		return strings.Join([]string{
			header, th.Danger.Render("  error revoking: " + err.Error()),
		}, "\n")
	case !removed:
		return strings.Join([]string{
			header, th.Muted.Render(fmt.Sprintf("  no saved grant matches %s %s %s", kind, scope, key)),
		}, "\n")
	}
	return strings.Join([]string{
		header,
		fmt.Sprintf("  revoked %s %s %s", kind, scope, key),
		// Say plainly that this session is unchanged — the store keeps the
		// grant live in memory on purpose, and a message implying otherwise
		// would misrepresent what just happened.
		th.Muted.Render("  takes effect in the next session; this one keeps the grant it already resolved against"),
	}, "\n")
}

// runSlashMode reports the approval posture in force.
//
// Deliberately READ-ONLY. Swapping the policy mid-session would need an
// engine-side setter with defined semantics for approvals already in
// flight, and a half-applied posture is worse than none — so this answers
// "what am I running?" and points at the two places that decide it, rather
// than pretending to a capability that is not wired.
func runSlashMode(m *Model, _ string) tea.Cmd {
	th := m.cfg.Theme
	current := m.cfg.PermissionMode
	if current == "" {
		current = "sandbox-auto (engine default)"
	}
	out := []string{
		th.Header.Render("Approval mode"),
		"  " + current,
	}
	if m.cfg.ModeCatalog != nil {
		out = append(out, "", th.Muted.Render("  available:"))
		for _, e := range m.cfg.ModeCatalog() {
			out = append(out, fmt.Sprintf("    %-16s %s", e[0], th.Muted.Render(e[1])))
		}
	}
	out = append(out, "", th.Muted.Render("  set with: agora -mode <name>, or permission_mode in .agora/config.json"))
	return m.cfg.Printer(strings.Join(out, "\n"))
}

// runSlashStatus prints agent id, current model, thread id, working dir,
// backend mode, and session usage as one block (§5: "context-remaining …
// Show in footer and /status"; usage reuses the exact numbers the idle
// status row shows via usageSegment).
func runSlashStatus(m *Model, _ string) tea.Cmd {
	var b strings.Builder
	b.WriteString(m.cfg.Theme.Header.Render("status"))
	row := func(label, val string) {
		fmt.Fprintf(&b, "\n  %-8s %s", label+":", val)
	}
	row("agent", m.cfg.AgentID)
	row("model", m.currentModel)
	effort := "default (engine-configured)"
	if m.currentEffort != "" {
		effort = string(m.currentEffort)
	}
	row("effort", effort)
	row("thread", m.cfg.ThreadID)
	row("dir", cwdOrDot())
	row("backend", backendMode(m.cfg.Backend))
	usage := "no completed turns yet"
	if m.haveUsage {
		usage = strings.TrimPrefix(strings.TrimSpace(m.usageSegment()), "· ")
	}
	row("usage", usage)
	// Only shown once a rate_limit event has actually arrived — the
	// overwhelming majority of sessions (API key, Bedrock, Vertex, any
	// non-subscription provider) never receive one, and a permanent
	// "plan: n/a" row would be clutter for every one of them for a signal
	// that will never apply.
	if m.haveRateLimit {
		row("plan", renderRateLimit(m.rateLimit))
	}
	return m.cfg.Printer(b.String())
}

// renderRateLimit formats one contracts.RateLimit for /status — e.g.
// "five_hour 82% (allowed_warning, resets 14:32)" or "overage 12%
// (overage credits in use)" when UsingOverage.
func renderRateLimit(rl contracts.RateLimit) string {
	window := rl.WindowType
	if window == "" {
		window = "usage"
	}
	s := fmt.Sprintf("%s %d%%", window, rl.Utilization)
	var notes []string
	if rl.Status != "" && rl.Status != "allowed" {
		notes = append(notes, rl.Status)
	}
	if rl.UsingOverage {
		notes = append(notes, "overage credits in use")
	}
	if rl.ResetsAt != nil {
		notes = append(notes, "resets "+rl.ResetsAt.Local().Format("15:04"))
	}
	if len(notes) > 0 {
		s += " (" + strings.Join(notes, ", ") + ")"
	}
	return s
}

// effortTierOrder is the display order for the reasoning-effort ladder —
// map iteration in Go is unordered, and /effort's listing should read
// low-to-high, not shuffle between calls.
var effortTierOrder = []string{"low", "medium", "high", "xhigh", "max"}

// runSlashEffort shows or sets this session's reasoning-effort tier: bare
// `/effort` reports the current pin, `/effort <tier>` sets it (applied to
// every plain user_message until changed — see submitComposer), and
// `/effort default` clears it back to the engine's configured/hardcoded
// default. Unknown input lists the valid tiers rather than silently
// no-op'ing, same shape as handleModelCommand's unknown-name handling.
func runSlashEffort(m *Model, args string) tea.Cmd {
	args = strings.ToLower(strings.TrimSpace(args))
	if args == "" {
		current := "default (engine-configured)"
		if m.currentEffort != "" {
			current = string(m.currentEffort)
		}
		return m.cfg.Printer(fmt.Sprintf("effort: %s — tiers: %s (or \"default\" to clear)", current, strings.Join(effortTierOrder, ", ")))
	}
	if args == "default" {
		m.currentEffort = ""
		return m.cfg.Printer("effort reset to default (engine-configured)")
	}
	tier, known := effortLadder[args]
	if !known {
		return m.cfg.Printer(fmt.Sprintf("unknown effort tier %q — tiers: %s", args, strings.Join(effortTierOrder, ", ")))
	}
	m.currentEffort = tier
	return m.cfg.Printer("effort set to " + string(tier))
}

// backendMode reports whether this connection speaks to a daemon over a
// socket/websocket (*ioBackend, this package) or runs the engine in-process
// (anything else — cmd/agora's inProcessBackend embeds tui.Backend by
// interface, so it can't be named here; "in-process" is simply the default
// when it isn't the known socket type). Test doubles (fakeBackend) report
// "in-process" too, which is the honest answer: there is no socket.
func backendMode(b Backend) string {
	if _, ok := b.(*ioBackend); ok {
		return "socket"
	}
	return "in-process"
}

// runSlashCopy puts the last finalized agent message (raw markdown) on the
// terminal's clipboard via an OSC 52 escape sequence — the standard,
// SSH-transparent "ask the terminal to set the clipboard" mechanism (no X11
// clipboard needed, works through the same connection the session runs
// over). An empty transcript (no agent reply yet this session) is a plain
// message, not an error.
func runSlashCopy(m *Model, _ string) tea.Cmd {
	if m.lastAgentMessage == "" {
		return m.cfg.Printer("nothing to copy")
	}
	payload := base64.StdEncoding.EncodeToString([]byte(m.lastAgentMessage))
	osc52 := "\x1b]52;c;" + payload + "\x07"
	return tea.Batch(
		m.cfg.Printer(osc52),
		m.cfg.Printer(fmt.Sprintf("copied %d chars", len(m.lastAgentMessage))),
	)
}

// runSlashCompact asks the engine to run a manual compaction episode
// (context spec §2 contract 5: between turns only — the engine skips the
// request if a turn is in flight; retry once it ends). The request rides
// the InConfig{Key:"compact"} backend seam; results surface as the
// thread.compaction.started/.completed events + a persisted marker item.
func runSlashCompact(m *Model, _ string) tea.Cmd {
	if m.running {
		return m.cfg.Printer("a turn is running — /compact only works between turns")
	}
	return tea.Batch(
		m.send(contracts.Input{Type: contracts.InConfig, Key: "compact"}),
		m.cfg.Printer("compaction requested"),
	)
}

// runSlashClear clears the terminal (tea.ClearScreen) and re-prints a fresh
// session header so the operator isn't left looking at a blank screen with
// no orientation — §0's architecture means the transcript itself lives in
// the terminal's own scrollback, not in any widget/list this Model holds,
// so there is no separate "transcript state" to reset here: an in-flight
// active cell (m.stream), the composer, and session usage totals are all
// untouched by /clear (a running turn keeps running; the session's
// cumulative cost/token count is a running total, not scrollback).
func runSlashClear(m *Model, _ string) tea.Cmd {
	header := Cell{Kind: CellSessionHeader, AgentID: m.cfg.AgentID, Model: m.currentModel}.Render(m.width, m.cfg.Theme)[0]
	return tea.Batch(tea.ClearScreen, m.cfg.Printer(header))
}

// runSlashFork forks the thread at the highest item Seq this attachment has
// seen (m.lastItemSeq — see handleEvent), via the optional ThreadForker
// backend seam. v1: no live thread switch (agora-spec-tui.md §6a's sibling
// note under /fork) — the operator relaunches into the new thread, same as
// /resume's listing hint.
func runSlashFork(m *Model, _ string) tea.Cmd {
	forker, ok := m.cfg.Backend.(ThreadForker)
	if !ok {
		return m.cfg.Printer("fork not supported here")
	}
	newID, err := forker.ForkThread(m.cfg.ThreadID, m.lastItemSeq)
	if err != nil {
		return m.cfg.Printer("fork failed: " + err.Error())
	}
	return m.cfg.Printer(fmt.Sprintf("forked → %s — open with: agora -thread %s", newID, newID))
}

// runSlashNew prints the command to start a fresh thread — v1 does NOT swap
// the running engine in place (that needs a Manager-per-thread rebuild, the
// same limitation /resume's listing already documents); it hands back a
// ready-to-run relaunch command instead, with a freshly generated id.
func runSlashNew(m *Model, _ string) tea.Cmd {
	id := freshThreadID(cwdOrDot(), m.cfg.Now())
	return m.cfg.Printer("start fresh: agora -thread " + id)
}

// runSlashInit creates a starter AGENTS.md in the process cwd (§6: "/init —
// create AGENTS.md"). AGENTS.md is project-layer CONTEXT, not authority
// (agora-spec-prompt.md §5/§index): it's read back in via
// internal/skills' discovery/merge (agora-spec-subagents.md §6) and injected
// as user-role prose — build/test commands, conventions, gotchas are exactly
// the kind of project-specific detail that context is for, so the template
// below is shaped as those three sections. An existing AGENTS.md is never
// overwritten (that would silently destroy operator edits); file-system
// errors (permission denied, read-only fs, …) print instead of panicking —
// this is a local convenience command, not something that should ever take
// the TUI down.
func runSlashInit(m *Model, _ string) tea.Cmd {
	// Stat + write happen on the Cmd goroutine — see printAsync.
	return printAsync(m.cfg.Printer, writeAgentsMD)
}

// writeAgentsMD creates AGENTS.md and reports what happened. Split out of
// runSlashInit so the filesystem work happens inside a tea.Cmd rather than
// in Update's call stack (agora#138).
func writeAgentsMD() string {
	dir := cwdOrDot()
	path := filepath.Join(dir, "AGENTS.md")
	if _, err := os.Stat(path); err == nil {
		return "AGENTS.md already exists"
	} else if !os.IsNotExist(err) {
		return "AGENTS.md: " + err.Error()
	}
	if err := os.WriteFile(path, []byte(agentsMDTemplate(filepath.Base(dir))), 0o644); err != nil {
		return "AGENTS.md: " + err.Error()
	}
	return "created AGENTS.md — edit it, then /new or restart to pick it up"
}

// agentsMDTemplate builds the starter AGENTS.md body. name is the project
// dir's basename (cwdOrDot's "." maps to "this project" so the placeholder
// heading never reads literally as "# AGENTS.md — .").
func agentsMDTemplate(name string) string {
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "this project"
	}
	return fmt.Sprintf(`# AGENTS.md — %s

Project-specific instructions for agents working in this repository.
This file is discovered automatically and injected as context for every
agent session rooted in this directory tree — see
agora-spec-subagents.md §6.

## Build & test

<!-- how to build this project, how to run its test suite -->

## Conventions

<!-- code style, file layout, naming, anything a newcomer would need told -->

## Gotchas

<!-- known traps: things that look right but aren't, footguns, sharp edges -->
`, name)
}

// freshThreadID generates a thread id in the SAME SHAPE cmd/agora's
// cwdThreadID uses for the "default" thread ("<safe-dir-base>-<12 hex
// chars>") — cmd/agora is a main package (not importable), so this is a
// deliberate small duplication of that scheme, not a copy of its
// determinism: cwdThreadID is intentionally stable per-directory (so bare
// `agora` always reattaches to the same thread); /new needs the opposite —
// a NEW id even when called twice in the same directory in the same
// second — so the hash input is salted with now (nanosecond clock, via
// cfg.Now so tests stay deterministic) instead of being cwd-only.
func freshThreadID(cwd string, now time.Time) string {
	sum := sha256.Sum256([]byte(cwd + "\x00" + now.Format(time.RFC3339Nano) + "\x00" + fmt.Sprint(now.Nanosecond())))
	base := threadSafeChars(filepath.Base(cwd))
	if base == "" {
		base = "dir"
	}
	return base + "-" + hex.EncodeToString(sum[:])[:12]
}

// threadSafeChars keeps only [A-Za-z0-9-_], mirroring cmd/agora's
// threadSafe (kept only alphanumerics/-/_ so the id is always a safe store
// directory name).
func threadSafeChars(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// dispatchRegistrySugar implements §6a's "/<registry-name> is sugar for
// /model <name>" — the shortcut form the operator types instinctively
// (e.g. "/kimi", "/glm"). Checked AFTER the known-verb table (so a real verb
// always wins a name collision) and BEFORE the unknown-command error.
func (m *Model) dispatchRegistrySugar(text string) (tea.Cmd, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil, false
	}
	name := strings.TrimPrefix(fields[0], "/")
	for _, n := range m.cfg.ModelRegistry.Names() {
		if strings.EqualFold(n, name) {
			cmd, _ := m.handleModelCommand("/model " + n)
			return cmd, true
		}
	}
	return nil, false
}

// unknownSlashMessage builds the §6a local error for a slash-prefixed input
// that matched no known verb and no registry name: "unknown command: /word"
// plus a nearest-match suggestion when one is close enough to be useful
// (measured finding, agora-6dac2f837e54: the whole point of containment is
// that the OPERATOR, not a model role-playing a CLI, sees this).
func (m *Model) unknownSlashMessage(text string) string {
	fields := strings.Fields(text)
	typed := "/"
	if len(fields) > 0 {
		typed = fields[0]
	}
	name := strings.ToLower(strings.TrimPrefix(typed, "/"))
	if suggestion := nearestCommand(name, m.slashSuggestionCandidates()); suggestion != "" {
		return fmt.Sprintf("unknown command: %s — did you mean /%s?", typed, suggestion)
	}
	return fmt.Sprintf("unknown command: %s", typed)
}

// slashSuggestionCandidates is every name a "did you mean" can point at:
// the dispatch table's verbs, the special-cased verbs that live outside it
// (/model, /resume — handled ahead of slashDispatch in submitComposer) and
// the two spellings of quit, plus every registry name (so "/glm" typo'd as
// "/glmm" still gets a useful suggestion, not just a generic error).
func (m *Model) slashSuggestionCandidates() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, c := range slashCommandTable() {
		add(c.name)
	}
	add("model")
	add("resume")
	add("exit")
	add("quit")
	for _, n := range m.cfg.ModelRegistry.Names() {
		add(n)
	}
	return out
}

// nearestCommand returns the candidate closest to name by Levenshtein
// distance, or "" when nothing is close enough to be a useful guess (a
// wrong suggestion is worse than none — e.g. "/xyz" shouldn't confidently
// point at some unrelated three-letter command).
func nearestCommand(name string, candidates []string) string {
	best := ""
	bestDist := -1
	for _, c := range candidates {
		d := levenshtein(name, c)
		switch {
		case bestDist == -1 || d < bestDist:
			bestDist, best = d, c
		case d == bestDist && c < best:
			// Ties resolve ALPHABETICALLY, not by candidate order. Order
			// was the previous tie-break, which made suggestions depend on
			// where a verb happened to sit in the command table: adding
			// /mode silently changed "/modek" from suggesting /model to
			// suggesting /mode, since both are distance 1. A deterministic
			// rule means adding a command can never quietly reword an
			// existing suggestion.
			best = c
		}
	}
	// Half the typed name's length (floor 1, so a 1-2 char typo like "/eit"
	// -> "exit" still matches) keeps the suggestion honest: a wildly
	// different word doesn't get dressed up as "did you mean".
	maxDist := len(name) / 2
	if maxDist < 1 {
		maxDist = 1
	}
	if maxDist > 3 {
		maxDist = 3
	}
	if best == "" || bestDist > maxDist {
		return ""
	}
	return best
}

// levenshtein computes single-character-edit distance between a and b
// (insert/delete/substitute, cost 1 each) — the standard DP table, no
// dependency pulled in for something this small.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
