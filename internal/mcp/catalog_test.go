package mcp

import (
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestStdioCacheKey_StableForSameConfig(t *testing.T) {
	c := ServerConfig{Name: "herald", Command: "npx", Args: []string{"-y", "herald"}, Cwd: "/work", EnvironmentID: "local"}
	k1 := StdioCacheKey(c, nil, false)
	k2 := StdioCacheKey(c, nil, false)
	if k1 != k2 {
		t.Fatalf("cache key not stable: %q vs %q", k1, k2)
	}
}

func TestStdioCacheKey_ChangesInvalidate(t *testing.T) {
	base := ServerConfig{Name: "herald", Command: "npx", Args: []string{"-y", "herald"}, Cwd: "/work", EnvironmentID: "local"}
	baseKey := StdioCacheKey(base, nil, false)

	cases := []struct {
		name string
		mod  func(c ServerConfig) ServerConfig
	}{
		{"command", func(c ServerConfig) ServerConfig { c.Command = "uvx"; return c }},
		{"args", func(c ServerConfig) ServerConfig { c.Args = append([]string{}, "-y", "other"); return c }},
		{"cwd", func(c ServerConfig) ServerConfig { c.Cwd = "/other"; return c }},
		{"environment_id", func(c ServerConfig) ServerConfig { c.EnvironmentID = "remote"; return c }},
		{"env", func(c ServerConfig) ServerConfig { c.Env = map[string]string{"A": "1"}; return c }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := StdioCacheKey(tc.mod(base), nil, false)
			if k == baseKey {
				t.Errorf("expected %s change to invalidate cache key", tc.name)
			}
		})
	}

	t.Run("elicitation", func(t *testing.T) {
		k := StdioCacheKey(base, nil, true)
		if k == baseKey {
			t.Errorf("expected elicitation flag to change cache key")
		}
	})

	t.Run("env_var_value", func(t *testing.T) {
		base2 := base
		base2.EnvVars = []EnvVarRef{{Name: "TOKEN", Source: "local"}}
		k1 := StdioCacheKey(base2, map[string]string{"TOKEN": "a"}, false)
		k2 := StdioCacheKey(base2, map[string]string{"TOKEN": "b"}, false)
		if k1 == k2 {
			t.Errorf("expected env_var value change to invalidate cache key")
		}
	})
}

func TestStdioCacheKey_IncludesServerName(t *testing.T) {
	c1 := ServerConfig{Name: "a", Command: "npx"}
	c2 := ServerConfig{Name: "b", Command: "npx"}
	if StdioCacheKey(c1, nil, false) == StdioCacheKey(c2, nil, false) {
		t.Fatalf("expected distinct keys for distinct server names")
	}
}

func TestWasmCacheKey_ExactByHashAndEnv(t *testing.T) {
	k1 := WasmCacheKey("sha256:aaa", map[string]string{"IDENTITY": "shadow"})
	k2 := WasmCacheKey("sha256:aaa", map[string]string{"IDENTITY": "shadow"})
	if k1 != k2 {
		t.Fatalf("wasm key not stable")
	}
	k3 := WasmCacheKey("sha256:bbb", map[string]string{"IDENTITY": "shadow"})
	if k1 == k3 {
		t.Fatalf("expected distinct key for distinct module_hash")
	}
	k4 := WasmCacheKey("sha256:aaa", map[string]string{"IDENTITY": "anvil"})
	if k1 == k4 {
		t.Fatalf("expected distinct key for distinct env")
	}
}

func TestCatalog_GetPublish(t *testing.T) {
	clock := NewFakeClock(time.Unix(0, 0))
	cat := NewCatalog(clock)
	key := CacheKey("k1")

	if _, ok := cat.Get(key); ok {
		t.Fatalf("expected miss before publish")
	}
	tools := []contracts.ToolSpec{{Name: "a"}}
	if !cat.Publish(key, 1, tools) {
		t.Fatalf("expected publish accepted")
	}
	got, ok := cat.Get(key)
	if !ok || len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("Get after publish = %v, %v", got, ok)
	}
}

func TestCatalog_TTLExpiry(t *testing.T) {
	clock := NewFakeClock(time.Unix(0, 0))
	cat := NewCatalog(clock)
	key := CacheKey("k1")
	cat.Publish(key, 1, []contracts.ToolSpec{{Name: "a"}})

	clock.Advance(CatalogCacheTTL - time.Second)
	if _, ok := cat.Get(key); !ok {
		t.Fatalf("expected hit just before TTL")
	}

	clock.Advance(2 * time.Second)
	if _, ok := cat.Get(key); ok {
		t.Fatalf("expected miss after TTL expiry")
	}
}

func TestCatalog_GenerationTicketRejectsStalePublish(t *testing.T) {
	clock := NewFakeClock(time.Unix(0, 0))
	cat := NewCatalog(clock)
	key := CacheKey("k1")

	if !cat.Publish(key, 2, []contracts.ToolSpec{{Name: "fresh"}}) {
		t.Fatalf("expected gen 2 accepted")
	}
	// A slower fetch from an earlier generation must not clobber it.
	if cat.Publish(key, 1, []contracts.ToolSpec{{Name: "stale"}}) {
		t.Fatalf("expected gen 1 (older) publish to be rejected")
	}
	got, _ := cat.Get(key)
	if got[0].Name != "fresh" {
		t.Fatalf("stale publish clobbered fresh entry: %v", got)
	}

	if !cat.Publish(key, 3, []contracts.ToolSpec{{Name: "newer"}}) {
		t.Fatalf("expected gen 3 accepted")
	}
	got, _ = cat.Get(key)
	if got[0].Name != "newer" {
		t.Fatalf("newer generation did not win: %v", got)
	}
}

func TestCatalog_CapEvictsLRU(t *testing.T) {
	clock := NewFakeClock(time.Unix(0, 0))
	cat := NewCatalog(clock)
	for i := 0; i < CatalogCacheCap; i++ {
		key := CacheKey(rune('a' + i))
		cat.Publish(key, 1, []contracts.ToolSpec{{Name: "x"}})
	}
	if cat.Len() != CatalogCacheCap {
		t.Fatalf("Len = %d, want %d", cat.Len(), CatalogCacheCap)
	}
	// One more publish evicts the least-recently-used entry.
	overflowKey := CacheKey("overflow")
	cat.Publish(overflowKey, 1, []contracts.ToolSpec{{Name: "y"}})
	if cat.Len() != CatalogCacheCap {
		t.Fatalf("Len after overflow = %d, want %d (cap enforced)", cat.Len(), CatalogCacheCap)
	}
	firstKey := CacheKey(rune('a'))
	if _, ok := cat.Get(firstKey); ok {
		t.Fatalf("expected oldest entry evicted")
	}
	if _, ok := cat.Get(overflowKey); !ok {
		t.Fatalf("expected newly published entry present")
	}
}
