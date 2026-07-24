package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// This file is the bubbletea lifecycle (§8 build order). It follows §0's
// non-negotiable idea: the transcript is never held in a viewport.Model —
// finalized cells are handed to Printer (tea.Println in production) and
// the Model only ever re-renders (1) the active cell's tail and (2) the
// bottom pane (status row, modal-or-composer, queue preview).

// Printer prints one finalized block of text to the terminal's own
// scrollback and forgets it — the seam over tea.Println so tests can
// capture what would have been printed without a real tty.
type Printer func(text string) tea.Cmd

func teaPrinter(text string) tea.Cmd { return tea.Println(text) }

// Config wires a Model to its backend and (optional) test seams.
type Config struct {
	Backend Backend
	AgentID string
	Model   string
	Theme   Theme
	Printer Printer
	// Now is the model's clock; injectable so status-row rendering tests
	// don't depend on wall-clock time (conventions: "no wall-clock in
	// snapshot paths").
	Now func() time.Time
	// KnownAlias validates a %-override's model alias (§4a). Nil = accept
	// any non-empty alias (used where the caller has no registry to check
	// against yet — see build report on bridle-registry wiring deferral).
	KnownAlias KnownAliasChecker
	// ModelRegistry backs the `/model` command (registry of name -> model
	// id). Nil = load from ~/.agora/models.json (LoadModelRegistry),
	// creating it with defaults if missing; tests inject a fixed registry
	// here instead of touching the real home dir.
	ModelRegistry ModelRegistry
	// ThreadID is the attached thread — `/resume` marks it in its listing.
	ThreadID string
	// ListServers feeds /mcp: returns the operator's configured MCP
	// servers (cmd/agora adapts the internal/mcp .mcp.json loader into
	// []ServerInfo, keeping internal/tui free of an mcp dependency).
	// Nil = not wired on this connection.
	ListServers func() ([]ServerInfo, error)
	// ListPermissions feeds /permissions: the approval grants saved across
	// sessions for this project (cmd/agora adapts the approval package's
	// FileScopeStore, keeping internal/tui free of an approval dependency).
	// Nil = not wired on this connection.
	ListPermissions func() ([]PermissionInfo, error)
	// RevokePermission removes a saved grant, reporting whether one
	// matched. Nil = revoking not available on this connection.
	RevokePermission func(kind, scope, key string) (bool, error)
	// PermissionMode is the approval posture this session resolved to, for
	// /mode and /status. "" = the engine's own default. Supplied by
	// cmd/agora from the same resolver the engine uses, so what the
	// operator is told matches what is enforced.
	PermissionMode string
	// ModeCatalog lists selectable modes as (name, description) pairs for
	// /mode's output. Nil = not wired.
	ModeCatalog func() [][2]string
}

// ThreadLister is the OPTIONAL backend seam behind `/resume` (NEX-798): list
// persisted threads, filtered to a working dir ("" = all). The in-process
// backend implements it over the LocalStore; daemon/ws backends that don't
// implement it degrade to a "not available" message.
type ThreadLister interface {
	ThreadSummaries(wd string) ([]contracts.ThreadMeta, error)
}

// ThreadForker is the OPTIONAL backend seam behind `/fork`: mirrors
// ThreadLister's pattern. seq is the persisted item Seq to fork at (the
// TUI tracks the highest Seq it has seen on the wire, m.lastItemSeq — see
// handleEvent); the in-process backend implements it over the LocalStore's
// Fork (internal/persistence, no copying: the child thread reads through
// the parent up to seq). Daemon/ws backends that don't implement it
// degrade to a "not available" message, same as /resume.
type ThreadForker interface {
	ForkThread(threadID string, seq int64) (newID string, err error)
}

// approvalEntry is one queued approval/question/plan request (§3: "Requests
// queue and interleave with streaming; shown in order").
type approvalEntry struct {
	ID       string
	Kind     contracts.ApprovalKind
	Raw      json.RawMessage
	Question *contracts.QuestionAsked
	Plan     *contracts.PlanArtifact
}

// Model is the bubbletea Model for the lean agora TUI.
type Model struct {
	cfg Config

	width, height int

	composer *Composer
	stream   *StreamState // nil when no turn is in flight

	running bool
	turnID  string
	// spinnerFrame advances on each spinnerTickMsg while a turn runs, and
	// turnStart (from the injectable cfg.Now) feeds the status row's
	// elapsed readout (§5: "spinner + elapsed + Esc to interrupt").
	spinnerFrame int
	spinnerGen   int // increments per turn; stale ticks are dropped (see spinnerTickMsg)
	turnStart    time.Time

	queue []approvalEntry // FIFO of unresolved requests
	// pendingDeny is set when the operator picked a deny/with-feedback
	// option: the modal closes and focus returns to the composer (§3); the
	// next composer Submit sends this deny instead of a user_message.
	pendingDeny    *approvalEntry
	pendingDenyOpt ModalOption
	modalCursor    int

	statusErr string

	// currentModel is the model id applied to every normal user turn
	// (submitComposer), unless a %-override on that turn wins. Set from
	// cfg.Model if non-empty, else the registry's "sonnet" default;
	// changed at runtime via `/model <name>`.
	currentModel string
	// currentProvider is the per-turn bridle provider selection for
	// currentModel's registry entry (nil for a default/subscription model; a
	// ProviderSpec for a local/LiteLLM entry). Applied to every turn alongside
	// currentModel.
	currentProvider *contracts.ProviderSpec

	// currentEffort is the session-level reasoning-effort pin set via
	// `/effort <tier>` (slash.go's runSlashEffort). Empty (the default)
	// means no pin: submitComposer sends no Effort at all on a plain
	// message, and the engine's own configured/hardcoded default applies.
	// A %-override on a single message still wins over this pin, same as
	// it wins over currentModel.
	currentEffort contracts.Effort

	// Session usage totals, accumulated from each turn.completed's usage
	// payload and shown on the idle status row (NEX-794). sessCost prefers the
	// provider-reported per-turn cost (exact, e.g. OpenRouter) and falls back
	// to the turn model's models.json price table; turns with neither
	// contribute tokens but no cost. turnModelID records which model the
	// in-flight turn was sent to, so the fallback prices the RIGHT model even
	// if /model changes mid-turn.
	sessIn, sessOut, sessCached, sessWrite int64
	sessCost                               float64
	haveUsage                              bool
	turnModelID                            string

	// lastAgentMessage is the most recently FINALIZED agent reply's raw text
	// (markdown, as streamed) — the source `/copy` puts on the clipboard.
	// Set from stream.Raw() at turn.completed, before the stream is dropped
	// (interrupted/failed turns leave it unchanged: a partial reply isn't
	// "the last response").
	lastAgentMessage string
	// lastItemSeq is the highest persisted item Seq seen on the wire so far
	// (every item.* event carries one in its ItemRef — sink.go assigns it
	// from the same store.Append call that makes it durable). `/fork` forks
	// the thread at this Seq: "the current latest point" without a second
	// round-trip to ask the store what its own last item is.
	lastItemSeq int64

	// quitting is set when an exit command arrives while a turn is running
	// (NEX-798): the turn is interrupted first (so the engine winds down and
	// the interrupted exchange persists — thread JSONL stays resume-clean),
	// then the terminal event quits the program. quitGraceCmd is the backstop:
	// if the engine never delivers a terminal event (wedged provider), quit
	// anyway after the grace period — the JSONL is still structurally safe
	// (torn-tail healing) even on a hard exit.
	quitting bool
}

