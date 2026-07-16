package prompt

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestRoleMap covers the §1a fragment role map: each fragment class maps to
// the right role in the authority gradient.
func TestRoleMap(t *testing.T) {
	cases := []struct {
		class FragmentClass
		want  contracts.Role
	}{
		{FragSegments, contracts.RoleSystem},
		{FragSkillsCatalog, contracts.RoleDeveloper},
		{FragMemoryIndex, contracts.RoleDeveloper},
		{FragProjectDocs, contracts.RoleUser},
		{FragSkillBody, contracts.RoleUser},
		{FragWorkingSet, contracts.RoleUser},
	}
	for _, c := range cases {
		got, ok := RoleMap[c.class]
		if !ok {
			t.Errorf("RoleMap[%q]: missing entry", c.class)
			continue
		}
		if got != c.want {
			t.Errorf("RoleMap[%q] = %q, want %q", c.class, got, c.want)
		}
	}
	if len(RoleMap) != len(cases) {
		t.Errorf("RoleMap has %d entries, want %d (an untested fragment class was added)", len(RoleMap), len(cases))
	}
}

// TestRoleMap_OnlySegmentsAreSystem asserts the authority-gradient invariant
// directly: exactly one fragment class carries RoleSystem, and it is
// FragSegments — "nothing else gets its authority" (§1a).
func TestRoleMap_OnlySegmentsAreSystem(t *testing.T) {
	var systemClasses []FragmentClass
	for class, role := range RoleMap {
		if role == contracts.RoleSystem {
			systemClasses = append(systemClasses, class)
		}
	}
	if len(systemClasses) != 1 || systemClasses[0] != FragSegments {
		t.Fatalf("RoleSystem classes = %v, want exactly [%q]", systemClasses, FragSegments)
	}
}
