package subagent

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func TestResolveInheritance_ModelEffortPrecedence(t *testing.T) {
	parent := ParentContext{Model: "parent-model", Effort: contracts.EffortMedium}
	def := &AgentDef{Model: "def-model", Effort: "low"}

	// Parent alone.
	eff := ResolveInheritance(parent, nil, SpawnOpts{})
	if eff.Model != "parent-model" || eff.Effort != contracts.EffortMedium {
		t.Errorf("no def/opts override: eff = %+v", eff)
	}

	// Def overrides parent.
	eff = ResolveInheritance(parent, def, SpawnOpts{})
	if eff.Model != "def-model" || eff.Effort != contracts.Effort("low") {
		t.Errorf("def should override parent: eff = %+v", eff)
	}

	// Call opts override def.
	eff = ResolveInheritance(parent, def, SpawnOpts{Model: "opts-model", Effort: contracts.EffortHigh})
	if eff.Model != "opts-model" || eff.Effort != contracts.EffortHigh {
		t.Errorf("opts should override def: eff = %+v", eff)
	}
}

func TestResolveInheritance_PolicyAlwaysInherited(t *testing.T) {
	policy := contracts.PolicySet{contracts.KindExec: contracts.PolicyAuto}
	parent := ParentContext{Policy: policy, Cwd: "/work"}
	eff := ResolveInheritance(parent, &AgentDef{Model: "x"}, SpawnOpts{})
	if eff.Policy[contracts.KindExec] != contracts.PolicyAuto {
		t.Errorf("Policy = %v, want inherited unconditionally (approvals §4 invariant 4)", eff.Policy)
	}
	if eff.Cwd != "/work" {
		t.Errorf("Cwd = %q, want inherited", eff.Cwd)
	}
}

func TestResolveInheritance_ToolsNarrowedByDef(t *testing.T) {
	parent := ParentContext{Tools: []string{"Read", "Write", "Bash", "Grep"}}
	def := &AgentDef{Tools: []string{"Bash", "Read", "Exec"}} // Exec not in parent's set
	eff := ResolveInheritance(parent, def, SpawnOpts{})
	want := []string{"Bash", "Read"} // def order, intersected with parent's set
	if len(eff.Tools) != len(want) {
		t.Fatalf("Tools = %v, want %v", eff.Tools, want)
	}
	for i := range want {
		if eff.Tools[i] != want[i] {
			t.Fatalf("Tools = %v, want %v", eff.Tools, want)
		}
	}
}

func TestResolveInheritance_DefOmitsToolsMeansNoNarrowing(t *testing.T) {
	parent := ParentContext{Tools: []string{"Read", "Write"}}
	eff := ResolveInheritance(parent, &AgentDef{}, SpawnOpts{}) // Tools nil = omit
	if len(eff.Tools) != 2 {
		t.Errorf("Tools = %v, want unchanged parent set", eff.Tools)
	}
}

func TestResolveInheritance_UnrestrictedParentGetsDefVerbatim(t *testing.T) {
	parent := ParentContext{Tools: nil} // parent itself unrestricted
	def := &AgentDef{Tools: []string{"Read", "Grep"}}
	eff := ResolveInheritance(parent, def, SpawnOpts{})
	if len(eff.Tools) != 2 || eff.Tools[0] != "Read" || eff.Tools[1] != "Grep" {
		t.Errorf("Tools = %v, want def.Tools verbatim", eff.Tools)
	}
}