// resolveModelForTurn maps a /model name (a registry key) or a raw model id to
// the (model id, provider selection) a turn should carry. A registry key uses
// its entry (id + provider); anything else is a raw id on the default provider.
func (m *Model) resolveModelForTurn(name string) (string, *contracts.ProviderSpec) {
	if entry, ok := m.cfg.ModelRegistry[name]; ok {
		return entry.Model, entry.ProviderSpec()
	}
	return name, nil
}

// NewModel constructs a ready Model. Backend may be nil for pure
// View()/Update() unit tests that never touch the network.
func NewModel(cfg Config) *Model {
	if cfg.Printer == nil {
		cfg.Printer = teaPrinter
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Theme.renderer == nil {
		cfg.Theme = DefaultTheme()
	}
	if cfg.ModelRegistry == nil {
		cfg.ModelRegistry = LoadModelRegistry(userHomeOrDot(), cwdOrDot())
	}
	currentModel := cfg.Model
	var currentProvider *contracts.ProviderSpec
	if entry, known := cfg.ModelRegistry[currentModel]; known {
		// The -model flag names a registry entry: resolve it exactly like
		// /model does — real model id + provider spec. Left unresolved,
		// "agora --model haiku" ran the ALIAS down the claudesdk lane (the
		// claude CLI happens to accept aliases, masking it), pricingFor
		// never matched (no $ on the status row), and "--model kimi" would
		// run "kimi" on the subscription lane with no provider at all.
		currentModel = entry.Model
		currentProvider = entry.ProviderSpec()
	}
	if currentModel == "" {
		// Convenience: if the config defines a "sonnet" name, start on it;
		// otherwise start empty and the engine's default model applies.
		currentModel = cfg.ModelRegistry["sonnet"].Model
	}
	return &Model{cfg: cfg, composer: NewComposer(), currentModel: currentModel, currentProvider: currentProvider}
}

func (m *Model) Init() tea.Cmd {
	if m.cfg.Backend == nil {
		return nil
	}
	return waitForEvent(m.cfg.Backend)
}

// backendEventMsg carries one Event off the backend's channel.
type backendEventMsg struct {
	ev contracts.Event
	ok bool
}

// quitGraceMsg fires quitGrace after an exit-while-running interrupted the
// turn: if the engine's terminal event hasn't quit us by then (wedged
// provider/sidecar), quit anyway — the JSONL is structurally safe regardless
// (torn-tail healing on the next open).
type quitGraceMsg struct{}

// quitGrace is how long an exit-while-running waits for the interrupted
// turn's terminal event before force-quitting. var, not const: tests shrink
// it so executing the tick cmd synchronously doesn't stall the suite.
var quitGrace = 3 * time.Second

func quitGraceCmd() tea.Cmd {
	return tea.Tick(quitGrace, func(time.Time) tea.Msg { return quitGraceMsg{} })
}

// spinnerFrames is the braille cycle the status row animates through while
// a turn runs (§5). Frame index only advances on spinnerTickMsg, so tests
// that never deliver ticks always see frame 0 — deterministic.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerTickMsg is the 100ms heartbeat that animates the status-row
// spinner. It carries the GENERATION that scheduled it: a chain only dies
// when a tick lands while idle, so a chain from turn N can survive into
// turn N+1 (which schedules its own) and double the tick rate — stale-gen
// ticks are dropped instead (review fix on the look-pass).
type spinnerTickMsg struct{ gen int }

func spinnerTick(gen int) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{gen: gen} })
}

func waitForEvent(b Backend) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-b.Events()
		return backendEventMsg{ev: ev, ok: ok}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case quitGraceMsg:
		if m.quitting {
			return m, tea.Quit // engine never delivered the terminal event
		}
		return m, nil
	case backendEventMsg:
		if !msg.ok {
			return m, nil // backend closed; nothing more to pump
		}
		cmds := m.handleEvent(msg.ev)
		cmds = append(cmds, waitForEvent(m.cfg.Backend))
		return m, tea.Batch(cmds...)
	case tea.KeyMsg:
		return m.handleKey(msg)
	case spinnerTickMsg:
		if !m.running || msg.gen != m.spinnerGen {
			return m, nil // turn ended or a stale chain — let it die
		}
		m.spinnerFrame++
		return m, spinnerTick(m.spinnerGen)
	}
	return m, nil
}

