package mcp

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// Catalog cache tuning (§2: "process-scoped LRU (cap 32, TTL 30 min)").
const (
	CatalogCacheCap = 32
	CatalogCacheTTL = 30 * time.Minute
)

// CacheKey identifies one server's cached tool catalog. Stable across
// restarts with unchanged config; changes to anything the fingerprint
// covers invalidate it (§2).
type CacheKey string

// StdioCacheKey computes the §2 stdio cache key: "server name + SHA1
// fingerprint over (command, args, env, env_vars + their current values,
// cwd, environment_id, elicitation capability)". envVarValues supplies the
// CURRENT resolved value for each c.EnvVars entry (the fingerprint covers
// the value, not just the forwarded name, per spec) — a caller with no
// value for a name simply omits it, which still changes the fingerprint
// deterministically since the (name, value-or-absent) set is what's hashed.
func StdioCacheKey(c ServerConfig, envVarValues map[string]string, elicitation bool) CacheKey {
	h := sha1.New()
	fmt.Fprintf(h, "command=%s\n", c.Command)
	for _, a := range c.Args {
		fmt.Fprintf(h, "arg=%s\n", a)
	}
	for _, k := range SortedNames(c.Env) {
		fmt.Fprintf(h, "env=%s=%s\n", k, c.Env[k])
	}
	names := make([]string, len(c.EnvVars))
	for i, ev := range c.EnvVars {
		names[i] = ev.Name
	}
	sort.Strings(names)
	for _, n := range names {
		v, ok := envVarValues[n]
		fmt.Fprintf(h, "env_var=%s=%s(present=%v)\n", n, v, ok)
	}
	fmt.Fprintf(h, "cwd=%s\n", c.Cwd)
	fmt.Fprintf(h, "environment_id=%s\n", c.EnvironmentID)
	fmt.Fprintf(h, "elicitation=%v\n", elicitation)
	return CacheKey(fmt.Sprintf("%s:%s", c.Name, hex.EncodeToString(h.Sum(nil))))
}

// WasmCacheKey computes the §1a/§2 wasm cache key: an EXACT key of
// module_hash + interpolated env (no heuristic fingerprint needed — content
// addressing already pins the module). Valid indefinitely per hash; the LRU
// still evicts it on capacity/TTL pressure like any entry, it is just never
// invalidated by anything OTHER than the hash/env changing.
func WasmCacheKey(moduleHash string, env map[string]string) CacheKey {
	var b strings.Builder
	b.WriteString(moduleHash)
	for _, k := range SortedNames(env) {
		fmt.Fprintf(&b, "|%s=%s", k, env[k])
	}
	return CacheKey(b.String())
}

// catalogEntry is one cached tool list plus the bookkeeping the generation
// ticket and LRU need.
type catalogEntry struct {
	tools      []contracts.ToolSpec
	generation int64
	storedAt   time.Time
}

// Catalog is the process-scoped tool-catalog cache (§2). Only stdio and
// wasm catalogs are cacheable (http servers can be listed cheaply and
// their tool set can legitimately change per-request auth context, so the
// spec scopes caching to the two transports named); callers key by
// StdioCacheKey/WasmCacheKey accordingly — Catalog itself is transport-
// agnostic, it just stores whatever key it's given.
type Catalog struct {
	mu    sync.Mutex
	clock Clock
	cap   int
	ttl   time.Duration

	entries map[CacheKey]*catalogEntry
	// order tracks LRU recency, most-recently-used at the back.
	order []CacheKey
	// lastGeneration tracks the highest ACCEPTED generation per key, so a
	// slow fetch can never clobber a newer one (§2 "generation tickets").
	lastGeneration map[CacheKey]int64
}

// NewCatalog builds a cache with the spec defaults (cap 32, TTL 30min); a
// nil clock uses SystemClock.
func NewCatalog(clock Clock) *Catalog {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Catalog{
		clock:          clock,
		cap:            CatalogCacheCap,
		ttl:            CatalogCacheTTL,
		entries:        make(map[CacheKey]*catalogEntry),
		lastGeneration: make(map[CacheKey]int64),
	}
}

// Get returns the cached tools for key if present and not expired.
// "Serve cached tools before startup completes" (§2) is the caller's use of
// this: check the cache first, connect live in parallel, Publish() the
// refresh when it lands.
func (c *Catalog) Get(key CacheKey) ([]contracts.ToolSpec, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.clock.Now().Sub(e.storedAt) > c.ttl {
		c.evict(key)
		return nil, false
	}
	c.touch(key)
	return e.tools, true
}

// Publish stores tools for key at the given fetch generation. Accepted only
// if generation > the last accepted generation for this key (§2: "a publish
// is accepted only if its fetch generation > last accepted"); returns
// whether it was accepted.
func (c *Catalog) Publish(key CacheKey, generation int64, tools []contracts.ToolSpec) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.lastGeneration[key]; ok && generation <= last {
		return false
	}
	c.lastGeneration[key] = generation
	if _, exists := c.entries[key]; !exists {
		if len(c.order) >= c.cap {
			c.evictOldest()
		}
		c.order = append(c.order, key)
	} else {
		c.touch(key)
	}
	c.entries[key] = &catalogEntry{tools: tools, generation: generation, storedAt: c.clock.Now()}
	return true
}

// Invalidate drops key's entry (e.g. its config changed under it).
func (c *Catalog) Invalidate(key CacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evict(key)
}

// Len returns the number of live (non-expired-check-skipped) entries —
// tests use this to assert cap eviction.
func (c *Catalog) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *Catalog) touch(key CacheKey) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
}

func (c *Catalog) evict(key CacheKey) {
	delete(c.entries, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *Catalog) evictOldest() {
	if len(c.order) == 0 {
		return
	}
	oldest := c.order[0]
	c.order = c.order[1:]
	delete(c.entries, oldest)
}
