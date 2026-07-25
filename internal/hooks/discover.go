package hooks

import "fmt"

// Layer is a hooks config source. Spec: agora-spec-hooks.md §4.1/§4.2.
//
// "Precedence" here (the §4.1 ordering) governs discovery/report ORDER —
// the global monotonic counter used by aggregation rules like "first block
// reason" (§2.1) and "continuation fragments concatenated in declaration
// order" (§2.9/2.10) — not an override relationship between layers: every
// matched handler from every layer still runs and is aggregated, nothing
// is masked by a higher layer. Spec keeps "the layer enum extensible"
// (§0 build notes); LayerPlugin is included for §4.1 item 3 even though
// plugin *sources* (finding/loading plugin dirs) are out of this unit's
// scope — the enum value and its precedence slot are what the ordering
// rules need.
type Layer string

const (
	// LayerManaged: broker/custodian-pushed policy hooks (§4.3, reserved).
	// Always enabled+trusted (§4.3) — never gated by trust.go's hash check.
	LayerManaged Layer = "managed"
	// LayerUser: ~/.agora/hooks.json + [hooks] in user config (§4.2).
	LayerUser Layer = "user"
	// LayerProject: <project>/.agora/hooks.json (§4.2).
	LayerProject Layer = "project"
	// LayerPlugin: plugin hook sources, loaded last (§4.1 item 3).
	LayerPlugin Layer = "plugin"
)

// layerOrder is the fixed lowest→highest precedence sequence, exactly
// §4.1's numbered list: 1. managed, 2. config layers lowest-precedence-first
// (user, then project), 3. plugin sources last.
var layerOrder = []Layer{LayerManaged, LayerUser, LayerProject, LayerPlugin}

// Rank returns l's position in layerOrder (0 = lowest precedence), or -1 for
// an unrecognized layer (a config loader should reject that at load time;
// this package doesn't construct Source values with an invalid Layer).
func (l Layer) Rank() int {
	for i, x := range layerOrder {
		if x == l {
			return i
		}
	}
	return -1
}

// Source identifies where a handler config came from: the layer, and the
// "{source_path_or_pluginid}" half of the §4.4 positional key — a dot-dir
// path for user/project, or "plugin:<id>" for a plugin.
type Source struct {
	Layer Layer
	Path  string
}

// RegisteredHandler is one handler as discovered: its config, its position
// (event/group/handler indices — the JSON-array positions, never Go map
// order), and its Seq — the global monotonic discovery-order counter
// (§3: "declaration/discovery order... global monotonic counter across
// layers"; §4.1's layer ordering).
type RegisteredHandler struct {
	Source       Source
	Event        EventName
	GroupIndex   int
	HandlerIndex int
	Matcher      string
	Handler      Handler
	Seq          int
}

// PositionalKey builds "{source_path_or_pluginid}:{event}:{group_index}:{handler_index}"
// (§4.4) — the key handler enable/trust state is recorded under.
func (rh RegisteredHandler) PositionalKey() string {
	return fmt.Sprintf("%s:%s:%d:%d", rh.Source.Path, rh.Event, rh.GroupIndex, rh.HandlerIndex)
}

// ContentHash computes rh's trust hash (§4.4) via ContentHash(event,
// matcher, command) — normalized event+matcher+command identity.
func (rh RegisteredHandler) ContentHash() string {
	return ContentHash(rh.Event, rh.Matcher, rh.Handler.Command, rh.Handler.CommandWindows)
}

// Registry accumulates handlers from config layers in a deterministic
// discovery order and resolves them (matching + trust) for a firing.
type Registry struct {
	handlers []RegisteredHandler
	seq      int
}

// Load appends every handler in cfg's event map for one Source, assigning
// each the next Seq values in a FIXED, deterministic order: events in
// AllEvents order (never map iteration — ground rule 3), then groups/
// handlers in their JSON-array (config-declaration) order.
//
// Precondition (matches the spec's discovery-time nature, not runtime-
// enforced): callers invoke Load once per layer in ascending Layer.Rank()
// order (managed, user, project, plugin) so that Seq reflects §4.1's
// layer-then-declaration order. Each layer/folder is loaded once (§4.2:
// "each folder loaded once"); de-duplicating repeated Source values across
// calls is the caller's job (a visited-set), not this method's.
func (reg *Registry) Load(src Source, cfg Config) {
	for _, ev := range AllEvents {
		groups := cfg.Hooks[ev]
		for gi, g := range groups {
			for hi, h := range g.Hooks {
				reg.handlers = append(reg.handlers, RegisteredHandler{
					Source:       src,
					Event:        ev,
					GroupIndex:   gi,
					HandlerIndex: hi,
					Matcher:      g.Matcher,
					Handler:      h.Normalize(),
					Seq:          reg.seq,
				})
				reg.seq++
			}
		}
	}
}

// ForEvent returns every registered handler for event whose matcher
// matches matchAgainst (or all of them, if the event ignores matchers —
// §1.2), in discovery order (Seq ascending — already the append order
// since Load assigns Seq monotonically). A matcher group with an invalid
// regex is dropped with a warning (§1.5) rather than erroring the whole
// call.
// All returns every registered handler in discovery order. Exported for the
// trust surface (NEX-825): an operator cannot grant trust to a hook they
// cannot see, and until this existed nothing could enumerate what had been
// discovered — hooks-state.json had a reader and no writer, so every handler
// resolved untrusted and silently never ran.
func (reg *Registry) All() []RegisteredHandler {
	out := make([]RegisteredHandler, len(reg.handlers))
	copy(out, reg.handlers)
	return out
}

func (reg *Registry) ForEvent(event EventName, matchAgainst string) (matched []RegisteredHandler, warnings []string) {
	ignore := event.MatcherIgnored()
	for _, rh := range reg.handlers {
		if rh.Event != event {
			continue
		}
		if ignore {
			matched = append(matched, rh)
			continue
		}
		ok, err := MatchMatcher(rh.Matcher, matchAgainst)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"hooks: dropping matcher group (invalid regex) source=%s event=%s group=%d: %v",
				rh.Source.Path, rh.Event, rh.GroupIndex, err))
			continue
		}
		if ok {
			matched = append(matched, rh)
		}
	}
	return matched, warnings
}

// ResolvedHandler is a RegisteredHandler after trust resolution for a
// firing (trust.go's ResolveTrust).
type ResolvedHandler struct {
	RegisteredHandler
	ContentHash string
	Enabled     bool
	TrustState  TrustState
	// Runnable is the fail-closed gate: false for Untrusted/Modified
	// (ground rule 6 — never execute silently), even though the handler is
	// still returned here for the UI to list (§4.4 closing sentence).
	Runnable bool
}

// Resolve applies trust.ResolveTrust to every handler in matched, using
// state (keyed by PositionalKey()) and bypassTrust (a session/dev-mode
// override — never set from untrusted input).
func Resolve(matched []RegisteredHandler, state map[string]HandlerState, bypassTrust bool) []ResolvedHandler {
	out := make([]ResolvedHandler, 0, len(matched))
	for _, rh := range matched {
		hash := rh.ContentHash()
		managed := rh.Source.Layer == LayerManaged
		st, known := state[rh.PositionalKey()]
		runnable, enabled, status := ResolveTrust(st, known, hash, managed, bypassTrust)
		out = append(out, ResolvedHandler{
			RegisteredHandler: rh,
			ContentHash:       hash,
			Enabled:           enabled,
			TrustState:        status,
			Runnable:          runnable,
		})
	}
	return out
}
