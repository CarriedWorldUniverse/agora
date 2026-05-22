# agora UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure agora's TUI for a "DM with shadow" feel — speaker blocks, in-place streaming, since-you-left divider, visible drop feedback, mouse wheel capture. No engine/transport changes.

**Architecture:** Replace per-line `chatLine` rendering with `chatBlock` (header line + indented body). Stream tokens *into* the active block instead of below it. Drop bus-traffic painting and the operator-relevance filter. Split `model.go` into focused sibling files (`model/messages/input/blocks`). Add operator-feedback for every silent drop path.

**Tech Stack:** Go 1.26+, bubbletea v1.3+ (Elm-style Model/Update/View), bubbles (textarea, viewport), lipgloss (styling). All UI changes; engine (funnel/bridle/wsasp) untouched.

**Spec:** `docs/superpowers/specs/2026-05-23-agora-ui-redesign-design.md`

---

## File map after rewrite

```
internal/ui/
├── chat.go          ~180  block types, renderChatBlock, coalesceBlocks, renderStreamingLine
├── styles.go        ~35   (new) all lipgloss.Style declarations
├── model.go         ~150  Model struct, Config, NewModel, Init, View, Update dispatcher
├── messages.go      ~50   (new) all tea.Msg type declarations
├── input.go         ~180  (new) keystroke handling, history, slash dispatch
├── blocks.go        ~140  (new) block lifecycle, idle tracking, status render helper
├── commands.go      ~140  slash command registry (+/retry, /ts, /bus, tab completion helper)
├── chat_test.go     (new) block render + coalesce tests
├── blocks_test.go   (new) lifecycle + idle tests
├── input_test.go    (new) keystroke + history tests
├── model_test.go    (new) update dispatch + view tests

internal/engine/
├── engine.go        ~220  + Config.OnDrop callback
├── agora_return_handler.go  ~120  emits TurnStarted/TurnFailed; drops ChatPanelReply/ChatSent
├── ui_hook.go       ~50   renamed messages: TurnChunk/TurnDone
├── notify.go        unchanged
├── engine_test.go   extend: OnDrop assertion
├── agora_return_handler_test.go  (new) verify Handle doesn't double-emit FinalText

cmd/agora/
├── main.go          drop ChatDelivered send; switch to WithMouseAllMotion; wire OnDrop
```

---

## Task ordering

1. Extract `styles.go` (pure code move).
2. Split `model.go` into `model.go` + `messages.go` + `input.go` + `blocks.go` (pure code move).
3. Add `chatBlock` struct + `blockClass` enum + `renderChatBlock` + `coalesceBlocks` (new code; old `chatLine` still in use).
4. Switch `Model` to use `chatBlock`; drop `markOperatorRelevant` + `filterChatter`.
5. In-place streaming: add `TurnStarted`/`TurnChunk`/`TurnDone` messages and active-block tracking.
6. `agora_return_handler` stops emitting `ChatPanelReply`/`ChatSent` for FinalText.
7. Re-entry divider: `idleTick`, `awaitingReentry`, `blocksDuringIdle`, divider drop on next keystroke.
8. Drop bus-traffic painting: remove `ChatDelivered` + `main.go`'s `p.Send` for chat.deliver.
9. Engine `OnDrop` callback + `SubmissionDropped` + system block render.
10. Disabled textarea at startup + `RegisterSubmit` enables it.
11. `TurnFailed` handling + `/retry` command.
12. Mouse wheel: switch to `WithMouseAllMotion` + `wheel:off` hint.
13. New scroll bindings: `Ctrl-K`/`Ctrl-J`, `Alt-Up`/`Alt-Down`.
14. Timestamp toggle: `Ctrl-G` + `/ts` + header timestamp render.
15. History prefix match.
16. Slash command hints + tab completion.
17. Layout chrome cleanup: drop top divider, update status line format (`since 14:02 · ts:off`).

Each task lands as one commit. Run `go build ./... && go vet ./... && go test ./...` after every task — should be green before moving on.

---

## Task 1: Extract styles.go

**Files:**
- Create: `internal/ui/styles.go`
- Modify: `internal/ui/chat.go` (remove style declarations)

- [ ] **Step 1: Create `internal/ui/styles.go`**

```go
// Package-shared lipgloss.Style declarations. Pulled out of chat.go
// so rendering logic and visual choices can evolve independently.
package ui

import "github.com/charmbracelet/lipgloss"

var (
	dimStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	chatInStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC")).Bold(true)
	chatOutStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
	ttyStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24")).Bold(true)
	notifyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#F472B6")).Bold(true)
	notifyBodyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F472B6"))
	systemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	modelStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399")).Bold(true)
	headerStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1E90FF"))
	dividerStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)
```

- [ ] **Step 2: Remove the `var (...)` style block from `internal/ui/chat.go`**

Delete the entire `var (...)` block (currently lines 91-102 in `chat.go`).

- [ ] **Step 3: Verify build and vet**

Run: `go build ./... && go vet ./...`
Expected: clean output, exit 0.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/styles.go internal/ui/chat.go
git commit -m "refactor(ui): extract lipgloss styles into styles.go"
```

---

## Task 2: Split model.go into model/messages/input/blocks

Pure code move. No behaviour change. Each new file is created with extractions from the current 666-line `model.go`; the rump file is left with only struct, lifecycle, and dispatcher.

**Files:**
- Create: `internal/ui/messages.go`
- Create: `internal/ui/input.go`
- Create: `internal/ui/blocks.go`
- Modify: `internal/ui/model.go` (delete extracted sections, replace `Update` body with dispatcher)

- [ ] **Step 1: Create `internal/ui/messages.go`**

Move all `tea.Msg` type declarations from `model.go` (currently lines ~25-99) into this file.

```go
// All tea.Msg types consumed by the Model. Pulled out of model.go
// so the message contract is scannable in one file. Pure type
// declarations, no behaviour.
package ui

import "time"

type InboxUpdated struct{}

type ChatDelivered struct {
	From       string
	Content    string
	MsgID      int64
	ReceivedAt time.Time
}

type ChatSent struct {
	To   string
	Body string
}

type ChatPanelReply struct {
	Body string
}

type EngineError struct {
	Source string
	Error  string
}

type NotifyOperator struct {
	Body string
}

type ModelChunk struct {
	Text string
}

type ModelTurnEnd struct{}

type ReadyToQuit struct{}

type RegisterSubmit struct {
	OnSubmit func(text string)
	InboxLen func() int
}

type wsTick struct{}

const wsTickInterval = 1500 * time.Millisecond
```

- [ ] **Step 2: Create `internal/ui/blocks.go`**

Move from `model.go`: `appendChat`, `refreshChatContent`, `chatHeight`, `renderStatus`, `max` helper. Move `chatLine` ring helpers if any are model-private. (`appendChatLine` stays in `chat.go` since it's chatLine-typed plumbing.)

```go
// Block lifecycle, chat-region layout helpers, and status-line
// render. Pulled out of model.go so the Model struct + bubbletea
// lifecycle stay focused. All methods are pointer-receivers on Model.
package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *Model) appendChat(line chatLine) {
	line.operatorRelevant = markOperatorRelevant(line.class, line.from, line.body, m.cfg.OperatorName)
	m.chat = appendChatLine(m.chat, line, m.cfg.HistoryDepth)
	m.refreshChatContent(false)
}

