// Slash-command processor for the input prompt. Spec §13.1.6.
//
// Lines starting with `/` are commands, not chat. Each command has a
// name, short help line, and handler that returns a (possibly nil)
// tea.Cmd. Registry is intentionally small + flat — growing it just
// means adding a row.
//
// Today's commands: /exit, /help, /retry, and /ts.
package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// QuitGraceful is the tea.Msg /exit emits.
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
			help:    "exit cleanly",
			handler: cmdExit,
		},
		{
			name:    "help",
			help:    "list available commands",
			handler: cmdHelp,
		},
		{
			name:    "retry",
			help:    "send the last submitted message again",
			handler: cmdRetry,
		},
		{
			name:    "ts",
			help:    "toggle inline timestamps",
			handler: cmdTS,
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
		m.appendBlock(chatBlock{
			class:     blockSystem,
			speaker:   "system",
			createdAt: time.Now(),
		})
		m.blocks[len(m.blocks)-1].body.WriteString("type /help for available commands")
		m.refreshChatContent(false)
		return nil, true
	}
	for _, def := range commands() {
		if def.name == name {
			return def.handler(m, args), true
		}
	}
	m.appendBlock(chatBlock{
		class:     blockSystem,
		speaker:   "system",
		createdAt: time.Now(),
	})
	m.blocks[len(m.blocks)-1].body.WriteString(fmt.Sprintf("unknown command: /%s (try /help)", name))
	m.refreshChatContent(false)
	return nil, true
}

// cmdExit emits QuitGraceful.
func cmdExit(m *Model, _ string) tea.Cmd {
	m.appendBlock(chatBlock{
		class:     blockSystem,
		speaker:   "system",
		createdAt: time.Now(),
	})
	m.blocks[len(m.blocks)-1].body.WriteString("exiting...")
	m.refreshChatContent(false)
	return func() tea.Msg { return QuitGraceful{} }
}

// cmdTS toggles inline timestamps in the chat view.
func cmdTS(m *Model, _ string) tea.Cmd {
	m.showTimestamps = !m.showTimestamps
	m.refreshChatContent(false)
	return nil
}

// cmdRetry re-submits the last message the operator sent.
func cmdRetry(m *Model, _ string) tea.Cmd {
	if m.lastSubmitted == "" {
		m.appendBlock(chatBlock{
			class:     blockSystem,
			speaker:   "system",
			createdAt: time.Now(),
		})
		m.blocks[len(m.blocks)-1].body.WriteString("nothing to retry — submit a message first")
		m.refreshChatContent(false)
		return nil
	}
	text := m.lastSubmitted
	ackTimeout := m.appendOptimistic(text)
	m.refreshChatContent(false)
	if m.cfg.Client == nil {
		return ackTimeout
	}
	send := func() tea.Msg {
		if err := m.cfg.Client.ChatSend(context.Background(), "@"+m.cfg.Agent+" "+text, m.threadTopic(), 0); err != nil {
			return SendFailed{Text: text, Err: err}
		}
		return nil
	}
	return tea.Batch(send, ackTimeout)
}

// commandNames returns the registered command names, alphabetised,
// used by the slash hint renderer and tab completion.
func commandNames() []string {
	defs := commands()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.name)
	}
	sort.Strings(out)
	return out
}

// cmdHelp renders the registry into a system-class block.
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
	m.appendBlock(chatBlock{
		class:     blockSystem,
		speaker:   "system",
		createdAt: time.Now(),
	})
	m.blocks[len(m.blocks)-1].body.WriteString(b.String())
	m.refreshChatContent(false)
	return nil
}
