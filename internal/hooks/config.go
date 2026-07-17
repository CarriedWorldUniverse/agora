package hooks

// HandlerType tags a handler config by its `type` field.
// Spec: agora-spec-hooks.md §1.4.
type HandlerType string

const (
	// HandlerCommand is the only implemented handler type in v1 (§1.4/build
	// notes: "Only implement command handlers first").
	HandlerCommand HandlerType = "command"
	// HandlerPrompt and HandlerAgent parse-and-skip: schema-present, not run.
	HandlerPrompt HandlerType = "prompt"
	HandlerAgent  HandlerType = "agent"
)

// DefaultTimeoutSeconds and MinTimeoutSeconds are the handler timeout
// default and floor. Spec §1.4: "timeout: 600 seconds default, floor 1".
const (
	DefaultTimeoutSeconds = 600
	MinTimeoutSeconds     = 1
)

// Handler is one `command`-type handler config entry.
// Spec: agora-spec-hooks.md §1.4.
type Handler struct {
	Type HandlerType `json:"type"`
	// Command is required for HandlerCommand; ignored (kept for round-trip)
	// for prompt/agent.
	Command string `json:"command,omitempty"`
	// CommandWindows is an optional per-OS override (alias command_windows).
	CommandWindows string `json:"commandWindows,omitempty"`
	// Timeout is in SECONDS. Zero means "unset" — Normalize applies the
	// default/floor (§1.4).
	Timeout int `json:"timeout,omitempty"`
	// Async: fire-and-forget, does not block the turn (§1.4, engine §3).
	Async bool `json:"async,omitempty"`
	// StatusMessage is a UI display string only; no engine semantics.
	StatusMessage string `json:"statusMessage,omitempty"`
}

// Normalize returns h with the timeout default/floor applied (§1.4: default
// 600, floor 1). It does not mutate h.
func (h Handler) Normalize() Handler {
	switch {
	case h.Timeout == 0:
		h.Timeout = DefaultTimeoutSeconds
	case h.Timeout < MinTimeoutSeconds:
		h.Timeout = MinTimeoutSeconds
	}
	return h
}

// EffectiveCommand returns the command to run on the given GOOS, honoring
// CommandWindows for "windows" and falling back to Command otherwise.
// Spec §1.4: "commandWindows: optional per-OS override".
func (h Handler) EffectiveCommand(goos string) string {
	if goos == "windows" && h.CommandWindows != "" {
		return h.CommandWindows
	}
	return h.Command
}

// MatcherGroup is one entry in an event's handler list: an optional matcher
// plus the handlers it gates. Spec: agora-spec-hooks.md §1.3.
type MatcherGroup struct {
	// Matcher: "", "*", or nil-equivalent all mean match-everything (§1.5).
	// A Go zero-value string already represents that, so MatcherGroup has no
	// separate "matcher present" flag — matching that JSON `null` and a
	// missing/empty field are handled identically per §1.5.
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []Handler `json:"hooks"`
}

// EventMap is the `hooks` object: PascalCase event name -> matcher groups.
// Spec: agora-spec-hooks.md §1.2.
type EventMap map[EventName][]MatcherGroup

// Config is the top-level hooks.json shape (also the flattened [hooks]
// TOML table, minus the state sub-table which lives in trust.go's
// HandlerState — state is read only from the User(+session) layer per
// §4.4, not carried on Config itself).
// Spec: agora-spec-hooks.md §1.1.
type Config struct {
	Description string   `json:"description,omitempty"`
	Hooks       EventMap `json:"hooks"`
}