func (m *Model) refreshChatContent(forceBottom bool) {
	if !m.vpReady {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(renderChatContent(m.chat, m.vp.Width, m.filterChatter))
	if forceBottom || atBottom {
		m.vp.GotoBottom()
		m.unreadBelow = 0
	} else {
		m.unreadBelow++
	}
}

func (m Model) chatHeight() int {
	inputLines := 1
	if h := m.input.Height(); h > 0 {
		inputLines = h
	}
	chrome := 3 + inputLines
	if m.liveLine != "" {
		chrome++
	}
	h := m.height - chrome
	if h < 1 {
		h = 1
	}
	return h
}

func (m Model) renderStatus() string {
	left := headerStyle.Render(fmt.Sprintf("agora · %s", m.cfg.AspectID))
	wsState := "offline"
	if m.wsConnected {
		wsState = "online"
	}
	rightParts := []string{fmt.Sprintf("ws:%s · inbox:%d", wsState, m.inboxDepth)}
	if m.vpReady && !m.vp.AtBottom() && m.unreadBelow > 0 {
		rightParts = append(rightParts, fmt.Sprintf("↓ %d below (Ctrl-E)", m.unreadBelow))
	}
	if !m.filterChatter {
		rightParts = append(rightParts, "all-chat (Ctrl-T)")
	}
	right := dimStyle.Render(strings.Join(rightParts, " · "))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
```

(Renamed `max` → `maxInt` because Go 1.21+ has a `max` builtin; keep ours under a non-shadowing name.)

- [ ] **Step 3: Create `internal/ui/input.go`**

Move keystroke handling (the big `case tea.KeyMsg` switch from `Update`), `historyBack`, `historyForward`, `resizeInputForContent`.

```go
// Keystroke handling, input-history recall, and textarea resize.
// Pulled out of model.go's giant Update switch. Each handler returns
// the (mutated) Model + any tea.Cmd; the Update dispatcher in
// model.go routes tea.KeyMsg to handleKey.
package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.quitting {
			return m, tea.Quit
		}
		m.appendChat(chatLine{
			class: classSystem,
			when:  time.Now(),
			body:  "ctrl-c — deregistering... (press again to force exit)",
		})
		return m, func() tea.Msg { return QuitGraceful{} }
	case "enter":
		text := strings.TrimRight(m.input.Value(), " \t\n")
		if text == "" {
			return m, nil
		}
		m.input.SetValue("")
		m.input.SetHeight(1)
		if n := len(m.inputHistory); n == 0 || m.inputHistory[n-1] != text {
			m.inputHistory = append(m.inputHistory, text)
			if limit := m.cfg.InputHistory; limit > 0 && len(m.inputHistory) > limit {
				m.inputHistory = m.inputHistory[len(m.inputHistory)-limit:]
			}
		}
		m.historyIdx = -1
		m.draftSnapshot = ""
		if cmd, handled := dispatchCommand(&m, text); handled {
			return m, cmd
		}
		m.appendChat(chatLine{
			class: classTTYIn,
			when:  time.Now(),
			from:  m.cfg.OperatorName,
			body:  text,
		})
		if m.onSubmit != nil {
			m.onSubmit(text)
		}
		return m, nil
	case "pgup", "pgdown", "ctrl+u", "ctrl+d":
		var vpCmd tea.Cmd
		m.vp, vpCmd = m.vp.Update(msg)
		if m.vp.AtBottom() {
			m.unreadBelow = 0
		}
		return m, vpCmd
	case "ctrl+e", "end":
		if m.vpReady {
			m.vp.GotoBottom()
			m.unreadBelow = 0
		}
		return m, nil
	case "ctrl+t":
		m.filterChatter = !m.filterChatter
		m.refreshChatContent(false)
		return m, nil
	case "ctrl+a", "home":
		if m.vpReady {
			m.vp.GotoTop()
		}
		return m, nil
	}
	switch msg.String() {
	case "up":
		if m.input.Line() > 0 {
			break
		}
		if len(m.inputHistory) > 0 {
			m.historyBack()
			return m, nil
		}
		if m.input.Value() == "" {
			var vpCmd tea.Cmd
			m.vp, vpCmd = m.vp.Update(msg)
			return m, vpCmd
		}
	case "down":
		if m.input.LineCount() > 1 && m.input.Line() < m.input.LineCount()-1 {
			break
		}
		if m.historyIdx != -1 {
			m.historyForward()
			return m, nil
		}
		if m.input.Value() == "" {
			var vpCmd tea.Cmd
			m.vp, vpCmd = m.vp.Update(msg)
			return m, vpCmd
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.resizeInputForContent()
	return m, cmd
}

func (m *Model) historyBack() {
	if len(m.inputHistory) == 0 {
		return
	}
	if m.historyIdx == -1 {
		m.draftSnapshot = m.input.Value()
		m.historyIdx = len(m.inputHistory) - 1
	} else if m.historyIdx > 0 {
		m.historyIdx--
	} else {
		return
	}
	m.input.SetValue(m.inputHistory[m.historyIdx])
	m.input.CursorEnd()
}

func (m *Model) historyForward() {
	if m.historyIdx == -1 {
		return
	}
	if m.historyIdx+1 >= len(m.inputHistory) {
		m.historyIdx = -1
		m.input.SetValue(m.draftSnapshot)
		m.draftSnapshot = ""
		m.input.CursorEnd()
		return
	}
	m.historyIdx++
	m.input.SetValue(m.inputHistory[m.historyIdx])
	m.input.CursorEnd()
}

func (m *Model) resizeInputForContent() {
	const maxInputLines = 6
	lines := m.input.LineCount()
	if lines < 1 {
		lines = 1
	}
	if lines > maxInputLines {
		lines = maxInputLines
	}
	if m.input.Height() != lines {
		m.input.SetHeight(lines)
	}
}
```

- [ ] **Step 4: Trim `internal/ui/model.go` to its core**

Replace the file's contents with this (keeps the struct, `NewModel`, `Init`, `Update` dispatcher, `View`):

```go
// Package ui holds the bubbletea Model/Update/View for agora.
//
// model.go is intentionally small: it holds the Model struct and the
// bubbletea lifecycle (NewModel, Init, Update dispatcher, View).
// Per-message handlers live in:
//   - input.go    keystroke handling, history
//   - blocks.go   block lifecycle, status render
//   - messages.go tea.Msg type declarations
//   - chat.go     block rendering, styles
package ui

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type Config struct {
	AspectID     string
	OperatorName string
	HistoryDepth int
	InputHistory int
	Logger       *slog.Logger
	WSConnected  func() bool
}

type Model struct {
	cfg Config

	width  int
	height int

	chat        []chatLine
	input       textarea.Model
	vp          viewport.Model
	vpReady     bool
	inboxDepth  int
	wsConnected bool
	unreadBelow int

	filterChatter bool

	onSubmit func(text string)
	inboxLen func() int

	inputHistory  []string
	historyIdx    int
	draftSnapshot string

	liveLine     string
	streamBuffer string

	quitting bool

	lastRenderedMsgID int64
}

func NewModel(cfg Config) Model {
	if cfg.HistoryDepth <= 0 {
		cfg.HistoryDepth = 1000
	}
	if cfg.InputHistory <= 0 {
		cfg.InputHistory = 100
	}
	if cfg.OperatorName == "" {
		cfg.OperatorName = "operator"
	}
	if cfg.AspectID == "" {
		cfg.AspectID = "aspect"
	}

	ta := textarea.New()
	ta.Prompt = "› "
	ta.Placeholder = "type to " + cfg.AspectID + "; shift+enter for newline; /exit to quit"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter"),
		key.WithHelp("shift+enter", "insert newline"),
	)
	ta.SetHeight(1)
	ta.Focus()

	return Model{cfg: cfg, input: ta, historyIdx: -1, filterChatter: true}
}

func (m Model) Init() tea.Cmd {
	if m.cfg.Logger != nil {
		m.cfg.Logger.Info("ui model initialized",
			"aspect", m.cfg.AspectID,
			"operator", m.cfg.OperatorName)
	}
	return tea.Batch(
		textarea.Blink,
		tea.Tick(wsTickInterval, func(time.Time) tea.Msg { return wsTick{} }),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(maxInt(0, msg.Width-3))
		chatHeight := m.chatHeight()
		firstSize := !m.vpReady
		if firstSize {
			m.vp = viewport.New(msg.Width, chatHeight)
			m.vpReady = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = chatHeight
		}
		m.refreshChatContent(firstSize)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case InboxUpdated:
		if m.inboxLen != nil {
			m.inboxDepth = m.inboxLen()
		}
		return m, nil
	case QuitGraceful:
		m.quitting = true
		return m, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return ReadyToQuit{} })
	case ReadyToQuit:
		return m, tea.Quit
	case RegisterSubmit:
		m.onSubmit = msg.OnSubmit
		m.inboxLen = msg.InboxLen
		return m, nil
	case wsTick:
		if m.cfg.WSConnected != nil {
			m.wsConnected = m.cfg.WSConnected()
		}
		return m, tea.Tick(wsTickInterval, func(time.Time) tea.Msg { return wsTick{} })
	case ChatDelivered:
		if msg.MsgID <= m.lastRenderedMsgID {
			return m, nil
		}
		m.lastRenderedMsgID = msg.MsgID
		m.appendChat(chatLine{class: classChatIn, when: msg.ReceivedAt, from: msg.From, body: msg.Content})
		return m, nil
	case ChatSent:
		m.appendChat(chatLine{class: classChatOut, when: time.Now(), from: msg.To, body: msg.Body})
		return m, nil
	case ChatPanelReply:
		m.appendChat(chatLine{class: classModel, when: time.Now(), from: m.cfg.AspectID, body: msg.Body})
		return m, nil
	case EngineError:
		m.appendChat(chatLine{class: classSystem, when: time.Now(), body: fmt.Sprintf("engine error (%s): %s", msg.Source, msg.Error)})
		return m, nil
	case NotifyOperator:
		m.appendChat(chatLine{class: classNotify, when: time.Now(), body: msg.Body})
		return m, nil
	case ModelChunk:
		m.streamBuffer += msg.Text
		m.liveLine = renderStreamingLine(m.streamBuffer)
		return m, nil
	case ModelTurnEnd:
		m.streamBuffer = ""
		m.liveLine = ""
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.resizeInputForContent()
	return m, cmd
}

func (m Model) View() string {
	if m.quitting {
		return "agora: shutting down...\n"
	}
	if m.width == 0 || m.height == 0 {
		return "agora: initializing...\n"
	}

	status := m.renderStatus()
	divider := dividerStyle.Render(strings.Repeat("─", m.width))

	liveRow := ""
	if m.liveLine != "" {
		liveRow = modelStyle.Render(m.cfg.AspectID+":") + " " + m.liveLine
		liveRow = wrapLines(liveRow, m.width)
	}

	m.vp.Height = m.chatHeight()
	chatBody := m.vp.View()
	inputRow := m.input.View()

	rows := []string{status, divider, chatBody}
	if liveRow != "" {
		rows = append(rows, liveRow)
	}
	rows = append(rows, divider, inputRow)

	return strings.Join(rows, "\n")
}
```

- [ ] **Step 5: Verify build, vet, and existing tests pass**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: clean output; engine tests still 4/4 PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/
git commit -m "refactor(ui): split model.go into model/messages/input/blocks"
```

---

## Task 3: Add chatBlock + blockClass + renderChatBlock + coalesceBlocks

Introduce the block primitives. Coexist with `chatLine` for now — switch the Model in Task 4.

**Files:**
- Modify: `internal/ui/chat.go` (add types + functions)
- Create: `internal/ui/chat_test.go`

- [ ] **Step 1: Add block types and rendering to `internal/ui/chat.go`**

