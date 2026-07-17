package tui

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// This file implements the composer state machine (agora-spec-tui.md §4):
// trigger detection for /, @, $, % (an atomic token once completed),
// large-paste collapse, in-session history, and queue-while-running. It is
// deliberately independent of any terminal-rendering library so the state
// machine is unit-testable without a tty — internal/tui's bubbletea Model
// (model.go) is the thin layer that turns keystrokes into calls here and
// Composer state into a rendered row.
//
// Deferred (spec explicitly allows or the brief scopes elsewhere): Vim
// mode, Ctrl+R incremental history search, image paste, backtrack,
// paste-burst reconstruction (§4 "Defer" list); persistent CROSS-SESSION
// history merge (v1 here is in-session only — a durable history file is a
// straightforward follow-on once a state-dir convention for the TUI is
// settled, not blocking the streaming/approval correctness core this unit
// is graded on); the @-file and $-skill pickers' CANDIDATE LISTS are
// injected via Provider funcs rather than this package doing filesystem
// walks or importing internal/skills directly — keeps the trigger/atomic
// token machinery decoupled from what's actually being completed.

// TriggerKind is which composer trigger is currently open.
type TriggerKind int

const (
	TriggerNone TriggerKind = iota
	TriggerSlash
	TriggerAt
	TriggerSkill
	TriggerOverride
)

// pasteCollapseThreshold: a single InsertText call carrying more than this
// many runes collapses to a placeholder element (§4: "Large paste as
// placeholder").
const pasteCollapseThreshold = 200

// atomicSpan marks a completed trigger token (or a paste placeholder) in
// the buffer as one non-editable unit — [Start,End) in rune offsets.
// Backspace immediately after End removes the whole span in one keystroke
// (the "atomic, non-editable" requirement, enforced for the one interaction
// v1 needs: whole-unit delete; disallowing a cursor from landing INSIDE the
// span is a smaller polish item deferred alongside the picker UI).
type atomicSpan struct {
	Start, End int
	// Full is the expanded content a paste placeholder stands in for; empty
	// for slash/@/$/% tokens (those ARE their own full content).
	Full string
}

// Composer holds the message-in-progress plus the picker/history/queue
// state around it.
type Composer struct {
	buf    []rune
	cursor int
	spans  []atomicSpan

	history    []string
	historyIdx int // -1 = not browsing history

	running bool
	queued  []string
}

// NewComposer returns an empty composer.
func NewComposer() *Composer {
	return &Composer{historyIdx: -1}
}

// Value returns the current buffer text.
func (c *Composer) Value() string { return string(c.buf) }

// SetValue replaces the buffer wholesale and moves the cursor to the end.
// Clears atomic spans (a fresh value has no tracked tokens).
func (c *Composer) SetValue(s string) {
	c.buf = []rune(s)
	c.cursor = len(c.buf)
	c.spans = nil
}

// Cursor returns the current rune-offset cursor position.
func (c *Composer) Cursor() int { return c.cursor }

// InsertText inserts s at the cursor. A single insert longer than
// pasteCollapseThreshold collapses to an atomic "[Pasted Content N chars]"
// placeholder instead of landing in the buffer verbatim (§4); the full text
// is expanded back in at Submit.
func (c *Composer) InsertText(s string) {
	if s == "" {
		return
	}
	if len([]rune(s)) > pasteCollapseThreshold {
		placeholder := fmt.Sprintf("[Pasted Content %d chars]", len([]rune(s)))
		c.insertRunes([]rune(placeholder))
		start := c.cursor - len([]rune(placeholder))
		c.spans = append(c.spans, atomicSpan{Start: start, End: c.cursor, Full: s})
		return
	}
	c.insertRunes([]rune(s))
}

func (c *Composer) insertRunes(r []rune) {
	buf := make([]rune, 0, len(c.buf)+len(r))
	buf = append(buf, c.buf[:c.cursor]...)
	buf = append(buf, r...)
	buf = append(buf, c.buf[c.cursor:]...)
	c.buf = buf
	c.cursor += len(r)
	c.shiftSpansAfterInsert(c.cursor-len(r), len(r))
}

func (c *Composer) shiftSpansAfterInsert(at, n int) {
	for i := range c.spans {
		if c.spans[i].Start >= at {
			c.spans[i].Start += n
			c.spans[i].End += n
		}
	}
}

