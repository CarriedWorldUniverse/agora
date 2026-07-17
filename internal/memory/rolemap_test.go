package memory

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/prompt"
)

// TestMemoryIndexRendersAsDeveloperRoleFragment pins the index-injection
// contract this package must honor: the RenderIndex output is a
// FragMemoryIndex-class fragment, and per internal/prompt.RoleMap that
// class is RoleDeveloper — a harness-generated catalog, never system-role
// (constitution) or user-role (content) — matching agora-spec-prompt.md
// §1a and agora-spec-memory.md §2 ("injected as a developer-role
// fragment... never instruction-weight authority").
func TestMemoryIndexRendersAsDeveloperRoleFragment(t *testing.T) {
	role, ok := prompt.RoleMap[prompt.FragMemoryIndex]
	if !ok {
		t.Fatal("prompt.RoleMap has no FragMemoryIndex entry")
	}
	if role != contracts.RoleDeveloper {
		t.Fatalf("prompt.RoleMap[FragMemoryIndex] = %v, want RoleDeveloper", role)
	}

	// Sanity: RenderIndex's output is exactly the fragment payload a caller
	// would tag with that role — it carries no role of its own (roles are a
	// prompt-assembly concept, contracts.Role, not something this package
	// invents), so this test also documents that the memory package's
	// output is role-agnostic text meant to be wrapped at RoleDeveloper by
	// the composer, not something it self-asserts.
	frag := RenderIndex(mkEntries(1), Budget(0))
	if frag.Text == "" {
		t.Fatal("RenderIndex produced empty text for a non-empty entry set")
	}
}