Append to `internal/ui/chat.go` (after the existing `chatLine` types — don't remove them yet):

```go
// blockClass tags each chatBlock. Drives header colour, body colour,
// and optional state suffix.
type blockClass int

const (
	blockYou            blockClass = iota // operator-typed input echo
	blockAspect                           // aspect reply (panel OR chat mirror)
	blockAspectThinking                   // active streaming; ends at TurnDone
	blockNotify                           // notify-operator content
	blockSystem                           // dropped submission, error, banner
	blockDivider                          // since-you-left, session start
)

// chatBlock is the post-rewrite scrollback primitive. One block per
// turn worth of output; body mutates in place during streaming.
type chatBlock struct {
	class     blockClass
	speaker   string
	body      strings.Builder
	createdAt time.Time
	failed    bool
	failedMsg string // populated when failed=true; renders in header
}

// renderChatBlock produces the styled header rule line + indented body.
// showTS adds "HH:MM" to the right edge of the header rule.
func renderChatBlock(b chatBlock, width int, showTS bool) string {
	headerText := blockHeaderText(b)
	headerStyleFn := blockHeaderStyle(b)

	tsSuffix := ""
	if showTS {
		tsSuffix = " " + dimStyle.Render(b.createdAt.Format("15:04"))
	}

	// header: "<glyph?><speaker><state?> <rule fill> <ts?>"
	leftWidth := lipgloss.Width(headerText)
	rightWidth := lipgloss.Width(tsSuffix)
	ruleWidth := width - leftWidth - rightWidth - 1 // 1 for space between
	if ruleWidth < 3 {
		ruleWidth = 3
	}
	header := headerStyleFn.Render(headerText) + " " + dividerStyle.Render(strings.Repeat("─", ruleWidth)) + tsSuffix

	body := b.body.String()
	if body == "" {
		return header
	}
	bodyStyleFn := blockBodyStyle(b)
	wrapped := wrapLines(body, width-2)
	indented := indentLines(wrapped, "  ")
	return header + "\n" + bodyStyleFn.Render(indented)
}

func blockHeaderText(b chatBlock) string {
	switch b.class {
	case blockYou:
		return "you"
	case blockAspect:
		s := b.speaker
		if b.failed {
			s += " · failed: " + b.failedMsg
		}
		return s
	case blockAspectThinking:
		return b.speaker + " · thinking"
	case blockNotify:
		return "⚡ " + b.speaker
	case blockSystem:
		return "· system"
	case blockDivider:
		return "─── " + b.body.String() + " "
	default:
		return b.speaker
	}
}

func blockHeaderStyle(b chatBlock) lipgloss.Style {
	switch b.class {
	case blockYou:
		return ttyStyle
	case blockAspect:
		if b.failed {
			return lipgloss.NewStyle().Foreground(lipgloss.Color("#F87171")).Bold(true)
		}
		return modelStyle
	case blockAspectThinking:
		return modelStyle.Italic(true)
	case blockNotify:
		return notifyStyle
	case blockSystem:
		return systemStyle
	case blockDivider:
		return dimStyle
	}
	return modelStyle
}

func blockBodyStyle(b chatBlock) lipgloss.Style {
	switch b.class {
	case blockNotify:
		return notifyBodyStyle
	case blockSystem:
		return systemStyle
	}
	return lipgloss.NewStyle()
}

func indentLines(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// coalesceBlocks folds consecutive same-speaker + same-class blocks
// into one (joining bodies with a blank line). Storage stays raw; this
// runs at render time. Divider blocks are never coalesced. Blocks
// with createdAt deltas > 60s also stay separate so distinct events
// remain visible.
func coalesceBlocks(blocks []chatBlock) []chatBlock {
	if len(blocks) == 0 {
		return blocks
	}
	out := make([]chatBlock, 0, len(blocks))
	out = append(out, blocks[0])
	for i := 1; i < len(blocks); i++ {
		cur := blocks[i]
		last := &out[len(out)-1]
		if last.class != cur.class || last.class == blockDivider || last.speaker != cur.speaker {
			out = append(out, cur)
			continue
		}
		if cur.createdAt.Sub(last.createdAt) > 60*time.Second {
			out = append(out, cur)
			continue
		}
		last.body.WriteString("\n\n")
		last.body.WriteString(cur.body.String())
	}
	return out
}

// renderBlockContent renders a slice of blocks at the given width,
// returns one string suitable for viewport.SetContent. Mirrors
// renderChatContent's signature but for blocks.
func renderBlockContent(blocks []chatBlock, width int, showTS bool) string {
	coalesced := coalesceBlocks(blocks)
	parts := make([]string, 0, len(coalesced))
	for _, b := range coalesced {
		parts = append(parts, renderChatBlock(b, width, showTS))
	}
	return strings.Join(parts, "\n\n")
}
```

- [ ] **Step 2: Create `internal/ui/chat_test.go`**

```go
package ui

import (
	"strings"
	"testing"
	"time"
)

func mkBlock(class blockClass, speaker, body string, ts time.Time) chatBlock {
	b := chatBlock{class: class, speaker: speaker, createdAt: ts}
	b.body.WriteString(body)
	return b
}

func TestRenderChatBlock_YouHeaderAndBody(t *testing.T) {
	now := time.Date(2026, 5, 23, 14, 32, 0, 0, time.UTC)
	b := mkBlock(blockYou, "you", "ship NEX-92 when keel's done", now)
	out := renderChatBlock(b, 60, false)
	if !strings.Contains(out, "you") {
		t.Fatalf("header missing 'you': %q", out)
	}
	if !strings.Contains(out, "  ship NEX-92 when keel's done") {
		t.Fatalf("body not indented or missing: %q", out)
	}
	if strings.Contains(out, "14:32") {
		t.Fatalf("timestamp leaked with showTS=false: %q", out)
	}
}

func TestRenderChatBlock_TimestampToggle(t *testing.T) {
	now := time.Date(2026, 5, 23, 14, 32, 0, 0, time.UTC)
	b := mkBlock(blockYou, "you", "hi", now)
	out := renderChatBlock(b, 60, true)
	if !strings.Contains(out, "14:32") {
		t.Fatalf("timestamp missing with showTS=true: %q", out)
	}
}

func TestRenderChatBlock_AspectThinkingHeader(t *testing.T) {
	b := mkBlock(blockAspectThinking, "shadow", "tokens streaming", time.Now())
	out := renderChatBlock(b, 60, false)
	if !strings.Contains(out, "shadow · thinking") {
		t.Fatalf("expected 'shadow · thinking' in header: %q", out)
	}
}

func TestRenderChatBlock_NotifyHeader(t *testing.T) {
	b := mkBlock(blockNotify, "shadow", "NEX-87 needs you", time.Now())
	out := renderChatBlock(b, 60, false)
	if !strings.Contains(out, "⚡ shadow") {
		t.Fatalf("expected '⚡ shadow' in header: %q", out)
	}
}

func TestRenderChatBlock_FailedHeader(t *testing.T) {
	b := mkBlock(blockAspect, "shadow", "partial", time.Now())
	b.failed = true
	b.failedMsg = "send timeout"
	out := renderChatBlock(b, 60, false)
	if !strings.Contains(out, "shadow · failed: send timeout") {
		t.Fatalf("expected failed header: %q", out)
	}
}

func TestRenderChatBlock_DividerInline(t *testing.T) {
	b := mkBlock(blockDivider, "", "since you left (2h 14m)", time.Now())
	out := renderChatBlock(b, 60, false)
	if !strings.Contains(out, "since you left (2h 14m)") {
		t.Fatalf("divider missing label: %q", out)
	}
}

func TestCoalesceBlocks_SameSpeakerAdjacentFolds(t *testing.T) {
	now := time.Date(2026, 5, 23, 14, 0, 0, 0, time.UTC)
	blocks := []chatBlock{
		mkBlock(blockAspect, "shadow", "first", now),
		mkBlock(blockAspect, "shadow", "second", now.Add(5*time.Second)),
	}
	out := coalesceBlocks(blocks)
	if len(out) != 1 {
		t.Fatalf("want 1 coalesced block, got %d", len(out))
	}
	body := out[0].body.String()
	if !strings.Contains(body, "first") || !strings.Contains(body, "second") {
		t.Fatalf("coalesced body missing parts: %q", body)
	}
	if !strings.Contains(body, "first\n\nsecond") {
		t.Fatalf("coalesced body not blank-line separated: %q", body)
	}
}

func TestCoalesceBlocks_DifferentSpeakerStaysSeparate(t *testing.T) {
	now := time.Now()
	blocks := []chatBlock{
		mkBlock(blockAspect, "shadow", "a", now),
		mkBlock(blockYou, "you", "b", now),
	}
	out := coalesceBlocks(blocks)
	if len(out) != 2 {
		t.Fatalf("want 2 (different speakers), got %d", len(out))
	}
}

func TestCoalesceBlocks_OverGapStaysSeparate(t *testing.T) {
	now := time.Now()
	blocks := []chatBlock{
		mkBlock(blockAspect, "shadow", "old", now),
		mkBlock(blockAspect, "shadow", "new", now.Add(2*time.Minute)),
	}
	out := coalesceBlocks(blocks)
	if len(out) != 2 {
		t.Fatalf("want 2 (over 60s gap), got %d", len(out))
	}
}

func TestCoalesceBlocks_DividerNeverFolds(t *testing.T) {
	now := time.Now()
	blocks := []chatBlock{
		mkBlock(blockDivider, "", "first divider", now),
		mkBlock(blockDivider, "", "second divider", now.Add(time.Second)),
	}
	out := coalesceBlocks(blocks)
	if len(out) != 2 {
		t.Fatalf("dividers must never fold; want 2 got %d", len(out))
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/ui/... -v -run 'TestRenderChatBlock|TestCoalesceBlocks'`
Expected: all 9 tests PASS.

- [ ] **Step 4: Verify full build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: clean, all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/chat.go internal/ui/chat_test.go
git commit -m "feat(ui): add chatBlock primitives and renderer"
```

---

## Task 4: Switch Model to chatBlock; drop filter + relevance

Replace `chatLine` usage in the Model with `chatBlock`. Remove `markOperatorRelevant`, `filterChatter`, the `Ctrl-T` binding, and `renderChatContent` (block content rendering uses `renderBlockContent` instead).

**Files:**
- Modify: `internal/ui/chat.go` (delete `chatLine`, `chatClass`, `appendChatLine`, `renderChatLine`, `stylePrefixBody`, `renderChatContent`, `markOperatorRelevant`)
- Modify: `internal/ui/model.go` (replace `chat []chatLine` → `blocks []chatBlock`; drop `filterChatter`)
- Modify: `internal/ui/blocks.go` (rewrite `appendChat` → `appendBlock`; rewrite `refreshChatContent`; update `renderStatus` to drop filter indicator)
- Modify: `internal/ui/input.go` (update key handler: drop `ctrl+t`; replace `appendChat(chatLine{...})` calls with block-equivalents)
- Modify: `internal/ui/model.go` Update dispatcher cases that constructed chatLines

- [ ] **Step 1: Delete `chatLine` and friends from `internal/ui/chat.go`**

Remove from `chat.go`:
- `type chatClass int` and the `class*` constants.
- `type chatLine struct`.
- `func appendChatLine`.
- `func renderChatLine`.
- `func stylePrefixBody`.
- `func renderChatContent`.
- `func markOperatorRelevant`.

Keep: `renderStreamingLine` (still used for code-fence buffering inside `renderChatBlock` for thinking blocks; we'll wire that in Task 5), `wrapLines`, all block types from Task 3, and the styles file references.

- [ ] **Step 2: Update `chat.go`'s package doc comment**

Replace the top doc comment with:

```go
// Block-based chat scrollback: types, rendering, fold/coalesce.
// Style declarations live in styles.go. Model-side block lifecycle
// (append, mutate, divider tracking) lives in blocks.go.
package ui
```

- [ ] **Step 3: Update `internal/ui/model.go` Model fields and Update dispatcher**

Replace the Model struct in `model.go` with this version:

```go
type Model struct {
	cfg Config

	width  int
	height int

	blocks      []chatBlock
	input       textarea.Model
	vp          viewport.Model
	vpReady     bool
	inboxDepth  int
	wsConnected bool
	unreadBelow int

	onSubmit func(text string)
	inboxLen func() int

	inputHistory  []string
	historyIdx    int
	draftSnapshot string

	// activeBlockIdx points at the currently-streaming block in m.blocks;
	// -1 when idle. Set on TurnStarted, mutated by TurnChunk, cleared by
	// TurnDone.
	activeBlockIdx int

	quitting bool

	lastRenderedMsgID int64
}
```

Replace `NewModel`'s final return with:

```go
return Model{cfg: cfg, input: ta, historyIdx: -1, activeBlockIdx: -1}
```

(Drops `filterChatter: true`.)

In the `Update` dispatcher, replace these cases:

```go
case ChatDelivered:
    if msg.MsgID <= m.lastRenderedMsgID {
        return m, nil
    }
    m.lastRenderedMsgID = msg.MsgID
    m.appendBlock(chatBlock{
        class:     blockAspect,
        speaker:   msg.From,
        createdAt: msg.ReceivedAt,
    })
    m.blocks[len(m.blocks)-1].body.WriteString(msg.Content)
    m.refreshChatContent(false)
    return m, nil
case ChatSent:
    m.appendBlock(chatBlock{
        class:     blockAspect,
        speaker:   m.cfg.AspectID,
        createdAt: time.Now(),
    })
    m.blocks[len(m.blocks)-1].body.WriteString(msg.Body)
    m.refreshChatContent(false)
    return m, nil
case ChatPanelReply:
    m.appendBlock(chatBlock{
        class:     blockAspect,
        speaker:   m.cfg.AspectID,
        createdAt: time.Now(),
    })
    m.blocks[len(m.blocks)-1].body.WriteString(msg.Body)
    m.refreshChatContent(false)
    return m, nil
case EngineError:
    m.appendBlock(chatBlock{
        class:     blockSystem,
        speaker:   "system",
        createdAt: time.Now(),
    })
    m.blocks[len(m.blocks)-1].body.WriteString(fmt.Sprintf("engine error (%s): %s", msg.Source, msg.Error))
    m.refreshChatContent(false)
    return m, nil
case NotifyOperator:
    m.appendBlock(chatBlock{
        class:     blockNotify,
        speaker:   m.cfg.AspectID,
        createdAt: time.Now(),
    })
    m.blocks[len(m.blocks)-1].body.WriteString(msg.Body)
    m.refreshChatContent(false)
    return m, nil
case ModelChunk:
    // legacy streaming; Task 5 replaces this with TurnChunk/active-block path.
    return m, nil
case ModelTurnEnd:
    return m, nil
```

Drop `m.liveLine`, `m.streamBuffer` fields and remove `liveRow` from `View()`:

```go
func (m Model) View() string {
    if m.quitting {
        return "agora: shutting down...\n"
    }
    if m.width == 0 || m.height == 0 {
        return "agora: initializing...\n"
    }

    status := m.renderStatus()
    divider := dividerStyle.Render(strings.Repeat("─", m.width))

    m.vp.Height = m.chatHeight()
    chatBody := m.vp.View()
    inputRow := m.input.View()

    rows := []string{status, divider, chatBody, divider, inputRow}
    return strings.Join(rows, "\n")
}
```

- [ ] **Step 4: Update `internal/ui/blocks.go`**

Replace `blocks.go` entirely:

```go
// Block lifecycle, chat-region layout helpers, and status-line render.
// All methods are pointer-receivers on Model. Block-class rendering
// lives in chat.go.
package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *Model) appendBlock(b chatBlock) {
	m.blocks = append(m.blocks, b)
	if cap := m.cfg.HistoryDepth; cap > 0 && len(m.blocks) > cap {
		evicted := len(m.blocks) - cap
		m.blocks = m.blocks[evicted:]
		if m.activeBlockIdx >= 0 {
			m.activeBlockIdx -= evicted
			if m.activeBlockIdx < 0 {
				m.activeBlockIdx = -1
			}
		}
	}
}

