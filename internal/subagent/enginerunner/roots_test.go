package enginerunner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/subagent"
	"github.com/CarriedWorldUniverse/agora/internal/subagent/enginerunner"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	"github.com/CarriedWorldUniverse/agora/internal/turnengine"
	bridle "github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/fake"
)

// outsideTempDir makes a directory that is NOT under any temp root.
//
// This is deliberately not t.TempDir(). Roots ALWAYS contains the temp dirs —
// tempDirs() adds os.TempDir() *and* a hardcoded "/tmp" — so any path under
// /tmp is legitimately inside every Roots value, and a test built from two
// t.TempDir()s cannot distinguish correct roots from wrong ones. Two earlier
// attempts at this test made exactly that mistake and passed with and without
// the fix; narrowing TMPDIR does not help either, because /tmp is hardcoded.
func outsideTempDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir to build a non-temp root in: %v", err)
	}
	dir, err := os.MkdirTemp(home, ".agora-roots-test-")
	if err != nil {
		t.Skipf("cannot create a non-temp dir under %s: %v", home, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// Guard the premise: if this somehow IS inside a temp root, the test
	// proves nothing and should say so rather than pass quietly.
	probe, err := toolrunner.NewRoots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if probe.ContainsLexical(filepath.Join(dir, "x")) {
		t.Skipf("%s is inside a temp root; this test cannot detect the divergence", dir)
	}
	return dir
}

// TestChildInheritsParentRoots covers agora#160: a child's writable roots must
// follow its PARENT's sandbox, not the process cwd.
//
// The child is asked to WRITE a new file inside the parent's tree. Writes are
// the containment-gated surface (reads are not gated at all), and the gate is
// at classify time, so the child's own approval_decision records the verdict:
// "patch" when the path is inside its roots, "escalation" when it is outside.
func TestChildInheritsParentRoots(t *testing.T) {
	parentDir := outsideTempDir(t)
	parentRoots, err := toolrunner.NewRoots(parentDir)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parentDir, "brand-new.txt")
	args, err := json.Marshal(map[string]string{"file_path": target, "content": "child wrote"})
	if err != nil {
		t.Fatal(err)
	}

	provider := fake.NewProvider(
		fake.Step{ToolCalls: []bridle.ToolInvocation{{ID: "1", Name: "Write", Args: args}}},
		fake.Step{Text: "done"},
	)
	store := persistence.NewMemStore()
	if err := store.Create(contracts.ThreadMeta{ThreadID: "root"}); err != nil {
		t.Fatal(err)
	}
	// never-escalate, matching what production children actually get
	// (cmd/agora's subagentProfile, agora#153). This is not incidental: with
	// DevProfile's prompting default, a child whose write lands OUTSIDE its
	// roots raises an escalation, nothing is attached to answer it, and the
	// run parks forever — so the without-the-fix case HANGS instead of
	// failing. That hang is agora#152 reproducing exactly, and it is why the
	// production profile is the right one to test against: it turns the
	// wrong-roots case into a clean deny this test can observe.
	prof := turnengine.DevProfile()
	prof.Policy = contracts.BuiltinPresets()[contracts.PresetNeverEscalate]
	runner := enginerunner.New(provider, store,
		enginerunner.WithProfile(prof),
		enginerunner.WithManagerOption(turnengine.WithRoots(parentRoots)))

	mgr := subagent.NewManager(store, subagent.NewMemGraphStore(), subagent.NewRegistry(nil), runner)
	mgr.RegisterRoot("root", subagent.ParentContext{Cwd: parentDir})
	id, err := mgr.Spawn(context.Background(), "root", "write the file", subagent.SpawnOpts{Foreground: true})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, runErr, ok := mgr.Result(id); !ok || runErr != nil {
		t.Fatalf("Result: ok=%v err=%v", ok, runErr)
	}

	kind, found := writeDecisionKind(t, store, id)
	if !found {
		t.Fatal("no approval_decision for the child's write")
	}
	if kind != string(contracts.KindPatch) {
		t.Errorf("child's write to its PARENT's tree classified %q; want %q — the child is rooted at the process cwd instead of its parent's sandbox (agora#160)", kind, contracts.KindPatch)
	}
}

func writeDecisionKind(t *testing.T, store contracts.ThreadStore, threadID string) (string, bool) {
	t.Helper()
	it, err := store.Resume(threadID)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	for {
		item, ok := it.Next()
		if !ok {
			return "", false
		}
		if item.Type != contracts.TIApprovalDecision {
			continue
		}
		b, err := json.Marshal(item.Payload)
		if err != nil {
			continue
		}
		var line struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(b, &line) == nil && line.Kind != "" {
			return line.Kind, true
		}
	}
}