func (m *Model) View() string {
	var b strings.Builder
	// The live region is deliberately kept to a STABLE height (queued-count +
	// status + composer/modal) — it must NOT include the in-flight stream tail.
	// Reason (operator-reported "appears then vanishes"): agora renders its
	// transcript to the terminal's own scrollback via the Printer (tea.Println,
	// §0 no-alt-screen), while View() is the ephemeral in-place region. When the
	// tail lived HERE, a reply with no trailing newline sat only in the live
	// region until turn-complete; then m.stream=nil shrank the region (erasing
	// the tail) in the same frame the finalized line was handed to tea.Println —
	// a bubbletea inline-render race the shrink won, so the whole reply flickered
	// and vanished. With the tail out of View(), the live region never shrinks,
	// so Println always lands cleanly above it: complete lines stream to
	// scrollback via Commit as they finish, and the trailing partial line is
	// flushed by Finalize on turn-complete (see handleEvent's delta/completed
	// cases). Slightly less smooth intra-line streaming, but the reply is never
	// lost — reliability over the two-region tail (agora-spec-tui §2 permits this
	// v1 simplification; a race-free live tail is a later refinement).
	if len(m.composer.Queued()) > 0 {
		b.WriteString(m.cfg.Theme.Muted.Render(fmt.Sprintf("(%d message(s) queued)", len(m.composer.Queued()))))
		b.WriteString("\n")
	}
	b.WriteString(m.renderStatusRow())
	b.WriteString("\n")
	if entry := m.activeModal(); entry != nil {
		b.WriteString(m.renderModal(*entry))
		return b.String()
	}
	b.WriteString(m.renderComposer())
	return b.String()
}

func (m *Model) renderStatusRow() string {
	if !m.running {
		if m.statusErr != "" {
			return m.cfg.Theme.Danger.Render("error: " + m.statusErr)
		}
		return m.cfg.Theme.Accent.Render(m.cfg.AgentID) + m.cfg.Theme.Muted.Render(fmt.Sprintf(" · %s%s%s · Esc to quit", m.currentModel, m.effortSegment(), m.usageSegment()))
	}
	if m.quitting {
		return m.cfg.Theme.Muted.Render("⣾ interrupting turn · exiting…")
	}
	frame := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
	elapsed := m.cfg.Now().Sub(m.turnStart).Round(time.Second)
	return m.cfg.Theme.Warning.Render(frame) + m.cfg.Theme.Muted.Render(fmt.Sprintf(" running · %s%s · %s%s · Esc to interrupt", m.currentModel, m.effortSegment(), elapsed, m.usageSegment()))
}

// effortSegment renders " · <tier>" when a session-level /effort pin
// (m.currentEffort) is active, or "" when unset — an unset pin stays
// invisible on the status row rather than printing "default" on every
// idle line.
func (m *Model) effortSegment() string {
	if m.currentEffort == "" {
		return ""
	}
	return " · " + string(m.currentEffort)
}

// usageSegment renders the session's cumulative usage for the status row —
// " · ↑12.3k ↓1.4k · cache 87% · $0.0431" — or "" before any turn has
// completed. Cost is omitted when nothing priced the session's turns (no
// provider-reported cost and no models.json pricing): showing $0.00 would
// misread as "free".
func (m *Model) usageSegment() string {
	if !m.haveUsage {
		return ""
	}
	// The three prompt counts are disjoint (contracts.Usage): total
	// submitted = uncached + cache reads + cache writes. ↑ shows the total
	// (what the model actually received); cache%% is reads over that total.
	// Earlier revisions divided cached by sessIn alone — right for the old
	// OpenAI-inclusive input, wildly over 100%% for the Anthropic lane's
	// disjoint counts.
	totalIn := m.sessIn + m.sessCached + m.sessWrite
	seg := fmt.Sprintf(" · ↑%s ↓%s", humanTokens(totalIn), humanTokens(m.sessOut))
	if totalIn > 0 {
		seg += fmt.Sprintf(" · cache %d%%", m.sessCached*100/totalIn)
	}
	if m.sessCost > 0 {
		seg += " · " + fmtUSD(m.sessCost)
	}
	return seg
}