func (m *Model) refreshChatContent(forceBottom bool) {
	if !m.vpReady {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(renderBlockContent(m.blocks, m.vp.Width, false))
	if forceBottom || atBottom {
		m.vp.GotoBottom()
		m.unreadBelow = 0
	} else {
		m.unreadBelow++
	}
}

func (m Model) chatHeight() int {
	inputLines := 1
	if h := m.input.Height(); h > 0 {
		inputLines = h
	}
	chrome := 3 + inputLines
	h := m.height - chrome
	if h < 1 {
		h = 1
	}
	return h
}

func (m Model) renderStatus() string {
	left := headerStyle.Render(fmt.Sprintf("agora · %s", m.cfg.AspectID))
	wsState := "offline"
	if m.wsConnected {
		wsState = "online"
	}
	rightParts := []string{fmt.Sprintf("ws:%s · inbox:%d", wsState, m.inboxDepth)}
	if m.vpReady && !m.vp.AtBottom() && m.unreadBelow > 0 {
		rightParts = append(rightParts, fmt.Sprintf("↓ %d below (Ctrl-E)", m.unreadBelow))
	}
	right := dimStyle.Render(strings.Join(rightParts, " · "))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
```

- [ ] **Step 5: Update `internal/ui/input.go`**

In `handleKey`, replace:

- `m.appendChat(chatLine{...})` calls with block constructors:

```go
case "ctrl+c":
    if m.quitting {
        return m, tea.Quit
    }
    m.appendBlock(chatBlock{
        class:     blockSystem,
        speaker:   "system",
        createdAt: time.Now(),
    })
    m.blocks[len(m.blocks)-1].body.WriteString("ctrl-c — deregistering... (press again to force exit)")
    m.refreshChatContent(false)
    return m, func() tea.Msg { return QuitGraceful{} }
case "enter":
    text := strings.TrimRight(m.input.Value(), " \t\n")
    if text == "" {
        return m, nil
    }
    m.input.SetValue("")
    m.input.SetHeight(1)
    if n := len(m.inputHistory); n == 0 || m.inputHistory[n-1] != text {
        m.inputHistory = append(m.inputHistory, text)
        if limit := m.cfg.InputHistory; limit > 0 && len(m.inputHistory) > limit {
            m.inputHistory = m.inputHistory[len(m.inputHistory)-limit:]
        }
    }
    m.historyIdx = -1
    m.draftSnapshot = ""
    if cmd, handled := dispatchCommand(&m, text); handled {
        return m, cmd
    }
    m.appendBlock(chatBlock{
        class:     blockYou,
        speaker:   m.cfg.OperatorName,
        createdAt: time.Now(),
    })
    m.blocks[len(m.blocks)-1].body.WriteString(text)
    m.refreshChatContent(false)
    if m.onSubmit != nil {
        m.onSubmit(text)
    }
    return m, nil
```

- Delete the `case "ctrl+t":` arm entirely (filter is gone).

- [ ] **Step 6: Update `commands.go`'s `cmdExit` to use block append**

In `commands.go`, replace the `m.appendChat(chatLine{...})` calls inside `cmdExit`, `cmdHelp`, and the unknown-command/lone-slash paths with the block-append pattern:

```go
// In cmdExit:
m.appendBlock(chatBlock{
    class:     blockSystem,
    speaker:   "system",
    createdAt: time.Now(),
})
m.blocks[len(m.blocks)-1].body.WriteString("exiting — deregistering from nexus...")
m.refreshChatContent(false)

// In cmdHelp (after building the help string into `b`):
m.appendBlock(chatBlock{
    class:     blockSystem,
    speaker:   "system",
    createdAt: time.Now(),
})
m.blocks[len(m.blocks)-1].body.WriteString(b.String())
m.refreshChatContent(false)

// In dispatchCommand's lone-slash and unknown-command paths:
m.appendBlock(chatBlock{
    class:     blockSystem,
    speaker:   "system",
    createdAt: time.Now(),
})
m.blocks[len(m.blocks)-1].body.WriteString("type /help for available commands") // or unknown-command msg
m.refreshChatContent(false)
```

- [ ] **Step 7: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green; chat_test.go from Task 3 still passes (rendering surface unchanged); engine tests still pass.

- [ ] **Step 8: Commit**

```bash
git add internal/ui/ internal/engine/
git commit -m "feat(ui): switch Model to chatBlock; drop filter and relevance"
```

---

## Task 5: In-place streaming — TurnStarted/TurnChunk/TurnDone + active block

**Files:**
- Modify: `internal/ui/messages.go` (add `TurnStarted`, `TurnChunk`, `TurnDone`, `TurnFailed`; deprecate `ModelChunk`, `ModelTurnEnd` — leave types in for one task while engine catches up)
- Modify: `internal/ui/model.go` (add Update cases for TurnStarted/TurnChunk/TurnDone)
- Modify: `internal/ui/blocks.go` (add `appendToActiveBlock`, `finishActiveBlock` helpers)
- Modify: `internal/engine/ui_hook.go` (emit `TurnChunk`/`TurnDone` instead of `ModelChunk`/`ModelTurnEnd`)
- Modify: `internal/engine/agora_return_handler.go` (`OnTurnStart` fires `ui.TurnStarted`)
- Create: `internal/ui/blocks_test.go`

- [ ] **Step 1: Add new message types to `internal/ui/messages.go`**

Append to `messages.go`:

```go
// TurnStarted opens a new streaming block for the next turn.
// Emitted by AgoraReturnHandler.OnTurnStart.
type TurnStarted struct {
	Source string
	MsgID  int64
}

// TurnChunk appends one streamed token's worth of text to the active
// block. Replaces ModelChunk.
type TurnChunk struct {
	Text string
}

// TurnDone finalises the active streaming block. Replaces ModelTurnEnd.
type TurnDone struct{}

// TurnFailed marks the active block as failed; body content stays
// visible, header re-renders with a failure reason.
type TurnFailed struct {
	Reason string
}
```

- [ ] **Step 2: Add block lifecycle helpers to `internal/ui/blocks.go`**

Append:

```go
func (m *Model) appendToActiveBlock(text string) {
	if m.activeBlockIdx < 0 || m.activeBlockIdx >= len(m.blocks) {
		return
	}
	m.blocks[m.activeBlockIdx].body.WriteString(text)
}

func (m *Model) finishActiveBlock() {
	if m.activeBlockIdx < 0 || m.activeBlockIdx >= len(m.blocks) {
		return
	}
	if m.blocks[m.activeBlockIdx].class == blockAspectThinking {
		m.blocks[m.activeBlockIdx].class = blockAspect
	}
	m.activeBlockIdx = -1
}

func (m *Model) markActiveBlockFailed(reason string) {
	if m.activeBlockIdx < 0 || m.activeBlockIdx >= len(m.blocks) {
		return
	}
	m.blocks[m.activeBlockIdx].failed = true
	m.blocks[m.activeBlockIdx].failedMsg = reason
	if m.blocks[m.activeBlockIdx].class == blockAspectThinking {
		m.blocks[m.activeBlockIdx].class = blockAspect
	}
	m.activeBlockIdx = -1
}
```

- [ ] **Step 3: Wire Update cases in `internal/ui/model.go`**

Add cases to the `Update` switch (and remove the no-op `ModelChunk` / `ModelTurnEnd` cases added in Task 4):

```go
case TurnStarted:
    m.appendBlock(chatBlock{
        class:     blockAspectThinking,
        speaker:   m.cfg.AspectID,
        createdAt: time.Now(),
    })
    m.activeBlockIdx = len(m.blocks) - 1
    m.refreshChatContent(false)
    return m, nil
case TurnChunk:
    m.appendToActiveBlock(msg.Text)
    m.refreshChatContent(false)
    return m, nil
case TurnDone:
    m.finishActiveBlock()
    m.refreshChatContent(false)
    return m, nil
case TurnFailed:
    m.markActiveBlockFailed(msg.Reason)
    m.refreshChatContent(false)
    return m, nil
```

- [ ] **Step 4: Update `internal/engine/ui_hook.go`**

Rewrite the file:

```go
package engine

import (
	bridle "github.com/CarriedWorldUniverse/bridle"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/CarriedWorldUniverse/agora/internal/ui"
)

type UIHook struct {
	Program *tea.Program
}

func (h *UIHook) BeginTurn(turnID, label, model, provider string, triggerMsg int64) {
	// TurnStarted is emitted by AgoraReturnHandler.OnTurnStart, which
	// has the source + msg_id context this hook doesn't.
}

func (h *UIHook) OnBridleEvent(ev bridle.Event) {
	switch e := ev.(type) {
	case bridle.ModelChunk:
		h.Program.Send(ui.TurnChunk{Text: e.Text})
	case bridle.TurnDone:
		h.Program.Send(ui.TurnDone{})
	}
}

func (h *UIHook) EndTurn() {}
```

- [ ] **Step 5: Update `internal/engine/agora_return_handler.go` to emit TurnStarted**

In `OnTurnStart`, after the existing log line, add:

```go
if h.Program != nil {
    h.Program.Send(ui.TurnStarted{Source: t.Source, MsgID: t.MsgID})
}
```

- [ ] **Step 6: Create `internal/ui/blocks_test.go`**

```go
package ui

import (
	"strings"
	"testing"
	"time"
)

func TestAppendToActiveBlock_BuildsUpBody(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m.appendBlock(chatBlock{class: blockAspectThinking, speaker: "shadow", createdAt: time.Now()})
	m.activeBlockIdx = len(m.blocks) - 1
	m.appendToActiveBlock("hello ")
	m.appendToActiveBlock("world")
	got := m.blocks[m.activeBlockIdx].body.String()
	if got != "hello world" {
		t.Fatalf("active block body: want %q got %q", "hello world", got)
	}
}

func TestFinishActiveBlock_DemotesThinkingToAspect(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m.appendBlock(chatBlock{class: blockAspectThinking, speaker: "shadow", createdAt: time.Now()})
	m.activeBlockIdx = 0
	m.finishActiveBlock()
	if m.blocks[0].class != blockAspect {
		t.Fatalf("class after finish: want blockAspect got %d", m.blocks[0].class)
	}
	if m.activeBlockIdx != -1 {
		t.Fatalf("activeBlockIdx not cleared: got %d", m.activeBlockIdx)
	}
}

func TestMarkActiveBlockFailed_SetsFlagAndDemotesClass(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m.appendBlock(chatBlock{class: blockAspectThinking, speaker: "shadow", createdAt: time.Now()})
	m.activeBlockIdx = 0
	m.markActiveBlockFailed("send timeout")
	if !m.blocks[0].failed {
		t.Fatalf("failed flag not set")
	}
	if m.blocks[0].failedMsg != "send timeout" {
		t.Fatalf("failedMsg: want %q got %q", "send timeout", m.blocks[0].failedMsg)
	}
	if m.blocks[0].class != blockAspect {
		t.Fatalf("class after fail: want blockAspect got %d", m.blocks[0].class)
	}
}

func TestAppendBlock_EvictsOverHistoryDepth(t *testing.T) {
	m := NewModel(Config{HistoryDepth: 3, AspectID: "shadow"})
	for i := 0; i < 5; i++ {
		b := chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()}
		b.body.WriteString("msg")
		m.appendBlock(b)
	}
	if len(m.blocks) != 3 {
		t.Fatalf("blocks len after evict: want 3 got %d", len(m.blocks))
	}
}

func TestActiveBlockIdx_AdjustsOnEviction(t *testing.T) {
	// HistoryDepth=3; fill the buffer, point active at the tail, then
	// add one more block. Eviction should shift activeBlockIdx down by 1.
	m := NewModel(Config{HistoryDepth: 3, AspectID: "shadow"})
	for i := 0; i < 3; i++ {
		m.appendBlock(chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()})
	}
	m.activeBlockIdx = 2 // tail block
	m.appendBlock(chatBlock{class: blockAspectThinking, speaker: "shadow", createdAt: time.Now()})
	// After eviction of index 0: blocks=[old1, old2, new]. Old active(2) → 1.
	if m.activeBlockIdx != 1 {
		t.Fatalf("activeBlockIdx after eviction: want 1, got %d", m.activeBlockIdx)
	}
	if len(m.blocks) != 3 {
		t.Fatalf("blocks len after eviction: want 3, got %d", len(m.blocks))
	}
}

