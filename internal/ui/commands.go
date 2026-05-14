// Slash-command processor for the input prompt. Spec §13.1.6.
//
// Lines starting with `/` are commands, not chat. Each command has a
// name, short help line, and handler that returns a (possibly nil)
// tea.Cmd. Registry is intentionally small + flat — growing it just
// means adding a row.
//
// Today's commands: /exit. Reserved for follow-ups: /help, /clear,
// /thread, /reload-keyfile, /status, /reconnect.
package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// QuitGraceful is the tea.Msg /exit emits. main.go listens for it
// (via the bubbletea program send loop) to drive the deregister +
// engine drain before tea.Quit.
type QuitGraceful struct{}

// commandHandler runs a parsed command. m is mutable (state may
// change — e.g. /clear resets chat). Return any tea.Cmd to chain.
type commandHandler func(m *Model, args string) tea.Cmd

type commandDef struct {
	name    string
	help    string
	handler commandHandler
}

// commands returns the slash-command registry. Built per-call to
// avoid an initialization cycle (cmdHelp walks the registry).
func commands() []commandDef {
	return []commandDef{
		{
			name:    "exit",
			help:    "deregister from nexus and exit cleanly",
			handler: cmdExit,
		},
		{
			name:    "help",
			help:    "list available commands",
			handler: cmdHelp,
		},
	}
}

// dispatchCommand parses the prompt line and runs the matching
// handler. Returns (cmd, true) when the line was a command (handled,
// chat path skipped); (nil, false) when it's regular chat input.
func dispatchCommand(m *Model, line string) (tea.Cmd, bool) {
	if !strings.HasPrefix(line, "/") {
		return nil, false
	}
	rest := strings.TrimPrefix(line, "/")
	name, args, _ := strings.Cut(rest, " ")
	name = strings.ToLower(strings.TrimSpace(name))
	args = strings.TrimSpace(args)
	if name == "" {
		// Lone "/" — treat as input mistake, surface help hint.
		m.appendChat(chatLine{
			class: classSystem,
			when:  time.Now(),
			body:  "type /help for available commands",
		})
		return nil, true
	}
	for _, def := range commands() {
		if def.name == name {
			return def.handler(m, args), true
		}
	}
	m.appendChat(chatLine{
		class: classSystem,
		when:  time.Now(),
		body:  fmt.Sprintf("unknown command: /%s (try /help)", name),
	})
	return nil, true
}

// cmdExit emits QuitGraceful — main.go's listener performs the
// deregister + engine drain before calling tea.Quit.
func cmdExit(m *Model, _ string) tea.Cmd {
	m.appendChat(chatLine{
		class: classSystem,
		when:  time.Now(),
		body:  "exiting — deregistering from nexus...",
	})
	return func() tea.Msg { return QuitGraceful{} }
}

// cmdHelp renders the registry into a system-class line.
func cmdHelp(m *Model, _ string) tea.Cmd {
	names := make([]string, 0, len(commands()))
	maxLen := 0
	for _, def := range commands() {
		names = append(names, def.name)
		if len(def.name) > maxLen {
			maxLen = len(def.name)
		}
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("commands:")
	for _, n := range names {
		for _, def := range commands() {
			if def.name == n {
				fmt.Fprintf(&b, "\n  /%-*s — %s", maxLen, def.name, def.help)
				break
			}
		}
	}
	m.appendChat(chatLine{
		class: classSystem,
		when:  time.Now(),
		body:  b.String(),
	})
	return nil
}