// recordUsage folds one turn.completed usage payload into the session totals.
// Provider-reported cost (exact, e.g. OpenRouter through litellm) wins; a turn
// reporting no cost is priced from the turn model's models.json table when one
// is configured (the subscription path — notional, ccusage-style).
func (m *Model) recordUsage(payload []byte) {
	var p struct {
		Usage *contracts.Usage `json:"usage"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Usage == nil {
		return
	}
	u := p.Usage
	if u.Input == 0 && u.Output == 0 && u.Cost == 0 {
		return
	}
	m.haveUsage = true
	m.sessIn += u.Input
	m.sessOut += u.Output
	m.sessCached += u.Cached
	m.sessWrite += u.CacheWrite
	switch {
	case u.Cost > 0:
		m.sessCost += u.Cost
	default:
		if pr := m.pricingFor(m.turnModelID); pr != nil {
			m.sessCost += pr.Cost(u.Input, u.Cached, u.CacheWrite, u.Output)
		}
	}
}

// pricingFor finds the price table for a model ID — the registry entry whose
// Model matches (names are aliases; the ID is what the turn actually ran on).
func (m *Model) pricingFor(modelID string) *ModelPricing {
	if modelID == "" {
		return nil
	}
	for _, e := range m.cfg.ModelRegistry {
		if e.Model == modelID && e.Pricing != nil {
			return e.Pricing
		}
	}
	return nil
}

// humanTokens compacts a token count for the one-line status row.
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// fmtUSD keeps small per-session costs readable (sub-cent needs the extra
// places) without over-precision on real money.
func fmtUSD(c float64) string {
	if c < 0.1 {
		return fmt.Sprintf("$%.4f", c)
	}
	return fmt.Sprintf("$%.2f", c)
}

func (m *Model) renderComposer() string {
	prompt := m.cfg.Theme.Accent.Render("❯ ")
	if m.pendingDeny != nil {
		prompt = m.cfg.Theme.Danger.Render("deny, tell the agent what to do differently ❯ ")
	}
	// Render a visible block cursor at Composer.Cursor() (bubbletea hides the
	// real terminal cursor, so mid-line editing needs its own). The rune under
	// the cursor is drawn reverse-video; at end-of-buffer, a reverse space.
	runes := []rune(m.composer.Value())
	cur := m.composer.Cursor()
	if cur < 0 {
		cur = 0
	}
	if cur > len(runes) {
		cur = len(runes)
	}
	left := string(runes[:cur])
	under, right := " ", ""
	if cur < len(runes) {
		if runes[cur] == '\n' {
			// Keep the line break in `right` so the cursor shows as a reverse
			// space at the end of the current visual line, not a swallowed \n.
			right = string(runes[cur:])
		} else {
			under = string(runes[cur])
			right = string(runes[cur+1:])
		}
	}
	return prompt + left + m.cfg.Theme.Selected.Render(under) + right
}

// activeModal returns the front of the queue, or nil if the queue is empty
// or the operator is mid-deny-with-feedback (composer has focus instead).
func (m *Model) activeModal() *approvalEntry {
	if m.pendingDeny != nil || len(m.queue) == 0 {
		return nil
	}
	return &m.queue[0]
}

func (m *Model) renderModal(e approvalEntry) string {
	var b strings.Builder
	switch e.Kind {
	case contracts.KindQuestion:
		b.WriteString(m.cfg.Theme.Header.Render("? " + e.Question.Args.Text))
		b.WriteString("\n")
		for i, opt := range e.Question.Args.Options {
			cursor := "  "
			label := opt.Label
			if i == m.modalCursor {
				cursor = "> "
				label = m.cfg.Theme.Selected.Render(label)
			}
			b.WriteString(cursor + label + "\n")
		}
	case contracts.KindPlan:
		b.WriteString(m.cfg.Theme.Header.Render("Plan review"))
		b.WriteString("\n")
		for _, step := range e.Plan.Steps {
			b.WriteString("  - " + step + "\n")
		}
		m.renderOptionRows(&b, PlanModalOptions(unresolvedIDs(e.Plan.OpenQuestions)))
	default:
		b.WriteString(m.cfg.Theme.Header.Render(fmt.Sprintf("approve %s?", e.Kind)))
		b.WriteString("\n")
		// §3: "Body shows the highlighted command or the diff" — this is
		// the core trust function of the gate (the operator must see WHAT
		// they approve, not just its kind), see finding #2.
		for _, line := range renderApprovalSubject(e, m.cfg.Theme, m.width) {
			b.WriteString(line + "\n")
		}
		m.renderOptionRows(&b, ApprovalModalOptions(e.Kind))
	}
	return b.String()
}

// renderOptionRows renders one modal option list: the highlighted row is
// reverse-video, approve rows tinted Success, deny rows Danger, disabled
// rows Muted — the decision's shape is visible before a keypress. All of it
// strips away under PlainTheme, so golden snapshots stay byte-stable.
func (m *Model) renderOptionRows(b *strings.Builder, opts []ModalOption) {
	for i, opt := range opts {
		cursor := "  "
		if i == m.modalCursor {
			cursor = "> "
		}
		label := opt.Label
		if opt.Disabled {
			label += "  (" + opt.DisabledWhy + ")"
		}
		b.WriteString(cursor + m.optionRowStyle(opt, i == m.modalCursor).Render(label) + "\n")
	}
}

func (m *Model) optionRowStyle(opt ModalOption, selected bool) lipgloss.Style {
	switch {
	case selected:
		return m.cfg.Theme.Selected
	case opt.Disabled:
		return m.cfg.Theme.Muted
	case opt.Decision == contracts.DecisionAllow:
		return m.cfg.Theme.Success
	case opt.Decision == contracts.DecisionDeny:
		return m.cfg.Theme.Danger
	default:
		return m.cfg.Theme.Bold
	}
}

func unresolvedIDs(qs []contracts.QuestionAsked) []string {
	ids := make([]string, len(qs))
	for i, q := range qs {
		ids[i] = q.ID
	}
	return ids
}

// handleEvent routes one backend Event into Model state, returning
// tea.Cmds (Printer calls for finalized content). Kept separate from
// Update's tea.Msg switch so it's directly unit-testable.
func (m *Model) handleEvent(ev contracts.Event) []tea.Cmd {
	// §"the current latest point" for /fork: every item-carrying event names
	// the persisted Seq the store just assigned it (sink.go). Track the max
	// seen so far regardless of event/item type — cheaper than a round-trip
	// to the store when /fork runs, and always current since the last thing
	// this attachment saw.
	if ev.Item != nil && ev.Item.Seq > m.lastItemSeq {
		m.lastItemSeq = ev.Item.Seq
	}
	var cmds []tea.Cmd
	switch ev.Type {
	case contracts.EvThreadStarted:
		cmds = append(cmds, m.cfg.Printer(Cell{Kind: CellSessionHeader, AgentID: m.cfg.AgentID, Model: m.cfg.Model}.Render(m.width, m.cfg.Theme)[0]))
	case contracts.EvTurnStarted:
		// Finalize/flush a prior non-nil stream before replacing it — a
		// double EvTurnStarted (e.g. a retried/duplicated event) must not
		// silently drop a buffered tail that was never committed.
		if m.stream != nil {
			for _, line := range m.stream.Finalize() {
				cmds = append(cmds, m.cfg.Printer(line))
			}
		}
		m.running = true
		m.turnID = ev.TurnID
		m.spinnerFrame = 0
		m.spinnerGen++
		m.turnStart = m.cfg.Now()
		m.stream = NewStreamState()
		m.composer.SetRunning(true)
		cmds = append(cmds, spinnerTick(m.spinnerGen))
	case contracts.EvAgentMessageDelta:
		// The delta text is carried in the "text" field — that is the shape
		// the sink emits (turnengine's itemPayload{Text string `json:"text"`},
		// same as item.completed). Decoding `json:"delta"` here silently
		// dropped EVERY agent token (p.Delta always ""), so a real turn
		// streamed a full response that never reached the transcript — the
		// TUI showed only its status row. The sink and this decoder had been
		// tested in isolation with mismatched field names; see the seam test
		// in stream/agent-delta coverage.
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		if m.stream == nil {
			m.stream = NewStreamState()
		}
		m.stream.Append(p.Text)
		for _, line := range m.stream.Commit() {
			cmds = append(cmds, m.cfg.Printer(line))
		}
	case contracts.EvTurnCompleted, contracts.EvTurnFailed:
		if ev.Type == contracts.EvTurnCompleted {
			m.recordUsage(ev.Payload)
			if m.stream != nil {
				if raw := strings.TrimSpace(m.stream.Raw()); raw != "" {
					m.lastAgentMessage = raw
				}
			}
		}
		if m.stream != nil {
			for _, line := range m.stream.Finalize() {
				cmds = append(cmds, m.cfg.Printer(line))
			}
			m.stream = nil
		}
		m.running = false
		m.composer.SetRunning(false)
		if m.quitting {
			// NEX-798: exit-while-running — the interrupted turn just reached
			// its terminal event (persisted engine-side), so leave now.
			cmds = append(cmds, tea.Quit)
			return cmds
		}
		for _, q := range m.composer.DrainQueued() {
			cmds = append(cmds, m.cfg.Printer(m.cfg.Theme.Accent.Render("›")+" "+q))
		}
	case contracts.EvItemStarted:
		if ev.Item == nil {
			break
		}
		switch ev.Item.Type {
		case contracts.ItemCommandExecution, contracts.ItemFileChange, contracts.ItemMCPToolCall:
			if m.stream != nil {
				for _, line := range m.stream.Finalize() {
					cmds = append(cmds, m.cfg.Printer(line))
				}
				m.stream = nil
			}
			var line string
			switch ev.Item.Type {
			case contracts.ItemCommandExecution:
				var p struct {
					Command string `json:"command"`
				}
				_ = json.Unmarshal(ev.Payload, &p)
				line = m.cfg.Theme.Muted.Render("$ " + p.Command)
			case contracts.ItemFileChange:
				var p struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal(ev.Payload, &p)
				line = m.cfg.Theme.Muted.Render("edit " + p.Path)
			case contracts.ItemMCPToolCall:
				var p struct {
					Tool string `json:"tool"`
				}
				_ = json.Unmarshal(ev.Payload, &p)
				line = m.cfg.Theme.Muted.Render("tool " + p.Tool)
			}
			cmds = append(cmds, m.cfg.Printer(line))
		}
	case contracts.EvItemCompleted:
		if ev.Item == nil {
			break
		}
		switch ev.Item.Type {
		case contracts.ItemCommandExecution, contracts.ItemFileChange, contracts.ItemMCPToolCall:
			var p struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			if p.Error != "" {
				cmds = append(cmds, m.cfg.Printer(m.cfg.Theme.Danger.Render("  ✗ "+p.Error)))
			}
		}
	case contracts.EvApprovalRequested:
		entry, ok := decodeApprovalRequest(ev.Payload)
		if !ok {
			// Fail closed (security): a malformed kind-specific sub-payload
			// must never be queued for View()/renderModal to dereference —
			// mirror EvQuestionAsked's already-safe "drop on decode
			// failure" behavior, but go further and resolve the dangling
			// server-side request with an explicit auto-deny rather than
			// silently dropping it (a stuck-forever pending request is its
			// own kind of failure).
			m.statusErr = fmt.Sprintf("malformed approval request (kind=%q): auto-denied", entry.Kind)
			if entry.ID != "" {
				if c := m.send(autoDenyInput(entry)); c != nil {
					cmds = append(cmds, c)
				}
			}
		} else {
			m.queue = append(m.queue, entry)
		}
	case contracts.EvQuestionAsked:
		var q contracts.QuestionAsked
		if err := json.Unmarshal(ev.Payload, &q); err == nil {
			m.queue = append(m.queue, approvalEntry{ID: q.ID, Kind: contracts.KindQuestion, Question: &q})
		}
	case contracts.EvApprovalResolved, contracts.EvQuestionAnswered:
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		m.removeFromQueue(p.ID)
	case contracts.EvError:
		var p struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		m.statusErr = p.Message
	}
	return cmds
}

// decodeApprovalRequest decodes an EvApprovalRequested payload. The second
// return is false when a kind that renderModal dereferences a typed pointer
// for (question, plan) failed to decode its kind-specific sub-payload — the
// caller must NOT queue such an entry (see the EvApprovalRequested case in
// handleEvent: a nil Question/Plan on a queued entry is exactly the
// nil-deref crash this guards against, mirroring EvQuestionAsked's existing
// "drop on decode failure" safety).
func decodeApprovalRequest(raw json.RawMessage) (approvalEntry, bool) {
	var wire struct {
		ID      string                 `json:"id"`
		Kind    contracts.ApprovalKind `json:"kind"`
		Payload json.RawMessage        `json:"payload"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return approvalEntry{}, false
	}
	entry := approvalEntry{ID: wire.ID, Kind: wire.Kind, Raw: wire.Payload}
	switch wire.Kind {
	case contracts.KindQuestion:
		var q contracts.QuestionAsked
		if err := json.Unmarshal(wire.Payload, &q); err != nil {
			return entry, false
		}
		entry.Question = &q
	case contracts.KindPlan:
		var p contracts.PlanArtifact
		if err := json.Unmarshal(wire.Payload, &p); err != nil {
			return entry, false
		}
		entry.Plan = &p
	}
	return entry, true
}