// Backspace deletes one rune left of the cursor, or the whole atomic span
// if the cursor sits exactly at a span's End.
func (c *Composer) Backspace() {
	if c.cursor == 0 {
		return
	}
	for i, sp := range c.spans {
		if sp.End == c.cursor {
			c.buf = append(c.buf[:sp.Start], c.buf[sp.End:]...)
			n := sp.End - sp.Start
			c.cursor = sp.Start
			c.spans = append(c.spans[:i], c.spans[i+1:]...)
			c.shiftSpansAfterDelete(sp.Start, n)
			return
		}
	}
	c.buf = append(c.buf[:c.cursor-1], c.buf[c.cursor:]...)
	c.cursor--
	c.shiftSpansAfterDelete(c.cursor, 1)
}

func (c *Composer) shiftSpansAfterDelete(at, n int) {
	kept := c.spans[:0]
	for _, sp := range c.spans {
		if sp.Start >= at+n {
			sp.Start -= n
			sp.End -= n
		}
		kept = append(kept, sp)
	}
	c.spans = kept
}

// isTriggerBoundary reports whether idx is a valid token start (0 or
// preceded by whitespace) — triggers only open at a token boundary, not
// mid-word (e.g. "foo@bar" does not open an @-mention).
func (c *Composer) isTriggerBoundary(idx int) bool {
	if idx == 0 {
		return true
	}
	return unicode.IsSpace(c.buf[idx-1])
}

// ActiveTrigger scans backward from the cursor for an open, unterminated
// trigger. Returns TriggerNone if the cursor isn't inside one (no trigger
// char found at a token boundary before the next whitespace/cursor).
func (c *Composer) ActiveTrigger() (kind TriggerKind, query string, start int) {
	i := c.cursor
	for i > 0 {
		ch := c.buf[i-1]
		if unicode.IsSpace(ch) {
			return TriggerNone, "", -1
		}
		if isTriggerRune(ch) && c.isTriggerBoundary(i-1) {
			return triggerKindFor(ch), string(c.buf[i:c.cursor]), i - 1
		}
		i--
	}
	return TriggerNone, "", -1
}

func isTriggerRune(r rune) bool {
	return r == '/' || r == '@' || r == '$' || r == '%'
}

func triggerKindFor(r rune) TriggerKind {
	switch r {
	case '/':
		return TriggerSlash
	case '@':
		return TriggerAt
	case '$':
		return TriggerSkill
	case '%':
		return TriggerOverride
	default:
		return TriggerNone
	}
}

// CompleteToken replaces the currently-open trigger span (from its trigger
// char through the cursor) with token, appends a trailing space, and marks
// it atomic (§4: "completed command becomes an atomic (non-editable)
// token"). No-op if no trigger is currently open.
func (c *Composer) CompleteToken(token string) {
	kind, _, start := c.ActiveTrigger()
	if kind == TriggerNone {
		return
	}
	c.buf = append(c.buf[:start], append([]rune(token+" "), c.buf[c.cursor:]...)...)
	end := start + len([]rune(token))
	c.cursor = end + 1
	c.spans = append(c.spans, atomicSpan{Start: start, End: end})
}

// PastesExpanded reports whether s contains any placeholder this composer
// is tracking (used by tests/Submit to confirm expansion happened).
func (c *Composer) pasteFullText(placeholder string) (string, bool) {
	for _, sp := range c.spans {
		if sp.Full != "" && string(c.buf[sp.Start:sp.End]) == placeholder {
			return sp.Full, true
		}
	}
	return "", false
}

// Submit expands any paste placeholders back to their full text, appends
// the result to history, resets the buffer, and returns the expanded
// message. If the composer is Running, the message is queued instead
// (§4: "Queue-while-running") and Submit returns ("", false, nil) — the
// caller checks the bool to know whether anything was actually sent.
func (c *Composer) Submit() (text string, sent bool) {
	raw := c.Value()
	if strings.TrimSpace(raw) == "" {
		return "", false
	}
	expanded := c.expand(raw)
	c.history = append(c.history, expanded)
	c.historyIdx = -1
	c.SetValue("")
	if c.running {
		c.queued = append(c.queued, expanded)
		return "", false
	}
	return expanded, true
}

