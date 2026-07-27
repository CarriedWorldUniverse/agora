package approval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "permissions.json")
}

func execPrefixGrant(key string) ScopeAllow {
	return ScopeAllow{Kind: contracts.KindExec, Scope: contracts.ScopePrefix, Key: key, By: "operator"}
}

// The point of the whole unit: a grant made in one process is honoured by
// the next one.
func TestFileScopeStore_GrantSurvivesReopen(t *testing.T) {
	path, proj := storePath(t), "/work/proj"

	s1, err := OpenFileScopeStore(path, proj)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s1.Grant(execPrefixGrant("go test")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	s2, err := OpenFileScopeStore(path, proj)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := s2.Match(contracts.KindExec, "", "go test"); !ok {
		t.Fatal("grant did not survive reopen — the store is not durable")
	}
}

// Grants are bucketed by project: approving `make deploy` in a scratch repo
// must not approve it in the cluster config.
func TestFileScopeStore_GrantsDoNotLeakAcrossProjects(t *testing.T) {
	path := storePath(t)

	a, _ := OpenFileScopeStore(path, "/work/project-a")
	if err := a.Grant(execPrefixGrant("make deploy")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	b, _ := OpenFileScopeStore(path, "/work/project-b")
	if _, ok := b.Match(contracts.KindExec, "", "make deploy"); ok {
		t.Fatal("a grant made in project-a applied in project-b — grants must not cross projects")
	}
}

// The "*" bucket is the operator's deliberate hand-edited escape hatch.
func TestFileScopeStore_GlobalBucketAppliesEverywhere(t *testing.T) {
	path := storePath(t)
	doc := permissionsFile{
		Version: PermissionsFileVersion,
		Projects: map[string][]persistedGrant{
			GlobalProjectBucket: {{Kind: "exec", Scope: "prefix", Key: "git status", By: "operator"}},
		},
	}
	data, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, proj := range []string{"/work/one", "/work/two"} {
		s, err := OpenFileScopeStore(path, proj)
		if err != nil {
			t.Fatalf("open(%s): %v", proj, err)
		}
		if _, ok := s.Match(contracts.KindExec, "", "git status"); !ok {
			t.Errorf("global grant did not apply in %s", proj)
		}
	}
}

// Grant must never write the global bucket — only an operator hand-edit
// creates one. Otherwise a single approval would silently go system-wide.
func TestFileScopeStore_GrantNeverWritesTheGlobalBucket(t *testing.T) {
	path := storePath(t)
	s, _ := OpenFileScopeStore(path, "/work/proj")
	if err := s.Grant(execPrefixGrant("rm -rf build")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc permissionsFile
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Projects[GlobalProjectBucket]; ok {
		t.Fatal("Grant wrote into the global bucket — an approval must not go system-wide")
	}
}

// Validation is delegated to MemScopeStore; a rejected grant must not reach
// the file either.
func TestFileScopeStore_InvalidGrantIsNotPersisted(t *testing.T) {
	path := storePath(t)
	s, _ := OpenFileScopeStore(path, "/work/proj")

	// host scope is escalation-only; on exec it must be rejected.
	err := s.Grant(ScopeAllow{Kind: contracts.KindExec, Scope: contracts.ScopeHost, Key: "example.com", By: "op"})
	if err == nil {
		t.Fatal("host-scope grant on an exec kind was accepted")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("a rejected grant still created the permissions file")
	}

	// ScopeOnce has nothing to persist.
	if err := s.Grant(ScopeAllow{Kind: contracts.KindExec, Scope: contracts.ScopeOnce, Key: "x", By: "op"}); err == nil {
		t.Fatal("ScopeOnce grant was accepted; it is not persistable")
	}
}

// A hand-edited file with an invalid combination must be rejected on load
// by the same rules, not trusted because it came from disk.
func TestFileScopeStore_HandEditedInvalidGrantIsIgnoredOnLoad(t *testing.T) {
	path := storePath(t)
	doc := permissionsFile{
		Version: PermissionsFileVersion,
		Projects: map[string][]persistedGrant{
			"/work/proj": {
				{Kind: "exec", Scope: "host", Key: "evil.com", By: "hand-edit"}, // invalid combo
				{Kind: "exec", Scope: "prefix", Key: "go build", By: "operator"},
			},
		},
	}
	data, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	s, _ := OpenFileScopeStore(path, "/work/proj")
	if _, ok := s.Match(contracts.KindExec, "", "evil.com"); ok {
		t.Fatal("an invalid hand-edited grant was honoured")
	}
	if _, ok := s.Match(contracts.KindExec, "", "go build"); !ok {
		t.Fatal("a valid grant alongside an invalid one was dropped")
	}
}

// Never fail the session over the permissions file.
func TestFileScopeStore_CorruptFileDegradesWithWarning(t *testing.T) {
	path := storePath(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, warn := OpenFileScopeStore(path, "/work/proj")
	if s == nil {
		t.Fatal("a corrupt file must not prevent building a store")
	}
	if warn == nil {
		t.Fatal("a corrupt file should produce a warning")
	}
	if _, ok := s.Match(contracts.KindExec, "", "anything"); ok {
		t.Fatal("a corrupt file must not yield grants")
	}
	// Still usable going forward.
	if err := s.Grant(execPrefixGrant("go test")); err != nil {
		t.Fatalf("store unusable after a corrupt load: %v", err)
	}
}

// A version we don't understand must be refused, not guessed at — silently
// misreading a permissions file is the wrong failure mode.
func TestFileScopeStore_UnknownVersionIsRefused(t *testing.T) {
	path := storePath(t)
	data := []byte(`{"version":999,"projects":{"/work/proj":[{"kind":"exec","scope":"prefix","key":"rm -rf /","by":"x"}]}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s, warn := OpenFileScopeStore(path, "/work/proj")
	if warn == nil || !strings.Contains(warn.Error(), "version") {
		t.Fatalf("warn = %v; want a version complaint", warn)
	}
	if _, ok := s.Match(contracts.KindExec, "", "rm -rf /"); ok {
		t.Fatal("a grant from an unknown-version file was honoured")
	}
}

func TestFileScopeStore_MissingFileIsNotAWarning(t *testing.T) {
	s, warn := OpenFileScopeStore(storePath(t), "/work/proj")
	if warn != nil {
		t.Fatalf("first run produced a warning: %v", warn)
	}
	if s == nil {
		t.Fatal("no store")
	}
}

func TestFileScopeStore_DuplicateGrantWritesOnce(t *testing.T) {
	path := storePath(t)
	s, _ := OpenFileScopeStore(path, "/work/proj")
	for i := 0; i < 3; i++ {
		if err := s.Grant(execPrefixGrant("go test")); err != nil {
			t.Fatalf("Grant: %v", err)
		}
	}
	data, _ := os.ReadFile(path)
	var doc permissionsFile
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if n := len(doc.Projects["/work/proj"]); n != 1 {
		t.Fatalf("wrote %d copies of the same grant; want 1", n)
	}
}

// A second agora running concurrently must not lose its grants to a stale
// snapshot — persist re-reads under the lock.
func TestFileScopeStore_ConcurrentStoresBothPersist(t *testing.T) {
	path, proj := storePath(t), "/work/proj"
	a, _ := OpenFileScopeStore(path, proj)
	b, _ := OpenFileScopeStore(path, proj)

	if err := a.Grant(execPrefixGrant("go test")); err != nil {
		t.Fatal(err)
	}
	if err := b.Grant(execPrefixGrant("go vet")); err != nil {
		t.Fatal(err)
	}

	final, _ := OpenFileScopeStore(path, proj)
	for _, key := range []string{"go test", "go vet"} {
		if _, ok := final.Match(contracts.KindExec, "", key); !ok {
			t.Errorf("grant %q was lost — a concurrent store overwrote it", key)
		}
	}
}

func TestFileScopeStore_RevokeRemovesFromDiskOnly(t *testing.T) {
	path, proj := storePath(t), "/work/proj"
	s, _ := OpenFileScopeStore(path, proj)
	if err := s.Grant(execPrefixGrant("go test")); err != nil {
		t.Fatal(err)
	}

	removed, err := s.Revoke("exec", "prefix", "go test")
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !removed {
		t.Fatal("Revoke reported nothing removed")
	}
	// Still live in THIS session — tightening mid-session would be a
	// surprising change of behaviour.
	if _, ok := s.Match(contracts.KindExec, "", "go test"); !ok {
		t.Error("Revoke tightened the running session; it should take effect next session")
	}
	// Gone for the next one.
	next, _ := OpenFileScopeStore(path, proj)
	if _, ok := next.Match(contracts.KindExec, "", "go test"); ok {
		t.Error("revoked grant came back in a new session")
	}
}

func TestFileScopeStore_RevokeUnknownGrantIsNotAnError(t *testing.T) {
	s, _ := OpenFileScopeStore(storePath(t), "/work/proj")
	removed, err := s.Revoke("exec", "prefix", "never granted")
	if err != nil || removed {
		t.Fatalf("Revoke(unknown) = (%v, %v); want (false, nil)", removed, err)
	}
}

func TestFileScopeStore_GrantsListsProjectAndGlobal(t *testing.T) {
	path, proj := storePath(t), "/work/proj"
	doc := permissionsFile{
		Version: PermissionsFileVersion,
		Projects: map[string][]persistedGrant{
			GlobalProjectBucket: {{Kind: "exec", Scope: "prefix", Key: "git status", By: "operator"}},
		},
	}
	data, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenFileScopeStore(path, proj)
	if err := s.Grant(execPrefixGrant("go test")); err != nil {
		t.Fatal(err)
	}

	got := s.Grants()
	if len(got) != 2 {
		t.Fatalf("Grants() returned %d; want 2 (one global, one project)", len(got))
	}
	var sawGlobal, sawProject bool
	for _, g := range got {
		if g.Key == "git status" && g.Global {
			sawGlobal = true
		}
		if g.Key == "go test" && !g.Global {
			sawProject = true
		}
	}
	if !sawGlobal || !sawProject {
		t.Fatalf("Grants() did not flag global vs project correctly: %+v", got)
	}
}