// autoDenyInput builds the fail-closed response for a malformed approval
// request: a question-shaped request declines to answer (mirroring
// EscQuestionAnswer); every other kind resolves as an explicit deny (the
// same decision Esc sends, agora-spec-tui.md §3's "every exit is an
// explicit decision" — a malformed frame is not an exception).
func autoDenyInput(e approvalEntry) contracts.Input {
	if e.Kind == contracts.KindQuestion {
		return contracts.Input{
			Type:   contracts.InQuestionResponse,
			ID:     e.ID,
			Answer: &contracts.AnswerInput{Text: "(auto-denied: malformed approval payload)"},
		}
	}
	in, _ := ResolveApproval(e.ID, EscDecision(), "malformed approval payload: auto-denied")
	return in
}

func (m *Model) removeFromQueue(id string) {
	if id == "" {
		return
	}
	out := m.queue[:0]
	for _, e := range m.queue {
		if e.ID != id {
			out = append(out, e)
		}
	}
	m.queue = out
	if m.pendingDeny != nil && m.pendingDeny.ID == id {
		m.pendingDeny = nil
	}
	m.modalCursor = 0
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C is the universal quit from ANY state (composer, a pending approval
	// modal, mid-turn). Without this the operator has NO way out but to kill the
	// process: the composer swallows Ctrl+C (it's not a rune and matches no case
	// in the switch below), AND bubbletea's built-in Ctrl+C-quits is overridden
	// because we handle tea.KeyMsg ourselves. tea.Quit tears the engine down
	// cleanly (the backend's Close cancels the Run ctx). Ctrl+D on an EMPTY
	// composer is the same EOF-style exit (a non-empty composer keeps it for a
	// future "delete word" without risking an accidental quit mid-compose).
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "ctrl+d":
		if m.composer.Value() == "" {
			return m, tea.Quit
		}
	}
	if e := m.activeModal(); e != nil {
		return m.handleModalKey(msg, *e)
	}
	if m.pendingDeny != nil && msg.String() == "esc" {
		// §3: "every exit is an explicit decision" — Esc here must not be
		// silently dropped (finding: no "esc" case in the composer key path
		// left the operator trapped, forced to type text + Enter to
		// escape). Cancel the pending deny and return focus to the
		// approval modal; the underlying queue entry was never removed
		// (chooseOption only sets pendingDeny, it does not dequeue), so
		// clearing pendingDeny alone restores activeModal().
		m.pendingDeny = nil
		m.pendingDenyOpt = ModalOption{}
		m.modalCursor = 0
		m.composer.SetValue("")
		return m, nil
	}
	if msg.String() == "esc" && m.running {
		// The running status row promises "Esc to interrupt" — honor it
		// (NEX-798; previously Esc mid-turn did nothing outside modals). The
		// engine cancels the turn; it lands as turn.failed{interrupted} with
		// the exchange persisted, and the session stays live for the next
		// message.
		return m, m.send(contracts.Input{Type: contracts.InInterrupt})
	}
	switch msg.String() {
	case "enter":
		return m, m.submitComposer()
	case "shift+enter", "alt+enter", "ctrl+j":
		// Insert a newline instead of submitting — multi-line input (paste a
		// block, write a structured prompt). Enter alone still submits. Three
		// bindings because terminals disagree on which they emit for
		// "newline-not-submit": modern ones send shift+enter, others need
		// alt+enter or ctrl+j (LF).
		m.composer.InsertText("\n")
	case "backspace":
		m.composer.Backspace()
	case "delete":
		m.composer.Delete()
	case "left":
		m.composer.MoveLeft()
	case "right":
		m.composer.MoveRight()
	case "home", "ctrl+a":
		m.composer.Home()
	case "end", "ctrl+e":
		m.composer.End()
	case "up":
		m.composer.HistoryUp()
	case "down":
		m.composer.HistoryDown()
	default:
		// bubbletea (v1.3.10) delivers the space bar as its OWN key type,
		// tea.KeySpace — NOT tea.KeyRunes — though it still carries Runes
		// ([]rune{' '}). Checking only KeyRunes here silently ate every space:
		// "hello sonnet" became "hellosonnet". Accept KeySpace too; both carry
		// the rune(s) to insert in msg.Runes.
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.composer.InsertText(string(msg.Runes))
		}
	}
	return m, nil
}

