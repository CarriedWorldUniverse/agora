package mcp

import (
	"strings"
	"testing"
)

func TestAssignNames_ShortNamesPassThrough(t *testing.T) {
	got := AssignNames([]ToolIdentity{{Server: "herald", Tool: "send"}})
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	want := "mcp__herald__send"
	if got[0].Name != want {
		t.Errorf("Name = %q, want %q", got[0].Name, want)
	}
}

func TestAssignNames_DeterministicSortOrder(t *testing.T) {
	pairs := []ToolIdentity{
		{Server: "zeta", Tool: "a"},
		{Server: "alpha", Tool: "b"},
		{Server: "alpha", Tool: "a"},
	}
	got := AssignNames(pairs)
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Server != "alpha" || got[0].Tool != "a" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Server != "alpha" || got[1].Tool != "b" {
		t.Errorf("got[1] = %+v", got[1])
	}
	if got[2].Server != "zeta" {
		t.Errorf("got[2] = %+v", got[2])
	}

	// Same input, called again, must produce byte-identical output.
	got2 := AssignNames(pairs)
	for i := range got {
		if got[i] != got2[i] {
			t.Fatalf("non-deterministic: %+v vs %+v", got[i], got2[i])
		}
	}
}

func TestAssignNames_OverflowTruncatesWithHashSuffix(t *testing.T) {
	longServer := strings.Repeat("s", 40)
	longTool := strings.Repeat("t", 40)
	got := AssignNames([]ToolIdentity{{Server: longServer, Tool: longTool}})
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	name := got[0].Name
	if len(name) > MaxToolNameLen {
		t.Fatalf("name %q exceeds MaxToolNameLen (%d): len=%d", name, MaxToolNameLen, len(name))
	}
	if !strings.Contains(name, "-") {
		t.Fatalf("expected hash-suffixed name, got %q", name)
	}
	// The last 12 chars before nothing (after the final "-") must be hex.
	idx := strings.LastIndex(name, "-")
	suffix := name[idx+1:]
	if len(suffix) != hashSuffixLen {
		t.Fatalf("suffix %q wrong length, want %d", suffix, hashSuffixLen)
	}

	// Deterministic: identical input -> identical output.
	got2 := AssignNames([]ToolIdentity{{Server: longServer, Tool: longTool}})
	if got2[0].Name != name {
		t.Fatalf("truncation not deterministic: %q vs %q", got2[0].Name, name)
	}
}

func TestAssignNames_DelimiterAmbiguityCollisionResolved(t *testing.T) {
	// "foo" + "bar__baz" and "foo__bar" + "baz" both qualify to the
	// identical raw string "mcp__foo__bar__baz" because server/tool names
	// may themselves contain "__". Both must end up in the result with
	// distinct, deterministic names.
	pairs := []ToolIdentity{
		{Server: "foo", Tool: "bar__baz"},
		{Server: "foo__bar", Tool: "baz"},
	}
	got := AssignNames(pairs)
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Name == got[1].Name {
		t.Fatalf("expected distinct names on collision, got %q for both", got[0].Name)
	}
	seen := map[string]bool{}
	for _, nt := range got {
		if seen[nt.Name] {
			t.Fatalf("duplicate assigned name %q", nt.Name)
		}
		seen[nt.Name] = true
	}
}

func TestAssignNames_MaxNameLenExactlyFits(t *testing.T) {
	// mcp__ (5) + server + __ (2) + tool == 64 exactly should NOT be hash-suffixed.
	server := strings.Repeat("a", 20)
	tool := strings.Repeat("b", MaxToolNameLen-5-len(server)-2)
	raw := qualify(server, tool)
	if len(raw) != MaxToolNameLen {
		t.Fatalf("test setup wrong: raw len = %d", len(raw))
	}
	got := AssignNames([]ToolIdentity{{Server: server, Tool: tool}})
	if got[0].Name != raw {
		t.Fatalf("exact-fit name got truncated: %q vs raw %q", got[0].Name, raw)
	}
}