func TestActiveBlockIdx_ClearsWhenEvictedPastZero(t *testing.T) {
	m := NewModel(Config{HistoryDepth: 2, AspectID: "shadow"})
	m.appendBlock(chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()})
	m.activeBlockIdx = 0 // active is the only block
	m.appendBlock(chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()})
	m.appendBlock(chatBlock{class: blockYou, speaker: "you", createdAt: time.Now()})
	// Two evictions; active was at index 0; should now be -1.
	if m.activeBlockIdx != -1 {
		t.Fatalf("activeBlockIdx after evicting past zero: want -1, got %d", m.activeBlockIdx)
	}
}
```

- [ ] **Step 7: Run tests**

Run: `go test ./internal/ui/... -v -run 'TestAppend|TestFinish|TestMark|TestActive'`
Expected: 6/6 PASS.

- [ ] **Step 8: Verify full build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 9: Commit**

```bash
git add internal/ui/ internal/engine/
git commit -m "feat(ui): in-place streaming via TurnStarted/Chunk/Done active block"
```

---

## Task 6: agora_return_handler stops emitting ChatPanelReply / ChatSent

The streaming block is now the canonical render. `Handle` should NOT also emit `ChatPanelReply` / `ChatSent` for the FinalText — that would re-paint. The wire emission for SourceChat still happens (still calls `bus.SendChat`).

**Files:**
- Modify: `internal/engine/agora_return_handler.go`
- Create: `internal/engine/agora_return_handler_test.go`

- [ ] **Step 1: Update `Handle` in `internal/engine/agora_return_handler.go`**

Replace the entire `Handle` body with:

```go
func (h *AgoraReturnHandler) Handle(ctx context.Context, res funnel.DeliberateResult, t funnel.TurnTrigger) error {
	reply := res.TurnResult.FinalText
	if h.Logger != nil {
		excerpt := reply
		if len(excerpt) > 80 {
			excerpt = excerpt[:80] + "…"
		}
		h.Logger.Info("return handler: handle",
			"source", t.Source,
			"msg_id", t.MsgID,
			"from", t.From,
			"reply_len", len(reply),
			"excerpt", excerpt)
	}

	// Strip notify-operator blocks and surface them; the cleaned reply
	// (if any) is what would have routed. Notifications still fire as
	// distinct blockNotify entries.
	notifications, cleaned := extractNotifyBlocks(reply)
	for _, n := range notifications {
		h.Program.Send(ui.NotifyOperator{Body: n})
	}
	reply = cleaned

	if reply == "" {
		// Nothing to route. Active streaming block (if any) was already
		// finalised by TurnDone. Nothing to do here.
		return nil
	}

	switch t.Source {
	case SourceTTY:
		// Active streaming block carries the panel reply. Nothing
		// additional to send to UI.
		return nil

	case SourceChat, "":
		// Wire emission to nexus chat. UI render already happened via
		// the streaming block; no ChatSent mirror needed.
		if _, err := h.Bus.SendChat(ctx, reply, t.MsgID, ""); err != nil {
			if h.Logger != nil {
				h.Logger.Error("return handler: send chat reply failed",
					"reply_to", t.MsgID,
					"err", err)
			}
			h.Program.Send(ui.TurnFailed{Reason: fmt.Sprintf("send to nexus: %v", err)})
			return err
		}
		return nil

	default:
		if h.Logger != nil {
			h.Logger.Warn("return handler: unknown trigger source — treating as panel-only",
				"source", t.Source)
		}
		return nil
	}
}
```

- [ ] **Step 2: Refactor `AgoraReturnHandler.Bus` to a `busSender` interface (for testability)**

In `internal/engine/agora_return_handler.go`, add an interface and switch the struct field to use it:

```go
// busSender is the subset of bus.Bus that AgoraReturnHandler uses.
// Keeping the dependency interface-typed lets tests inject a fake
// without standing up a websocket.
type busSender interface {
	SendChat(ctx context.Context, content string, replyTo int64, topic string) (int64, error)
}

type AgoraReturnHandler struct {
	Bus     busSender
	Program *tea.Program
	Logger  *slog.Logger
}
```

`*bus.Bus` (concrete) satisfies this interface — its `SendChat` signature matches. `cmd/agora/main.go` already passes `b` (a `*bus.Bus`); no change needed there because Go satisfies interfaces structurally.

- [ ] **Step 3: Create `internal/engine/agora_return_handler_test.go`**

```go
package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/CarriedWorldUniverse/nexus/nexus/frame/funnel"
)

// fakeBus records SendChat invocations and optionally errors.
type fakeBus struct {
	calls    int
	lastBody string
	lastTo   int64
	err      error
}

func (f *fakeBus) SendChat(_ context.Context, content string, replyTo int64, _ string) (int64, error) {
	f.calls++
	f.lastBody = content
	f.lastTo = replyTo
	return 1, f.err
}