func (m *Model) handleModalKey(msg tea.KeyMsg, e approvalEntry) (tea.Model, tea.Cmd) {
	if e.Kind == contracts.KindQuestion {
		return m.handleQuestionModalKey(msg, e)
	}
	opts := m.optionsFor(e)
	switch msg.String() {
	case "up":
		if m.modalCursor > 0 {
			m.modalCursor--
		}
	case "down":
		if m.modalCursor < len(opts)-1 {
			m.modalCursor++
		}
	case "esc":
		return m, m.sendEscForModal(e)
	case "enter":
		if m.modalCursor < len(opts) {
			return m, m.chooseOption(e, opts[m.modalCursor])
		}
	}
	return m, nil
}

// handleQuestionModalKey handles the question card (§3): v1 wires
// single-option selection end-to-end (cursor + Enter → BuildQuestionAnswer
// → send). Multi-select toggling and a composer-routed free-text answer
// are NOT wired into the key-handling loop yet — BuildQuestionAnswer
// itself supports both (approval_test.go covers them directly) but this
// Model doesn't yet offer the UI affordance to reach them (a toggle-select
// keybinding and a "answer with typed text" mode). Documented deferral,
// not a silent stub: a free-text-only question (no Options) currently has
// no Enter action here and can only be Esc'd.
func (m *Model) handleQuestionModalKey(msg tea.KeyMsg, e approvalEntry) (tea.Model, tea.Cmd) {
	n := len(e.Question.Args.Options)
	switch msg.String() {
	case "up":
		if m.modalCursor > 0 {
			m.modalCursor--
		}
	case "down":
		if m.modalCursor < n-1 {
			m.modalCursor++
		}
	case "esc":
		return m, m.sendEscForModal(e)
	case "enter":
		if n == 0 || m.modalCursor >= n {
			return m, nil
		}
		label := e.Question.Args.Options[m.modalCursor].Label
		in, err := BuildQuestionAnswer(e.ID, e.Question.Args, QuestionCardChoice{Selected: []string{label}})
		if err != nil {
			m.statusErr = err.Error()
			return m, nil
		}
		m.removeFromQueue(e.ID)
		return m, m.send(in)
	}
	return m, nil
}

