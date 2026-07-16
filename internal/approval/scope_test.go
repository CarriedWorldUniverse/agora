package approval

import (
	"sync"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestScopeSessionShortCircuits: an "allow for this session" grant persists
// and short-circuits a later matching prompt-policy request in the same
// session, without asking again.
func TestScopeSessionShortCircuits(t *testing.T) {
	store := NewMemScopeStore()
	ps := contracts.PolicySet{contracts.KindExec: contracts.PolicyPrompt}

	// Before any grant: asks.
	got := Decide(ps, Request{ID: "r1", Kind: contracts.KindExec, SessionID: "th1", ScopeKey: "git status"}, store)
	if got.Action != ActionAsk {
		t.Fatalf("pre-grant: got %q want ask", got.Action)
	}

	if err := store.Grant(ScopeAllow{Kind: contracts.KindExec, Scope: contracts.ScopeSession, Key: "th1", By: "device:phone1"}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Same session, any exec command: short-circuits to allow.
	got = Decide(ps, Request{ID: "r2", Kind: contracts.KindExec, SessionID: "th1", ScopeKey: "rm -rf /tmp/x"}, store)
	if got.Action != ActionAllow {
		t.Fatalf("post-grant same session: got %q want allow", got.Action)
	}
	if got.Scope != contracts.ScopeSession {
		t.Errorf("scope: got %q want %q", got.Scope, contracts.ScopeSession)
	}
	if got.By != "device:phone1" {
		t.Errorf("by: got %q want device:phone1", got.By)
	}
	if got.Stage != contracts.StageApprover {
		t.Errorf("stage: got %q want %q (the original grant's stage)", got.Stage, contracts.StageApprover)
	}

	// Persists across repeated calls (not one-shot consumed).
	got = Decide(ps, Request{ID: "r3", Kind: contracts.KindExec, SessionID: "th1", ScopeKey: "echo hi"}, store)
	if got.Action != ActionAllow {
		t.Fatalf("second post-grant call: got %q want allow (must persist, not be consumed)", got.Action)
	}

	// Different session: no match, asks.
	got = Decide(ps, Request{ID: "r4", Kind: contracts.KindExec, SessionID: "th2", ScopeKey: "echo hi"}, store)
	if got.Action != ActionAsk {
		t.Fatalf("different session: got %q want ask", got.Action)
	}
}

// TestScopePrefixExactMatchOnly: a scope=prefix allow matches narrowly on
// the EXACT scope key it was granted for — never a substring/HasPrefix
// containment check. This is the deliberate, documented resolution of the
// "prefix" scope's matching semantics (see scope.go doc comment): the
// caller is responsible for deriving/normalizing the prefix key before
// calling Decide, so this package never has to reason about command
// tokenization or risk a naive-prefix escape (e.g. a stored "rm" wrongly
// matching "rm -rf /").
func TestScopePrefixExactMatchOnly(t *testing.T) {
	store := NewMemScopeStore()
	if err := store.Grant(ScopeAllow{Kind: contracts.KindExec, Scope: contracts.ScopePrefix, Key: "git", By: "device:phone1"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	ps := contracts.PolicySet{contracts.KindExec: contracts.PolicyPrompt}

	got := Decide(ps, Request{ID: "e1", Kind: contracts.KindExec, SessionID: "th1", ScopeKey: "git"}, store)
	if got.Action != ActionAllow {
		t.Fatalf("exact key match: got %q want allow", got.Action)
	}

	got = Decide(ps, Request{ID: "e2", Kind: contracts.KindExec, SessionID: "th1", ScopeKey: "git status"}, store)
	if got.Action != ActionAsk {
		t.Fatalf("longer string starting with the same prefix must NOT match (narrow, exact-key only): got %q want ask", got.Action)
	}
}

// TestScopeKindIsolation: a scope allow for one kind must never match a
// request of a different kind, even with the same session/scope key.
func TestScopeKindIsolation(t *testing.T) {
	store := NewMemScopeStore()
	if err := store.Grant(ScopeAllow{Kind: contracts.KindExec, Scope: contracts.ScopeSession, Key: "th1", By: "d1"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	ps := contracts.PolicySet{contracts.KindEscalation: contracts.PolicyPrompt}
	got := Decide(ps, Request{ID: "k1", Kind: contracts.KindEscalation, SessionID: "th1"}, store)
	if got.Action != ActionAsk {
		t.Fatalf("cross-kind leakage: got %q want ask", got.Action)
	}
}

// TestGrantRejectsOnceScope: "once" is the default, non-persisted scope —
// Grant must reject it (there is nothing to persist).
func TestGrantRejectsOnceScope(t *testing.T) {
	store := NewMemScopeStore()
	err := store.Grant(ScopeAllow{Kind: contracts.KindExec, Scope: contracts.ScopeOnce, Key: "th1", By: "d1"})
	if err == nil {
		t.Fatal("expected error granting a once-scope allow")
	}
}

// TestGrantRejectsPrefixOnNonExec: scope=prefix is exec-only per §1.
func TestGrantRejectsPrefixOnNonExec(t *testing.T) {
	store := NewMemScopeStore()
	err := store.Grant(ScopeAllow{Kind: contracts.KindPatch, Scope: contracts.ScopePrefix, Key: "git", By: "d1"})
	if err == nil {
		t.Fatal("expected error granting a prefix-scope allow for a non-exec kind")
	}
}

// TestGrantRejectsEmptyKey: an empty scope key can never be matched
// narrowly/exactly, so it must be rejected rather than silently stored as
// a wildcard.
func TestGrantRejectsEmptyKey(t *testing.T) {
	store := NewMemScopeStore()
	err := store.Grant(ScopeAllow{Kind: contracts.KindExec, Scope: contracts.ScopeSession, Key: "", By: "d1"})
	if err == nil {
		t.Fatal("expected error granting an empty scope key")
	}
}

// TestScopeStoreConcurrentSafe exercises Grant/Match from concurrent
// goroutines under -race.
func TestScopeStoreConcurrentSafe(t *testing.T) {
	store := NewMemScopeStore()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = store.Grant(ScopeAllow{Kind: contracts.KindExec, Scope: contracts.ScopeSession, Key: "th-race", By: "d"})
		}(i)
		go func(i int) {
			defer wg.Done()
			_, _ = store.Match(contracts.KindExec, "th-race", "")
		}(i)
	}
	wg.Wait()
}