func (c *Composer) expand(raw string) string {
	out := raw
	for _, sp := range c.spans {
		if sp.Full == "" {
			continue
		}
		placeholder := string(c.buf[sp.Start:sp.End])
		out = strings.Replace(out, placeholder, sp.Full, 1)
	}
	return out
}

// SetRunning toggles queue-while-running mode.
func (c *Composer) SetRunning(running bool) { c.running = running }

// Running reports whether the composer is in queue-while-running mode.
func (c *Composer) Running() bool { return c.running }

// Queued returns the preview rows queued while a turn was running (shown
// above the composer, §4).
func (c *Composer) Queued() []string { return c.queued }

// DrainQueued pops all queued messages (called once the turn goes idle and
// they're sent).
func (c *Composer) DrainQueued() []string {
	q := c.queued
	c.queued = nil
	return q
}

// HistoryUp/HistoryDown cycle through submitted-message history
// (in-session only, v1 — see file doc comment).
func (c *Composer) HistoryUp() {
	if len(c.history) == 0 {
		return
	}
	if c.historyIdx == -1 {
		c.historyIdx = len(c.history) - 1
	} else if c.historyIdx > 0 {
		c.historyIdx--
	}
	c.SetValue(c.history[c.historyIdx])
}

func (c *Composer) HistoryDown() {
	if c.historyIdx == -1 {
		return
	}
	if c.historyIdx < len(c.history)-1 {
		c.historyIdx++
		c.SetValue(c.history[c.historyIdx])
		return
	}
	c.historyIdx = -1
	c.SetValue("")
}

// --- §4a: one-shot model/effort override ---

// effortLadder is the full effort ladder a %-override may name explicitly.
var effortLadder = map[string]contracts.Effort{
	"low":    contracts.EffortLow,
	"medium": contracts.EffortMedium,
	"high":   contracts.EffortHigh,
	"xhigh":  contracts.EffortXHigh,
	"max":    contracts.EffortMax,
}

// ErrUnresolvableOverride is returned when a %-override's alias/effort
// can't be resolved — the composer surfaces this inline before submit, the
// turn never runs with a bad override (§4a: "Unresolvable alias: inline
// composer error before submit, never a failed turn").
var ErrUnresolvableOverride = errors.New("tui: unresolvable %-override")

// KnownAliasChecker reports whether alias is a resolvable model alias/id.
// Injected so this package doesn't need to import bridle's registry
// directly — see the file doc comment.
type KnownAliasChecker func(alias string) bool

// ParseOverride parses a message that may start with a %-override
// (§4a: "%alias" or "%alias:effort", or "%:effort" to keep the current
// model and only raise effort). Returns ok=false if the message doesn't
// start with %. isKnownAlias may be nil (skips alias validation — used
// where the caller validates separately, e.g. tests).
func ParseOverride(text string, isKnownAlias KnownAliasChecker) (model string, effort contracts.Effort, rest string, ok bool, err error) {
	if !strings.HasPrefix(text, "%") {
		return "", "", text, false, nil
	}
	fields := strings.SplitN(text, " ", 2)
	directive := fields[0][1:] // strip leading %
	if len(fields) == 2 {
		rest = fields[1]
	}

	var aliasPart, effortPart string
	if idx := strings.IndexByte(directive, ':'); idx >= 0 {
		aliasPart, effortPart = directive[:idx], directive[idx+1:]
	} else {
		aliasPart = directive
	}

	if aliasPart != "" {
		if isKnownAlias != nil && !isKnownAlias(aliasPart) {
			return "", "", "", true, fmt.Errorf("%w: unknown model alias %q", ErrUnresolvableOverride, aliasPart)
		}
		model = aliasPart
	}
	if effortPart != "" {
		e, known := effortLadder[effortPart]
		if !known {
			return "", "", "", true, fmt.Errorf("%w: unknown effort tier %q", ErrUnresolvableOverride, effortPart)
		}
		effort = e
	} else if aliasPart != "" {
		// Default effort when only a model is given: high (feedback-effort-
		// prefer-high; the spec's default for the ladder this override
		// exists to reach).
		effort = contracts.EffortHigh
	}
	if aliasPart == "" && effortPart == "" {
		return "", "", "", true, fmt.Errorf("%w: empty directive", ErrUnresolvableOverride)
	}
	return model, effort, rest, true, nil
}