func (m *Model) optionsFor(e approvalEntry) []ModalOption {
	switch e.Kind {
	case contracts.KindPlan:
		return PlanModalOptions(unresolvedIDs(e.Plan.OpenQuestions))
	default:
		return ApprovalModalOptions(e.Kind)
	}
}

func (m *Model) sendEscForModal(e approvalEntry) tea.Cmd {
	m.removeFromQueue(e.ID)
	var in contracts.Input
	if e.Kind == contracts.KindQuestion {
		in = EscQuestionAnswer(e.ID)
	} else {
		in, _ = ResolveApproval(e.ID, EscDecision(), "")
	}
	return m.send(in)
}

func (m *Model) chooseOption(e approvalEntry, opt ModalOption) tea.Cmd {
	if opt.RequiresMessage {
		m.pendingDeny = &e
		m.pendingDenyOpt = opt
		m.modalCursor = 0
		return nil
	}
	var in contracts.Input
	var err error
	if e.Kind == contracts.KindPlan {
		in, err = ResolvePlan(e.ID, opt, "")
	} else {
		in, err = ResolveApproval(e.ID, opt, "")
	}
	if err != nil {
		m.statusErr = err.Error()
		return nil
	}
	m.removeFromQueue(e.ID)
	return m.send(in)
}

// submitComposer handles Enter on the composer: either a pending
// deny-with-feedback response, or (later) a normal user_message /
// %-override / slash command. v1 wires the deny-with-feedback path (tested
// directly) plus a plain user_message send.
// isExitCommand reports whether the submitted composer text is an explicit
// exit slash-command (/quit, /exit, /q — case-insensitive, surrounding space
// tolerated). Deliberately narrow: only the slash-prefixed forms quit, so bare
// words like "exit" remain sendable to the model as ordinary messages.
func isExitCommand(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "/quit", "/exit", "/q":
		return true
	}
	return false
}

// parseModelCommand reports whether text is a `/model` slash command and
// returns its trimmed argument (empty for bare `/model`).
func parseModelCommand(text string) (arg string, ok bool) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	switch {
	case lower == "/model":
		return "", true
	case strings.HasPrefix(lower, "/model "):
		return strings.TrimSpace(trimmed[len("/model "):]), true
	default:
		return "", false
	}
}

// handleModelCommand intercepts `/model` (list) and `/model <name>` (switch)
// before any other composer-submit handling. handled reports whether text
// was a `/model` command at all; when true, the caller must return cmd
// without sending a turn.
func (m *Model) handleModelCommand(text string) (cmd tea.Cmd, handled bool) {
	arg, ok := parseModelCommand(text)
	if !ok {
		return nil, false
	}
	names := m.cfg.ModelRegistry.Names()
	if arg == "" {
		var b strings.Builder
		b.WriteString("available models:")
		for _, name := range names {
			marker := "  "
			if m.cfg.ModelRegistry[name].Model == m.currentModel {
				marker = "* "
			}
			b.WriteString(fmt.Sprintf("\n%s%s -> %s", marker, name, m.cfg.ModelRegistry[name].Model))
		}
		return m.cfg.Printer(b.String()), true
	}
	entry, known := m.cfg.ModelRegistry[arg]
	if !known {
		m.statusErr = fmt.Sprintf("unknown model %q", arg)
		return m.cfg.Printer("available models: " + strings.Join(names, ", ")), true
	}
	m.currentModel = entry.Model
	m.currentProvider = entry.ProviderSpec()
	where := "subscription"
	if entry.BaseURL != "" {
		where = entry.BaseURL
	}
	return m.cfg.Printer(fmt.Sprintf("model set to %s (%s via %s)", arg, entry.Model, where)), true
}

// handleResumeCommand intercepts `/resume` (list this working dir's persisted
// threads) and `/resume all` (every thread) — NEX-798. Listing only in v1:
// switching threads in-place needs a Manager-per-thread rebuild, so the
// listing prints the `agora -thread <id>` relaunch hint instead.
func (m *Model) handleResumeCommand(text string) (cmd tea.Cmd, handled bool) {
	trimmed := strings.ToLower(strings.TrimSpace(text))
	if trimmed != "/resume" && trimmed != "/resume all" {
		return nil, false
	}
	lister, ok := m.cfg.Backend.(ThreadLister)
	if !ok {
		m.statusErr = "resume listing not available on this backend"
		return nil, true
	}
	wd := cwdOrDot()
	scope := "this directory"
	if trimmed == "/resume all" {
		wd, scope = "", "all directories"
	}
	metas, err := lister.ThreadSummaries(wd)
	if err != nil {
		m.statusErr = "list threads: " + err.Error()
		return nil, true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "sessions (%s):", scope)
	if len(metas) == 0 {
		b.WriteString(" none")
	}
	for _, meta := range metas {
		marker := "  "
		if meta.ThreadID == m.cfg.ThreadID {
			marker = "* "
		}
		fmt.Fprintf(&b, "\n%s%-28s %s  %s", marker, meta.ThreadID, meta.CreatedAt.Format("2006-01-02 15:04"), meta.WorkingDir)
	}
	b.WriteString("\nopen one: agora -thread <id>   (in its directory)")
	return m.cfg.Printer(b.String()), true
}

