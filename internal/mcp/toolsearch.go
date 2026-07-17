package mcp

import (
	"sort"
	"strings"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// DefaultMaxResults is tool_search's default max_results (§5).
const DefaultMaxResults = 5

// Entry is one registry-wide tool (native family or MCP), core or deferred.
// Spec: agora-spec-mcp.md §5.
type Entry struct {
	Name        string // model-visible qualified name
	Description string
	Schema      contracts.ToolSpec // full definition, always held (deferral is a PRESENTATION state)
	State       contracts.ToolState
}

// Registry is the registry-wide tool catalog (native families AND MCP
// tools) that deferral/tool_search operates over (§5: "applies
// registry-wide"). It stores the FULL schema for every entry regardless of
// State — State only governs what's injected into the system prompt at
// start; tool_search promotes Deferred -> Core without ever re-fetching
// (the schema was already known, just not shown).
type Registry struct {
	mu      sync.Mutex
	entries map[string]*Entry
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]*Entry)}
}

// Register adds or replaces an entry.
//
// Name-collision across servers is NOT enforced here (Register just keys by
// e.Name, last write wins) — the integration caller is responsible for
// always running AssignNames over the COMPLETE cross-server tool set before
// registering, so two servers' same-named tools are disambiguated up front
// rather than silently clobbering each other in this map. (LOW, review
// finding, not implemented — documented expectation only.)
func (r *Registry) Register(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := e
	r.entries[e.Name] = &cp
}

// Get returns entry by exact model-visible name.
func (r *Registry) Get(name string) (Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[name]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

// snapshot returns all entries, name-sorted (deterministic; never
// map-iteration order, house style).
func (r *Registry) snapshot() []*Entry {
	names := make([]string, 0, len(r.entries))
	for n := range r.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*Entry, len(names))
	for i, n := range names {
		out[i] = r.entries[n]
	}
	return out
}

// Core returns all entries currently in core state, name-sorted.
func (r *Registry) Core() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Entry
	for _, e := range r.snapshot() {
		if e.State == contracts.ToolCore {
			out = append(out, *e)
		}
	}
	return out
}

// Deferred returns all entries currently deferred, name-sorted — the
// compact "deferred tools available via tool_search: …" listing (§5).
func (r *Registry) Deferred() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Entry
	for _, e := range r.snapshot() {
		if e.State == contracts.ToolDeferred {
			out = append(out, *e)
		}
	}
	return out
}

// promote marks names core (session-sticky: once loaded, never goes back to
// deferred — §5) and returns which names actually changed state, for the
// tool.loaded event.
func (r *Registry) promote(names []string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var loaded []string
	for _, n := range names {
		e, ok := r.entries[n]
		if !ok {
			continue
		}
		if e.State != contracts.ToolCore {
			e.State = contracts.ToolCore
			loaded = append(loaded, n)
		}
	}
	sort.Strings(loaded)
	return loaded
}

// SearchResult is tool_search's return: the full schemas for matched tools
// (now callable like any core tool) plus which names newly transitioned to
// core (drives the tool.loaded event, §5).
type SearchResult struct {
	Tools  []contracts.ToolSpec
	Loaded []string
}

// Search implements the tool_search tool (§5): "select:name1,name2" for
// exact fetch by name, or a free-text keyword query ranked over name +
// description. maxResults <= 0 uses DefaultMaxResults.
func (r *Registry) Search(query string, maxResults int) SearchResult {
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}

	if rest, ok := strings.CutPrefix(query, "select:"); ok {
		names := strings.Split(rest, ",")
		var matched []string
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if _, ok := r.Get(n); ok {
				matched = append(matched, n)
			}
		}
		sort.Strings(matched)
		return r.load(matched)
	}

	ranked := r.rank(query)
	if len(ranked) > maxResults {
		ranked = ranked[:maxResults]
	}
	names := make([]string, len(ranked))
	for i, e := range ranked {
		names[i] = e.Name
	}
	return r.load(names)
}

func (r *Registry) load(names []string) SearchResult {
	loaded := r.promote(names)
	tools := make([]contracts.ToolSpec, 0, len(names))
	for _, n := range names {
		if e, ok := r.Get(n); ok {
			tools = append(tools, e.Schema)
		}
	}
	return SearchResult{Tools: tools, Loaded: loaded}
}

// scored is one ranked candidate.
type scored struct {
	entry *Entry
	score int
}

// rank implements the weighted-lexical scorer (§5: "name-match ≫
// description-match, prefix-related bonus") — the same shape as the
// extracted codex skills-shadow scorer the spec cites: per query token,
// name hits dominate description hits, and a prefix match earns a bonus
// over a mid-string substring match. Deterministic tie-break: score desc,
// then name asc.
func (r *Registry) rank(query string) []*Entry {
	q := strings.ToLower(strings.TrimSpace(query))
	tokens := strings.Fields(q)
	if len(tokens) == 0 {
		return nil
	}

	r.mu.Lock()
	entries := r.snapshot()
	r.mu.Unlock()

	var candidates []scored
	for _, e := range entries {
		name := strings.ToLower(e.Name)
		desc := strings.ToLower(e.Description)
		score := 0
		if name == q {
			score += 1000
		}
		for _, t := range tokens {
			if t == "" {
				continue
			}
			if strings.HasPrefix(name, t) {
				score += 60
			} else if strings.Contains(name, t) {
				score += 40
			}
			if strings.HasPrefix(desc, t) {
				score += 15
			} else if strings.Contains(desc, t) {
				score += 10
			}
		}
		if score > 0 {
			candidates = append(candidates, scored{entry: e, score: score})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].entry.Name < candidates[j].entry.Name
	})

	out := make([]*Entry, len(candidates))
	for i, c := range candidates {
		out[i] = c.entry
	}
	return out
}
