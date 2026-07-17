package subagent

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAgentDefMD(t *testing.T) {
	const doc = `---
name: reviewer
description: Review stage for completed code changes. Use after builders complete.
tools: Read, Glob, Grep, Bash
model: sonnet
effort: high
---
You are the reviewer. Check correctness, bugs, edge cases.
`
	def, err := ParseAgentDefMD([]byte(doc), "fallback")
	if err != nil {
		t.Fatalf("ParseAgentDefMD: %v", err)
	}
	if def.Name != "reviewer" {
		t.Errorf("Name = %q, want reviewer", def.Name)
	}
	if def.Description == "" {
		t.Error("Description is empty")
	}
	wantTools := []string{"Read", "Glob", "Grep", "Bash"}
	if len(def.Tools) != len(wantTools) {
		t.Fatalf("Tools = %v, want %v", def.Tools, wantTools)
	}
	for i, tool := range wantTools {
		if def.Tools[i] != tool {
			t.Errorf("Tools[%d] = %q, want %q", i, def.Tools[i], tool)
		}
	}
	if def.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet", def.Model)
	}
	if def.Effort != "high" {
		t.Errorf("Effort = %q, want high", def.Effort)
	}
	if !strings.Contains(def.Prompt, "You are the reviewer") {
		t.Errorf("Prompt = %q, want body text", def.Prompt)
	}
}

func TestParseAgentDefMD_NameFallback(t *testing.T) {
	const doc = `---
description: does a thing
---
body
`
	def, err := ParseAgentDefMD([]byte(doc), "my-agent")
	if err != nil {
		t.Fatalf("ParseAgentDefMD: %v", err)
	}
	if def.Name != "my-agent" {
		t.Errorf("Name = %q, want fallback my-agent", def.Name)
	}
}

func TestParseAgentDefMD_ToolsOmittedMeansAllTools(t *testing.T) {
	const doc = `---
name: x
description: y
---
z
`
	def, err := ParseAgentDefMD([]byte(doc), "fallback")
	if err != nil {
		t.Fatalf("ParseAgentDefMD: %v", err)
	}
	if def.Tools != nil {
		t.Errorf("Tools = %v, want nil (omit = all tools per spec §1)", def.Tools)
	}
}

func TestParseAgentDefMD_EmptyDescriptionErrors(t *testing.T) {
	const doc = `---
name: x
description: ""
---
z
`
	_, err := ParseAgentDefMD([]byte(doc), "fallback")
	if !errors.Is(err, ErrAgentDefEmptyDescription) {
		t.Fatalf("err = %v, want ErrAgentDefEmptyDescription", err)
	}
}

func TestParseAgentDefMD_NoFrontmatterErrors(t *testing.T) {
	_, err := ParseAgentDefMD([]byte("just a body, no frontmatter\n"), "fallback")
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseAgentDefMD_UnterminatedFrontmatterErrors(t *testing.T) {
	_, err := ParseAgentDefMD([]byte("---\nname: x\n"), "fallback")
	if err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
}

func TestBuiltinAgentDefs(t *testing.T) {
	defs := BuiltinAgentDefs()
	if len(defs) != 2 {
		t.Fatalf("len(defs) = %d, want 2", len(defs))
	}
	byName := map[string]*AgentDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	gp, ok := byName[BuiltinGeneralPurpose]
	if !ok {
		t.Fatal("missing general-purpose builtin")
	}
	if gp.Tools != nil {
		t.Errorf("general-purpose Tools = %v, want nil (all tools)", gp.Tools)
	}
	ex, ok := byName[BuiltinExplore]
	if !ok {
		t.Fatal("missing explore builtin")
	}
	if len(ex.Tools) == 0 {
		t.Error("explore should have a read-only tool allowlist")
	}
}

func TestRegistry_DefOverridesBuiltin(t *testing.T) {
	custom := &AgentDef{Name: BuiltinExplore, Description: "custom explore", Tools: []string{"Read"}}
	r := NewRegistry([]*AgentDef{custom})
	got, ok := r.Get(BuiltinExplore)
	if !ok {
		t.Fatal("explore missing")
	}
	if got.Description != "custom explore" {
		t.Errorf("Description = %q, want custom to win over builtin", got.Description)
	}
}

func TestRegistry_FirstDefWinsCollision(t *testing.T) {
	a := &AgentDef{Name: "dup", Description: "first"}
	b := &AgentDef{Name: "dup", Description: "second"}
	r := NewRegistry([]*AgentDef{a, b})
	got, ok := r.Get("dup")
	if !ok {
		t.Fatal("dup missing")
	}
	if got.Description != "first" {
		t.Errorf("Description = %q, want first (caller-ordered precedence)", got.Description)
	}
}

func TestRegistry_Names_Sorted(t *testing.T) {
	r := NewRegistry([]*AgentDef{{Name: "zzz", Description: "d"}, {Name: "aaa", Description: "d"}})
	names := r.Names()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Names() not sorted: %v", names)
		}
	}
}