func (m *Model) submitComposer() tea.Cmd {
	if m.pendingDeny != nil {
		text, sent := m.composer.Submit()
		if !sent {
			return nil
		}
		return m.resolvePendingDeny(text)
	}
	// NEX-798: exit commands NEVER queue. Peek the buffer BEFORE Submit —
	// Submit's queue-while-running logic would swallow "/exit" into the
	// message queue and the operator would stay trapped until the turn ended.
	// Idle → quit immediately (engine teardown via backend.Close, as before).
	// Running → interrupt the turn first so it lands as a persisted
	// turn.failed{interrupted} (the thread JSONL stays resume-clean), then the
	// terminal event quits (see handleEvent); quitGraceCmd backstops a wedged
	// engine.
	if isExitCommand(m.composer.Value()) {
		m.composer.SetValue("")
		if !m.running {
			return tea.Quit
		}
		m.quitting = true
		return tea.Batch(m.send(contracts.Input{Type: contracts.InInterrupt}), quitGraceCmd())
	}
	text, sent := m.composer.Submit()
	if !sent {
		return nil
	}
	if cmd, handled := m.handleModelCommand(text); handled {
		return cmd
	}
	if cmd, handled := m.handleResumeCommand(text); handled {
		return cmd
	}
	if isExitCommand(text) {
		// Explicit slash-command exit (/quit, /exit, /q). Bare "quit"/"exit"
		// intentionally still go to the model as a message — only the
		// slash-prefixed forms (and Ctrl+C, see handleKey) quit, so you can
		// still literally say "exit" to Claude without leaving.
		return tea.Quit
	}
	if c, args, ok := slashDispatch(text); ok {
		return c.run(m, args)
	}
	// §6a containment (NEX-795): from here on, "text" starting with "/" is
	// EITHER the escape hatch (backslash-slash — stripped, sent verbatim) OR
	// blocked locally (registry-name sugar dispatches as /model <name>;
	// anything else known-unknown gets a local error, never the model). A
	// leading SPACE is the other escape hatch, and needs no handling here:
	// text starting with " " already fails HasPrefix(text, "/") below and
	// falls straight through to the ordinary message path, verbatim,
	// leading space and all.
	switch {
	case strings.HasPrefix(text, `\/`):
		text = "/" + strings.TrimPrefix(text, `\/`)
	case strings.HasPrefix(text, "/"):
		if cmd, ok := m.dispatchRegistrySugar(text); ok {
			return cmd
		}
		return m.cfg.Printer(m.unknownSlashMessage(text))
	}
	model, effort, rest, isOverride, err := ParseOverride(text, m.cfg.KnownAlias)
	var in contracts.Input
	if isOverride {
		if err != nil {
			m.statusErr = err.Error()
			return nil
		}
		modelID, pspec := m.currentModel, m.currentProvider
		if model != "" {
			// A named %-override wins for this turn — resolve it through the
			// registry so a local/LiteLLM name also carries its provider (a
			// %-override with only effort keeps the current model+provider).
			modelID, pspec = m.resolveModelForTurn(model)
		}
		in = contracts.Input{Type: contracts.InUserMessage, Text: rest, Model: modelID, Effort: effort, Provider: pspec}
	} else {
		in = contracts.Input{Type: contracts.InUserMessage, Text: text, Model: m.currentModel, Effort: m.currentEffort, Provider: m.currentProvider}
	}
	// Remember which model this turn runs on so recordUsage can price it from
	// the right table when the provider reports no cost.
	m.turnModelID = in.Model
	// Echo the operator's OWN message into the transcript (scrollback) before
	// sending — the engine never emits it back (it only persists it + runs the
	// turn) and the session doesn't broadcast inputs, so without this the
	// submitted message just vanishes when the composer clears ("wipes it
	// out"), unlike Claude Code / every chat TUI where your line stays in the
	// history above the reply. Rendered as a CellUserMessage ("› <text>").
	cmds := m.echoUserMessage(in.Text)
	cmds = append(cmds, m.send(in))
	return tea.Batch(cmds...)
}

// echoUserMessage returns Printer cmds that write the operator's just-submitted
// message to the transcript as a "› <text>" user cell. Empty/whitespace-only
// text echoes nothing.
func (m *Model) echoUserMessage(text string) []tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var cmds []tea.Cmd
	for _, line := range (Cell{Kind: CellUserMessage, Text: text}).Render(m.width, m.cfg.Theme) {
		cmds = append(cmds, m.cfg.Printer(line))
	}
	return cmds
}

// resolvePendingDeny builds and sends the deny-with-feedback response for
// m.pendingDeny. It ONLY removes the entry from the queue / clears
// pendingDeny AFTER ResolveApproval succeeds — on error the pending state
// is left intact and the error surfaced, so a would-be error can never
// silently drop the decision (finding: previously removeFromQueue ran
// before the err check; currently unreachable in practice since Submit()
// only returns sent=true for non-empty text and pendingDenyOpt always
// RequiresMessage, but fragile — kept correct defensively, and split out
// so the ordering itself is directly unit-testable without needing to
// coax the composer into an otherwise-unreachable state).
func (m *Model) resolvePendingDeny(text string) tea.Cmd {
	in, err := ResolveApproval(m.pendingDeny.ID, m.pendingDenyOpt, text)
	if err != nil {
		m.statusErr = err.Error()
		return nil
	}
	m.removeFromQueue(m.pendingDeny.ID)
	return m.send(in)
}

func (m *Model) send(in contracts.Input) tea.Cmd {
	if m.cfg.Backend == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.cfg.Backend.Send(ctx, in); err != nil {
			return backendEventMsg{ev: contracts.Event{Type: contracts.EvError, Payload: mustMarshalTUI(errPayload{Message: err.Error()})}, ok: true}
		}
		return nil
	}
}

type errPayload struct {
	Message string `json:"message"`
}

func mustMarshalTUI(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
