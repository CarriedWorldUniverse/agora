package mcp

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func seedRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	entries := []Entry{
		{Name: "mcp__herald__send_message", Description: "Send a message via herald", Schema: contracts.ToolSpec{Name: "mcp__herald__send_message"}, State: contracts.ToolDeferred},
		{Name: "mcp__herald__list_channels", Description: "List herald channels", Schema: contracts.ToolSpec{Name: "mcp__herald__list_channels"}, State: contracts.ToolDeferred},
		{Name: "mcp__cairn__commit", Description: "Commit changes to cairn", Schema: contracts.ToolSpec{Name: "mcp__cairn__commit"}, State: contracts.ToolDeferred},
		{Name: "fs", Description: "Read and write files", Schema: contracts.ToolSpec{Name: "fs"}, State: contracts.ToolCore},
	}
	for _, e := range entries {
		r.Register(e)
	}
	return r
}

func TestRegistry_CoreDeferredSorted(t *testing.T) {
	r := seedRegistry(t)
	core := r.Core()
	if len(core) != 1 || core[0].Name != "fs" {
		t.Fatalf("Core() = %+v", core)
	}
	deferred := r.Deferred()
	if len(deferred) != 3 {
		t.Fatalf("Deferred() len = %d", len(deferred))
	}
	// Sorted by name.
	if deferred[0].Name != "mcp__cairn__commit" {
		t.Fatalf("Deferred()[0] = %s, want sorted first entry", deferred[0].Name)
	}
}

func TestSearch_SelectExactByName(t *testing.T) {
	r := seedRegistry(t)
	res := r.Search("select:mcp__herald__send_message,mcp__cairn__commit", 5)
	if len(res.Tools) != 2 {
		t.Fatalf("Tools len = %d, want 2", len(res.Tools))
	}
	names := map[string]bool{res.Tools[0].Name: true, res.Tools[1].Name: true}
	if !names["mcp__herald__send_message"] || !names["mcp__cairn__commit"] {
		t.Fatalf("unexpected tools: %+v", res.Tools)
	}
	if len(res.Loaded) != 2 {
		t.Fatalf("Loaded = %v, want 2 newly-promoted names", res.Loaded)
	}
	// Session-sticky: entries are now core.
	e, _ := r.Get("mcp__herald__send_message")
	if e.State != contracts.ToolCore {
		t.Fatalf("expected promoted entry to be core, got %s", e.State)
	}
}

func TestSearch_SelectUnknownNameSkipped(t *testing.T) {
	r := seedRegistry(t)
	res := r.Search("select:does_not_exist", 5)
	if len(res.Tools) != 0 {
		t.Fatalf("expected no matches, got %+v", res.Tools)
	}
}

func TestSearch_SelectAlreadyCoreNotReLoaded(t *testing.T) {
	r := seedRegistry(t)
	res := r.Search("select:fs", 5)
	if len(res.Tools) != 1 {
		t.Fatalf("Tools len = %d", len(res.Tools))
	}
	// fs was already core, so Loaded (newly-promoted) must be empty.
	if len(res.Loaded) != 0 {
		t.Fatalf("Loaded = %v, want empty (already core)", res.Loaded)
	}
}

func TestSearch_KeywordRankingGolden(t *testing.T) {
	r := seedRegistry(t)
	res := r.Search("herald", 5)
	if len(res.Tools) != 2 {
		t.Fatalf("Tools len = %d, want 2 (both herald tools)", len(res.Tools))
	}
	// Deterministic golden ordering: name-match ties broken by name asc.
	got := []string{res.Tools[0].Name, res.Tools[1].Name}
	want := []string{"mcp__herald__list_channels", "mcp__herald__send_message"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranking = %v, want %v", got, want)
		}
	}
}

func TestSearch_KeywordNameBeatsDescriptionOnly(t *testing.T) {
	r := NewRegistry()
	r.Register(Entry{Name: "mcp__x__commit", Description: "unrelated", Schema: contracts.ToolSpec{Name: "mcp__x__commit"}, State: contracts.ToolDeferred})
	r.Register(Entry{Name: "mcp__y__other", Description: "does a commit of state", Schema: contracts.ToolSpec{Name: "mcp__y__other"}, State: contracts.ToolDeferred})

	res := r.Search("commit", 5)
	if len(res.Tools) != 2 {
		t.Fatalf("Tools len = %d, want 2", len(res.Tools))
	}
	if res.Tools[0].Name != "mcp__x__commit" {
		t.Fatalf("expected name-match to rank first, got %s", res.Tools[0].Name)
	}
}

func TestSearch_MaxResultsDefaultAndOverride(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 10; i++ {
		name := "mcp__s__tool" + string(rune('a'+i))
		r.Register(Entry{Name: name, Description: "matches query keyword", Schema: contracts.ToolSpec{Name: name}, State: contracts.ToolDeferred})
	}
	res := r.Search("keyword", 0)
	if len(res.Tools) != DefaultMaxResults {
		t.Fatalf("default max_results: got %d, want %d", len(res.Tools), DefaultMaxResults)
	}
	res2 := r.Search("keyword", 3)
	if len(res2.Tools) != 3 {
		t.Fatalf("override max_results: got %d, want 3", len(res2.Tools))
	}
}

func TestSearch_NoMatchesEmpty(t *testing.T) {
	r := seedRegistry(t)
	res := r.Search("zzz_nomatch_zzz", 5)
	if len(res.Tools) != 0 || len(res.Loaded) != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
}