func newHandlerWithBus(bus busSender) *AgoraReturnHandler {
	return &AgoraReturnHandler{
		Bus:     bus,
		Program: nil, // tests that don't need UI side-effects pass nil; handler tolerates nil Program
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestHandle_TTYSource_DoesNotCallBusSendChat(t *testing.T) {
	bus := &fakeBus{}
	h := newHandlerWithBus(bus)
	err := h.Handle(context.Background(),
		funnel.DeliberateResult{TurnResult: funnel.TurnResult{FinalText: "panel reply"}},
		funnel.TurnTrigger{Source: SourceTTY, MsgID: 0})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if bus.calls != 0 {
		t.Fatalf("SendChat calls for TTY source: want 0, got %d", bus.calls)
	}
}

func TestHandle_ChatSource_CallsBusSendChat(t *testing.T) {
	bus := &fakeBus{}
	h := newHandlerWithBus(bus)
	err := h.Handle(context.Background(),
		funnel.DeliberateResult{TurnResult: funnel.TurnResult{FinalText: "hello peers"}},
		funnel.TurnTrigger{Source: SourceChat, MsgID: 42})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if bus.calls != 1 {
		t.Fatalf("SendChat calls: want 1, got %d", bus.calls)
	}
	if bus.lastBody != "hello peers" {
		t.Fatalf("SendChat body: want %q, got %q", "hello peers", bus.lastBody)
	}
	if bus.lastTo != 42 {
		t.Fatalf("SendChat replyTo: want 42, got %d", bus.lastTo)
	}
}

func TestHandle_ChatSource_BusErrorReturnsError(t *testing.T) {
	bus := &fakeBus{err: errors.New("broker rejected")}
	h := newHandlerWithBus(bus)
	err := h.Handle(context.Background(),
		funnel.DeliberateResult{TurnResult: funnel.TurnResult{FinalText: "hello"}},
		funnel.TurnTrigger{Source: SourceChat, MsgID: 1})
	if err == nil {
		t.Fatalf("Handle: want error, got nil")
	}
}

func TestHandle_EmptyFinalText_NoOp(t *testing.T) {
	bus := &fakeBus{}
	h := newHandlerWithBus(bus)
	err := h.Handle(context.Background(),
		funnel.DeliberateResult{TurnResult: funnel.TurnResult{FinalText: ""}},
		funnel.TurnTrigger{Source: SourceChat, MsgID: 1})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if bus.calls != 0 {
		t.Fatalf("SendChat calls for empty reply: want 0, got %d", bus.calls)
	}
}
```

- [ ] **Step 4: Guard against nil `Program` in `Handle`**

In the `Handle` body from Step 1, the lines that call `h.Program.Send(...)` need a nil guard (so tests can pass `Program: nil`). Wrap each:

```go
if h.Program != nil {
    h.Program.Send(ui.NotifyOperator{Body: n})
}
```

and similarly for the `ui.TurnFailed{...}` send.

- [ ] **Step 5: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green; 4 new handler tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/engine/
git commit -m "feat(engine): agora_return_handler stops double-emitting FinalText"
```

---

## Task 7: Re-entry divider — idleTick, awaitingReentry, blocksDuringIdle

**Files:**
- Modify: `internal/ui/messages.go` (add `idleTick`)
- Modify: `internal/ui/model.go` (Model fields: `lastInteractionAt`, `idleSince`, `awaitingReentry`, `blocksDuringIdle`; Init schedules `idleTick`; Update handles `idleTick`)
- Modify: `internal/ui/input.go` (every `tea.KeyMsg` updates `lastInteractionAt` and drops divider if pending)
- Modify: `internal/ui/blocks.go` (`appendBlock` increments `blocksDuringIdle` when `awaitingReentry`)
- Modify: `internal/ui/blocks_test.go` (add idle/re-entry tests)

- [ ] **Step 1: Add `idleTick` message and interval const to `internal/ui/messages.go`**

Append:

```go
type idleTick struct{}

const idleTickInterval = 60 * time.Second
const idleThreshold = 5 * time.Minute
```

- [ ] **Step 2: Add fields to Model and schedule idleTick in Init**

In `internal/ui/model.go`, add to Model struct:

```go
lastInteractionAt time.Time
idleSince         time.Time
awaitingReentry   bool
blocksDuringIdle  int
```

In `NewModel`, initialise:

```go
return Model{
    cfg: cfg, input: ta, historyIdx: -1, activeBlockIdx: -1,
    lastInteractionAt: time.Now(),
}
```

In `Init`, add an `idleTick` ticker:

```go
return tea.Batch(
    textarea.Blink,
    tea.Tick(wsTickInterval, func(time.Time) tea.Msg { return wsTick{} }),
    tea.Tick(idleTickInterval, func(time.Time) tea.Msg { return idleTick{} }),
)
```

Add to Update:

```go
case idleTick:
    if !m.awaitingReentry && time.Since(m.lastInteractionAt) >= idleThreshold {
        m.idleSince = m.lastInteractionAt
        m.awaitingReentry = true
        m.blocksDuringIdle = 0
    }
    return m, tea.Tick(idleTickInterval, func(time.Time) tea.Msg { return idleTick{} })
```

- [ ] **Step 3: Update keystroke handler in `internal/ui/input.go`**

At the **top** of `handleKey` (before the switch), insert:

```go
m.markInteraction()
```

Then add the helper to `blocks.go`:

```go
func (m *Model) markInteraction() {
	now := time.Now()
	if m.awaitingReentry && m.blocksDuringIdle > 0 {
		dur := now.Sub(m.idleSince)
		divider := chatBlock{
			class:     blockDivider,
			createdAt: now,
		}
		divider.body.WriteString("since you left (" + formatIdleDuration(dur) + ")")
		// Insert divider BEFORE the new content. The keystroke that
		// triggered this hasn't appended yet; the divider lands at
		// the tail of existing content.
		m.blocks = append(m.blocks, divider)
		if cap := m.cfg.HistoryDepth; cap > 0 && len(m.blocks) > cap {
			m.blocks = m.blocks[len(m.blocks)-cap:]
		}
		m.refreshChatContent(false)
	}
	if m.awaitingReentry {
		m.awaitingReentry = false
		m.blocksDuringIdle = 0
	}
	m.lastInteractionAt = now
}

func formatIdleDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
```

(Make sure `fmt` is imported in `blocks.go` — it already is from `renderStatus`.)

- [ ] **Step 4: Update `appendBlock` in `blocks.go` to track blocksDuringIdle**

Modify `appendBlock`:

```go
func (m *Model) appendBlock(b chatBlock) {
	m.blocks = append(m.blocks, b)
	if m.awaitingReentry && b.class != blockDivider {
		m.blocksDuringIdle++
	}
	if cap := m.cfg.HistoryDepth; cap > 0 && len(m.blocks) > cap {
		evicted := len(m.blocks) - cap
		m.blocks = m.blocks[evicted:]
		if m.activeBlockIdx >= 0 {
			m.activeBlockIdx -= evicted
			if m.activeBlockIdx < 0 {
				m.activeBlockIdx = -1
			}
		}
	}
}
```

- [ ] **Step 5: Add re-entry tests to `blocks_test.go`**

Append:

```go
func TestReentry_DividerDropsOnNextKeystroke(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	// Simulate idle threshold crossed
	m.lastInteractionAt = time.Now().Add(-10 * time.Minute)
	m.awaitingReentry = true
	m.idleSince = m.lastInteractionAt
	// Block lands during idle
	m.appendBlock(chatBlock{class: blockNotify, speaker: "shadow", createdAt: time.Now()})
	if m.blocksDuringIdle != 1 {
		t.Fatalf("blocksDuringIdle after notify: want 1 got %d", m.blocksDuringIdle)
	}
	// Operator keystroke
	m.markInteraction()
	// Find a divider block in m.blocks
	foundDivider := false
	for _, b := range m.blocks {
		if b.class == blockDivider {
			foundDivider = true
			body := b.body.String()
			if !strings.Contains(body, "since you left") {
				t.Fatalf("divider body: want 'since you left' got %q", body)
			}
			break
		}
	}
	if !foundDivider {
		t.Fatalf("divider not appended after keystroke")
	}
	if m.awaitingReentry {
		t.Fatalf("awaitingReentry should be cleared after divider drop")
	}
}

func TestReentry_NoDividerWhenIdleWasSilent(t *testing.T) {
	m := NewModel(Config{AspectID: "shadow"})
	m.lastInteractionAt = time.Now().Add(-10 * time.Minute)
	m.awaitingReentry = true
	m.idleSince = m.lastInteractionAt
	// No blocks during idle
	m.markInteraction()
	for _, b := range m.blocks {
		if b.class == blockDivider {
			t.Fatalf("divider dropped despite silent idle: %v", b)
		}
	}
	if m.awaitingReentry {
		t.Fatalf("awaitingReentry should still clear after keystroke")
	}
}

func TestFormatIdleDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{6 * time.Minute, "6m"},
		{2*time.Hour + 14*time.Minute, "2h 14m"},
		{45 * time.Second, "0m"},
	}
	for _, c := range cases {
		if got := formatIdleDuration(c.d); got != c.want {
			t.Fatalf("formatIdleDuration(%v): want %q got %q", c.d, c.want, got)
		}
	}
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/ui/... -v -run 'TestReentry|TestFormatIdle'`
Expected: 3/3 PASS.

- [ ] **Step 7: Full build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 8: Commit**

```bash
git add internal/ui/
git commit -m "feat(ui): since-you-left divider on re-entry after idle window"
```

---

## Task 8: Drop bus traffic from UI + remove dead message types

After Task 6, `ChatSent` / `ChatPanelReply` / `EngineError` types are dead (no senders). After this task, `ChatDelivered` is dead too. Clean up all four together.

**Files:**
- Modify: `internal/ui/messages.go` (delete `ChatDelivered`, `ChatSent`, `ChatPanelReply`, `EngineError`)
- Modify: `internal/ui/model.go` (delete the four corresponding `case` arms + `lastRenderedMsgID` field)
- Modify: `cmd/agora/main.go` (`onChat` no longer calls `p.Send(ui.ChatDelivered{...})`)

- [ ] **Step 1: Delete dead types from `internal/ui/messages.go`**

Remove the `type ChatDelivered struct { ... }` block. Also remove:
- `type ChatSent struct { ... }`
- `type ChatPanelReply struct { ... }`
- `type EngineError struct { ... }`

These have had no emitter since Task 6 (handler stopped sending) and Task 8 (now no bus.OnChat send either). `TurnFailed` replaces `EngineError` for failure surfacing.

- [ ] **Step 2: Delete the four arms from `Update` in `internal/ui/model.go`**

Remove:
- `case ChatDelivered:` arm
- `case ChatSent:` arm
- `case ChatPanelReply:` arm
- `case EngineError:` arm

Also remove `lastRenderedMsgID` from the Model struct — it was only read by the `ChatDelivered` arm.

- [ ] **Step 3: Update `cmd/agora/main.go`**

In `main.go`, change `onChat` from:

```go
onChat := func(it bus.ChatItem) {
    if p != nil {
        p.Send(ui.ChatDelivered{
            From:       it.From,
            Content:    it.Content,
            MsgID:      it.MsgID,
            ReceivedAt: it.ReceivedAt,
        })
    }
    if eng != nil {
        eng.Receive(bridle.InboxItem{...})
    }
}
```

to:

```go
onChat := func(it bus.ChatItem) {
    // Bus chat.deliver flows to the engine inbox only — UI does not
    // render bus traffic (per spec §4.5). Shadow surfaces what the
    // operator should see via notify_operator.
    if eng != nil {
        eng.Receive(bridle.InboxItem{
            From:       it.From,
            Content:    it.Content,
            MsgID:      it.MsgID,
            ThreadRoot: it.ThreadRoot,
            Source:     engine.SourceChat,
        })
    }
}
```

- [ ] **Step 4: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/ cmd/agora/main.go
git commit -m "feat: bus chat.deliver no longer paints in UI (per spec §4.5)"
```

---

## Task 9: Engine OnDrop callback + SubmissionDropped + system block render

**Files:**
- Modify: `internal/engine/engine.go` (add `Config.OnDrop`; call from `Receive`)
- Modify: `internal/ui/messages.go` (add `SubmissionDropped`)
- Modify: `internal/ui/model.go` (Update handler renders system block)
- Modify: `cmd/agora/main.go` (wire `OnDrop` → `p.Send(ui.SubmissionDropped{...})`)
- Modify: `internal/engine/engine_test.go` (add OnDrop assertion)

- [ ] **Step 1: Add `Config.OnDrop` to `internal/engine/engine.go`**

Update Config:

```go
type Config struct {
	Funnel *funnel.Funnel
	Logger *slog.Logger

	// OnDrop, if set, is invoked when a TTY submission is dropped by
	// the 15-min content-hash dedupe. Lets the UI surface a visible
	// system block explaining the drop. Reason is a short tag for
	// future expansion ("tty-duplicate"); firstSeen is the timestamp
	// of the original submission of the identical content.
	OnDrop func(reason string, firstSeen time.Time)
}
```

In `Receive`, inside the dedupe-hit branch:

```go
if firstSeen, hit := e.ttyHashes[contentHash]; hit && now.Sub(firstSeen) < ttyDedupeWindow {
    e.ttyMu.Unlock()
    if e.cfg.Logger != nil {
        e.cfg.Logger.Info("engine: dropping duplicate tty submission",
            "window", ttyDedupeWindow,
            "since_first", now.Sub(firstSeen),
            "content_sha", contentHash[:12])
    }
    if e.cfg.OnDrop != nil {
        e.cfg.OnDrop("tty-duplicate", firstSeen)
    }
    return
}
```

- [ ] **Step 2: Add `SubmissionDropped` to `internal/ui/messages.go`**

```go
type SubmissionDropped struct {
	Reason    string
	FirstSeen time.Time
}
```

- [ ] **Step 3: Add Update arm to `internal/ui/model.go`**

```go
case SubmissionDropped:
    m.appendBlock(chatBlock{
        class:     blockSystem,
        speaker:   "system",
        createdAt: time.Now(),
    })
    body := "dropped duplicate — same line submitted " + formatAgo(time.Since(msg.FirstSeen)) + " ago"
    if rem := time.Until(msg.FirstSeen.Add(15 * time.Minute)); rem > 0 {
        body += ". Modify the message or wait " + formatAgo(rem) + " more to resend."
    }
    m.blocks[len(m.blocks)-1].body.WriteString(body)
    m.refreshChatContent(false)
    return m, nil
```

Add to `blocks.go`:

```go
func formatAgo(d time.Duration) string {
	m := int(d.Minutes())
	if m < 1 {
		s := int(d.Seconds())
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm", m)
}
```

- [ ] **Step 4: Wire `OnDrop` in `cmd/agora/main.go`**

In `main.go`, when constructing `engine.New`:

```go
eng = engine.New(engine.Config{
    Funnel: f,
    Logger: log,
    OnDrop: func(reason string, firstSeen time.Time) {
        if p != nil {
            p.Send(ui.SubmissionDropped{Reason: reason, FirstSeen: firstSeen})
        }
    },
})
```

- [ ] **Step 5: Extend `internal/engine/engine_test.go`**

Add:

```go
func TestReceive_TTYDedupeHitFiresOnDrop(t *testing.T) {
	var (
		gotReason    string
		gotFirstSeen time.Time
		gotCalls     int
	)
	cb := func(reason string, firstSeen time.Time) {
		gotCalls++
		gotReason = reason
		gotFirstSeen = firstSeen
	}
	f, err := funnel.New(funnel.Config{
		AspectID:     "test",
		AspectHome:   t.TempDir(),
		SystemPrompt: "test",
		Harness:      bridle.NewHarness(stubProvider{}),
		Provider:     "stub",
		Model:        "stub",
		ContextMode:  funnel.ContextStateless,
		Return:       funnel.NoopReturnHandler{},
		Runner:       funnel.NullRunner{},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("funnel.New: %v", err)
	}
	e := New(Config{
		Funnel: f,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnDrop: cb,
	})

	item := bridle.InboxItem{From: "operator", Content: "dup-test", Source: SourceTTY}
	e.Receive(item) // first — accepted
	if gotCalls != 0 {
		t.Fatalf("OnDrop fired on first Receive: %d calls", gotCalls)
	}
	e.Receive(item) // duplicate — dropped, OnDrop fires
	if gotCalls != 1 {
		t.Fatalf("OnDrop calls: want 1 got %d", gotCalls)
	}
	if gotReason != "tty-duplicate" {
		t.Fatalf("OnDrop reason: want tty-duplicate got %q", gotReason)
	}
	if gotFirstSeen.IsZero() {
		t.Fatalf("OnDrop firstSeen was zero")
	}
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/engine/... -v -run 'TestReceive_TTYDedupeHitFiresOnDrop'`
Expected: PASS.

- [ ] **Step 7: Full build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 8: Commit**

```bash
git add internal/engine/ internal/ui/ cmd/agora/main.go
git commit -m "feat: visible system block on TTY dedupe drop (engine OnDrop callback)"
```

---

## Task 10: Disabled textarea at startup + RegisterSubmit enables it

**Files:**
- Modify: `internal/ui/model.go` (textarea starts blurred + placeholder swap)
- Modify: `internal/ui/messages.go` (no new types — RegisterSubmit already exists)

- [ ] **Step 1: Add `textareaEnabled` to Model and adjust NewModel**

In `internal/ui/model.go`, add to Model:

```go
textareaEnabled bool
```

In `NewModel`, change the textarea setup:

```go
ta := textarea.New()
ta.Prompt = "› "
ta.Placeholder = "agora starting…"
ta.ShowLineNumbers = false
ta.CharLimit = 0
ta.KeyMap.InsertNewline = key.NewBinding(
    key.WithKeys("shift+enter", "alt+enter"),
    key.WithHelp("shift+enter", "insert newline"),
)
ta.SetHeight(1)
ta.Blur() // disabled until RegisterSubmit

return Model{
    cfg: cfg, input: ta, historyIdx: -1, activeBlockIdx: -1,
    lastInteractionAt: time.Now(),
    textareaEnabled:   false,
}
```

- [ ] **Step 2: Update `RegisterSubmit` arm to enable textarea**

In `Update`:

```go
case RegisterSubmit:
    m.onSubmit = msg.OnSubmit
    m.inboxLen = msg.InboxLen
    m.textareaEnabled = true
    m.input.Placeholder = "type to " + m.cfg.AspectID + "; shift+enter for newline; /exit to quit"
    m.input.Focus()
    return m, nil
```

- [ ] **Step 3: In `handleKey`, ignore keystrokes when textarea is disabled (except Ctrl-C)**

At the very top of `handleKey`, before `markInteraction`:

```go
if !m.textareaEnabled {
    if msg.String() == "ctrl+c" {
        return m, tea.Quit
    }
    return m, nil
}
```

- [ ] **Step 4: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/
git commit -m "feat(ui): disable textarea until RegisterSubmit (kills startup-race silent drop)"
```

---

## Task 11: TurnFailed handling + /retry command

**Files:**
- Modify: `internal/ui/commands.go` (add `/retry`)
- Modify: `internal/ui/model.go` (track last triggering submission for /retry)
- Modify: `internal/ui/input.go` (capture last submitted text on Enter)

- [ ] **Step 1: Add `lastSubmitted string` field to Model**

In `model.go` Model struct:

```go
lastSubmitted string // captured on Enter; used by /retry
```

- [ ] **Step 2: Update `handleKey`'s Enter path in `input.go`**

After `text := strings.TrimRight(...)` and the empty check, before the history append:

```go
m.lastSubmitted = text
```

- [ ] **Step 3: Add `/retry` to `internal/ui/commands.go`**

Add to the `commands()` registry:

```go
{
    name:    "retry",
    help:    "re-run the last submitted message",
    handler: cmdRetry,
},
```

Add the handler:

```go
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
	m.appendBlock(chatBlock{
		class:     blockYou,
		speaker:   m.cfg.OperatorName,
		createdAt: time.Now(),
	})
	m.blocks[len(m.blocks)-1].body.WriteString(m.lastSubmitted)
	m.refreshChatContent(false)
	if m.onSubmit != nil {
		m.onSubmit(m.lastSubmitted)
	}
	return nil
}
```

- [ ] **Step 4: After TurnFailed, append a system hint**

In `model.go` `Update`, update the TurnFailed arm:

```go
case TurnFailed:
    m.markActiveBlockFailed(msg.Reason)
    m.appendBlock(chatBlock{
        class:     blockSystem,
        speaker:   "system",
        createdAt: time.Now(),
    })
    m.blocks[len(m.blocks)-1].body.WriteString("/retry to re-run this turn")
    m.refreshChatContent(false)
    return m, nil
```

- [ ] **Step 5: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/
git commit -m "feat(ui): /retry command + TurnFailed hint banner"
```

---

## Task 12: Mouse wheel — switch to WithMouseAllMotion + wheel:off hint

**Files:**
- Modify: `cmd/agora/main.go` (swap `WithMouseCellMotion` → `WithMouseAllMotion`)
- Modify: `internal/ui/model.go` (track wheelObserved; emit `wheel:off` in status if not seen within 30s)

- [ ] **Step 1: Swap mouse mode in `cmd/agora/main.go`**

Change:

```go
p = tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
```

to:

```go
p = tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseAllMotion())
```

- [ ] **Step 2: Add `wheelObserved` tracking to Model**

In Model struct (`model.go`):

```go
wheelObserved    bool
wheelCheckExpiry time.Time
```

In `NewModel` return value:

```go
return Model{
    cfg: cfg, input: ta, historyIdx: -1, activeBlockIdx: -1,
    lastInteractionAt: time.Now(),
    wheelCheckExpiry:  time.Now().Add(30 * time.Second),
}
```

- [ ] **Step 3: Handle `tea.MouseMsg` in Update**

Add a case to `Update`:

```go
case tea.MouseMsg:
    if msg.Type == tea.MouseWheelUp || msg.Type == tea.MouseWheelDown {
        m.wheelObserved = true
    }
    var vpCmd tea.Cmd
    m.vp, vpCmd = m.vp.Update(msg)
    if m.vp.AtBottom() {
        m.unreadBelow = 0
    }
    return m, vpCmd
```

- [ ] **Step 4: Update `renderStatus` to show wheel state after 30s**

In `blocks.go` `renderStatus`, add to `rightParts`:

```go
if !m.wheelObserved && time.Now().After(m.wheelCheckExpiry) {
    rightParts = append(rightParts, "wheel:off")
}
```

- [ ] **Step 5: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add cmd/agora/main.go internal/ui/
git commit -m "feat(ui): WithMouseAllMotion + wheel:off hint when wheel not captured"
```

---

## Task 13: New scroll bindings — Ctrl-K/J, Alt-Up/Down

**Files:**
- Modify: `internal/ui/input.go`

- [ ] **Step 1: Add line-scroll bindings to `handleKey`**

Insert these cases in the first switch in `handleKey` (before the catch-all that delegates to the textarea):

```go
case "ctrl+k":
    if m.input.Value() == "" && m.vpReady {
        m.vp.LineUp(1)
        if m.vp.AtBottom() {
            m.unreadBelow = 0
        }
    }
    return m, nil
case "ctrl+j":
    if m.input.Value() == "" && m.vpReady {
        m.vp.LineDown(1)
        if m.vp.AtBottom() {
            m.unreadBelow = 0
        }
    }
    return m, nil
case "alt+up":
    if m.vpReady {
        m.vp.LineUp(1)
        if m.vp.AtBottom() {
            m.unreadBelow = 0
        }
    }
    return m, nil
case "alt+down":
    if m.vpReady {
        m.vp.LineDown(1)
        if m.vp.AtBottom() {
            m.unreadBelow = 0
        }
    }
    return m, nil
```

- [ ] **Step 2: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 3: Commit**

```bash
git add internal/ui/
git commit -m "feat(ui): Ctrl-K/J (empty input) + Alt-Up/Down (always) line scroll"
```

---

## Task 14: Timestamp toggle — Ctrl-G + /ts + render flag

**Files:**
- Modify: `internal/ui/model.go` (add `showTimestamps` field)
- Modify: `internal/ui/input.go` (add `ctrl+g` arm)
- Modify: `internal/ui/blocks.go` (`refreshChatContent` passes `m.showTimestamps`; `renderStatus` shows `ts:on|off`)
- Modify: `internal/ui/commands.go` (add `/ts`)

- [ ] **Step 1: Add field**

In Model struct:

```go
showTimestamps bool
```

- [ ] **Step 2: Add ctrl+g arm in `input.go`**

```go
case "ctrl+g":
    m.showTimestamps = !m.showTimestamps
    m.refreshChatContent(false)
    return m, nil
```

- [ ] **Step 3: Update `refreshChatContent` to pass timestamp flag**

In `blocks.go`:

```go
func (m *Model) refreshChatContent(forceBottom bool) {
	if !m.vpReady {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetContent(renderBlockContent(m.blocks, m.vp.Width, m.showTimestamps))
	if forceBottom || atBottom {
		m.vp.GotoBottom()
		m.unreadBelow = 0
	} else {
		m.unreadBelow++
	}
}
```

- [ ] **Step 4: Update `renderStatus` to show ts state**

Add to `rightParts` in `renderStatus`:

```go
tsState := "off"
if m.showTimestamps {
    tsState = "on"
}
rightParts = append(rightParts, "ts:"+tsState)
```

- [ ] **Step 5: Add `/ts` to `commands.go`**

```go
{
    name:    "ts",
    help:    "toggle inline timestamps",
    handler: cmdTS,
},
```

Handler:

```go
func cmdTS(m *Model, _ string) tea.Cmd {
	m.showTimestamps = !m.showTimestamps
	m.refreshChatContent(false)
	return nil
}
```

- [ ] **Step 6: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 7: Commit**

```bash
git add internal/ui/
git commit -m "feat(ui): Ctrl-G + /ts toggle inline timestamps; status shows ts:state"
```

---

## Task 15: History prefix match

Replaces "only when textarea empty" with prefix-aware recall.

**Files:**
- Modify: `internal/ui/input.go` (new `historyPrefixMatch`; rewire `up`/`down` arms)

- [ ] **Step 1: Add `historyPrefixMatch` helper**

In `input.go`:

```go
// historyPrefixMatch returns history entries (newest first) whose
// first line starts with the given prefix. Empty prefix returns all
// entries newest first.
func (m *Model) historyPrefixMatch(prefix string) []string {
	var out []string
	for i := len(m.inputHistory) - 1; i >= 0; i-- {
		entry := m.inputHistory[i]
		firstLine := entry
		if nl := strings.Index(entry, "\n"); nl >= 0 {
			firstLine = entry[:nl]
		}
		if prefix == "" || strings.HasPrefix(firstLine, prefix) {
			out = append(out, entry)
		}
	}
	return out
}
```

- [ ] **Step 2: Rewrite `up`/`down` arms in `handleKey`**

Replace the existing arrow-key arms with:

```go
case "up":
    if m.input.Line() > 0 {
        break
    }
    cur := m.input.Value()
    matches := m.historyPrefixMatch(cur)
    if len(matches) > 0 {
        m.historyBackPrefix(cur, matches)
        return m, nil
    }
    if cur == "" {
        var vpCmd tea.Cmd
        m.vp, vpCmd = m.vp.Update(msg)
        return m, vpCmd
    }
case "down":
    if m.input.LineCount() > 1 && m.input.Line() < m.input.LineCount()-1 {
        break
    }
    if m.historyIdx != -1 {
        m.historyForwardPrefix(m.draftSnapshot)
        return m, nil
    }
    if m.input.Value() == "" {
        var vpCmd tea.Cmd
        m.vp, vpCmd = m.vp.Update(msg)
        return m, vpCmd
    }
```

- [ ] **Step 3: Add prefix-aware history navigation**

Replace `historyBack` / `historyForward` with prefix-aware versions:

```go
func (m *Model) historyBackPrefix(prefix string, matches []string) {
	// matches is newest-first; advance through them
	if m.historyIdx == -1 {
		m.draftSnapshot = prefix
		m.historyIdx = 0
	} else if m.historyIdx+1 < len(matches) {
		m.historyIdx++
	} else {
		return // at oldest
	}
	if m.historyIdx < len(matches) {
		m.input.SetValue(matches[m.historyIdx])
		m.input.CursorEnd()
	}
}

func (m *Model) historyForwardPrefix(draft string) {
	if m.historyIdx == -1 {
		return
	}
	if m.historyIdx > 0 {
		m.historyIdx--
		matches := m.historyPrefixMatch(draft)
		if m.historyIdx < len(matches) {
			m.input.SetValue(matches[m.historyIdx])
			m.input.CursorEnd()
		}
		return
	}
	// past newest match: restore draft
	m.historyIdx = -1
	m.input.SetValue(m.draftSnapshot)
	m.draftSnapshot = ""
	m.input.CursorEnd()
}
```

Delete the old `historyBack` and `historyForward` functions.

- [ ] **Step 4: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/
git commit -m "feat(ui): prefix-match history recall (Up/Down match what you've typed)"
```

---

## Task 16: Slash command hints + tab completion

**Files:**
- Modify: `internal/ui/model.go` (add `slashHint` field; render below input divider in View when set)
- Modify: `internal/ui/input.go` (compute hint on each key; handle Tab when prefix is `/...`)
- Modify: `internal/ui/commands.go` (add `/bus` stub, `commandNames` helper)

- [ ] **Step 1: Add `/bus` stub and commandNames helper to `commands.go`**

Add to registry:

```go
{
    name:    "bus",
    help:    "(not yet implemented) view bus traffic scrollback",
    handler: cmdBus,
},
```

Handlers + helper:

```go
func cmdBus(m *Model, _ string) tea.Cmd {
	m.appendBlock(chatBlock{
		class:     blockSystem,
		speaker:   "system",
		createdAt: time.Now(),
	})
	m.blocks[len(m.blocks)-1].body.WriteString("/bus — not yet implemented (see spec §12)")
	m.refreshChatContent(false)
	return nil
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
```

- [ ] **Step 2: Add `slashHint` field and hint computation**

In Model:

```go
slashHint string
```

In `input.go`, after the existing `handleKey` switch (before the default key-to-textarea path), recompute the hint based on the new textarea contents. Easiest: do it right after the textarea consumes the keystroke. Add a small helper:

```go
func (m *Model) updateSlashHint() {
	v := m.input.Value()
	if !strings.HasPrefix(v, "/") {
		m.slashHint = ""
		return
	}
	prefix := strings.TrimPrefix(v, "/")
	if i := strings.Index(prefix, " "); i >= 0 {
		prefix = prefix[:i]
	}
	matches := []string{}
	for _, name := range commandNames() {
		if strings.HasPrefix(name, prefix) {
			matches = append(matches, "/"+name)
		}
	}
	if len(matches) == 0 {
		m.slashHint = ""
		return
	}
	m.slashHint = "commands: " + strings.Join(matches, " ")
}
```

Call `m.updateSlashHint()` immediately after the textarea consumes any key (end of `handleKey`, just before returning the textarea-update path):

```go
var cmd tea.Cmd
m.input, cmd = m.input.Update(msg)
m.resizeInputForContent()
m.updateSlashHint()
return m, cmd
```

- [ ] **Step 3: Handle Tab in `handleKey` for slash-completion**

Add at the top of `handleKey` (after `markInteraction`, before the main switch):

```go
if msg.Type == tea.KeyTab && strings.HasPrefix(m.input.Value(), "/") {
    prefix := strings.TrimPrefix(m.input.Value(), "/")
    if i := strings.Index(prefix, " "); i >= 0 {
        prefix = prefix[:i]
    }
    matches := []string{}
    for _, name := range commandNames() {
        if strings.HasPrefix(name, prefix) {
            matches = append(matches, name)
        }
    }
    if len(matches) == 1 {
        m.input.SetValue("/" + matches[0])
        m.input.CursorEnd()
        m.updateSlashHint()
    }
    return m, nil
}
```

- [ ] **Step 4: Render `slashHint` below input divider in View**

In `model.go` `View`:

```go
rows := []string{status, divider, chatBody, divider, inputRow}
if m.slashHint != "" {
    rows = append(rows, systemStyle.Render(m.slashHint))
}
return strings.Join(rows, "\n")
```

(Note: this adds one row below the input; chatHeight already accounts for chrome. Acceptable for the hint — it's only visible while typing a slash command.)

- [ ] **Step 5: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/
git commit -m "feat(ui): slash command hint + Tab completion + /bus stub"
```

---

## Task 17: Layout chrome cleanup — drop top divider, `since 14:02` in status

**Files:**
- Modify: `internal/ui/model.go` (View: drop top divider)
- Modify: `internal/ui/blocks.go` (`renderStatus`: add `since HH:MM`)

- [ ] **Step 1: Add `sessionStart` field**

In Model:

```go
sessionStart time.Time
```

In `NewModel`:

```go
return Model{
    cfg: cfg, input: ta, historyIdx: -1, activeBlockIdx: -1,
    lastInteractionAt: time.Now(),
    sessionStart:      time.Now(),
    wheelCheckExpiry:  time.Now().Add(30 * time.Second),
}
```

- [ ] **Step 2: Update `renderStatus` to show session start**

In `blocks.go` `renderStatus`, build `rightParts` like:

```go
rightParts := []string{}
if m.wsConnected {
    rightParts = append(rightParts, "online")
} else {
    rightParts = append(rightParts, "offline")
}
rightParts = append(rightParts, "since "+m.sessionStart.Format("15:04"))
tsState := "off"
if m.showTimestamps {
    tsState = "on"
}
rightParts = append(rightParts, "ts:"+tsState)
if m.vpReady && !m.vp.AtBottom() && m.unreadBelow > 0 {
    rightParts = append(rightParts, fmt.Sprintf("↓ %d below (Ctrl-E)", m.unreadBelow))
}
if !m.wheelObserved && time.Now().After(m.wheelCheckExpiry) {
    rightParts = append(rightParts, "wheel:off")
}
```

Drop the `ws:online · inbox:N` format — `online`/`offline` alone is enough; inbox depth is internal. (Per spec §3.)

- [ ] **Step 3: Drop top divider in `View`**

Replace `View`:

```go
func (m Model) View() string {
    if m.quitting {
        return "agora: shutting down...\n"
    }
    if m.width == 0 || m.height == 0 {
        return "agora: initializing...\n"
    }

    status := m.renderStatus()
    bottomDivider := dividerStyle.Render(strings.Repeat("─", m.width))

    m.vp.Height = m.chatHeight()
    chatBody := m.vp.View()
    inputRow := m.input.View()

    rows := []string{status, "", chatBody, bottomDivider, inputRow}
    if m.slashHint != "" {
        rows = append(rows, systemStyle.Render(m.slashHint))
    }
    return strings.Join(rows, "\n")
}
```

(Status line then a blank row instead of a divider; chat region below; one bottom divider above input.)

- [ ] **Step 4: Update `chatHeight` for new chrome math**

In `blocks.go`:

```go
func (m Model) chatHeight() int {
	inputLines := 1
	if h := m.input.Height(); h > 0 {
		inputLines = h
	}
	// chrome = status(1) + blank(1) + bottomDivider(1) + input(N)
	chrome := 3 + inputLines
	if m.slashHint != "" {
		chrome++
	}
	h := m.height - chrome
	if h < 1 {
		h = 1
	}
	return h
}
```

- [ ] **Step 5: Verify build/vet/test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 6: Manual smoke test**

Build and run agora against plumb:

```bash
cd ~/Source/agora && go build -o /tmp/agora ./cmd/agora
/tmp/agora -keyfile ~/Source/<your-keyfile>.json -log-file /tmp/agora-test.log
```

Verify:
- Status line shows `agora · shadow` left; `online · since 14:02 · ts:off` right.
- Type "hello" + Enter: appears as `you ────` block.
- Shadow's reply streams in-place as `shadow · thinking ────`; on completion the suffix drops.
- Ctrl-G toggles `ts:on|off`.
- /help shows command list as a system block.
- Type `/r` then Tab → completes to `/retry`.

- [ ] **Step 7: Commit**

```bash
git add internal/ui/
git commit -m "feat(ui): layout chrome cleanup — drop top divider, session-start in status"
```

---

## Final verification

After all 17 tasks:

- [ ] Run: `go build ./... && go vet ./... && go test ./...`
  Expected: clean, all tests pass.
- [ ] Run: `gofmt -l internal/ cmd/`
  Expected: empty output (no formatting drift).
- [ ] Read through the diff vs `main`: `git log --oneline main..HEAD` should show 17 commits.
- [ ] Push branch and open PR; reference spec `docs/superpowers/specs/2026-05-23-agora-ui-redesign-design.md`.

PR description should call out the **one user-visible behaviour break**: bus `chat.deliver` no longer paints in scrollback; `/bus` placeholder prints "not yet implemented".
