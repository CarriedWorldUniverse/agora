package approval

// Regression tests for the U7 review gate (security-validator + Sonnet +
// DeepSeek-v4-pro — all three independently confirmed the two question
// fail-opens and the ScopeHost kind gap).

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// alwaysMatchStore is a ScopeStore stub whose Match always reports a grant, to
// prove Decide never short-circuits a question to allow even when the store
// would match.
type alwaysMatchStore struct{}

func (alwaysMatchStore) Grant(ScopeAllow) error { return nil }
func (alwaysMatchStore) Match(kind contracts.ApprovalKind, sessionID, scopeKey string) (ScopeAllow, bool) {
	return ScopeAllow{Kind: kind, Scope: contracts.ScopeSession, Key: sessionID, By: "device:evil"}, true
}

// HIGH #1 (security-validator, DeepSeek) — a question under PolicyAuto must
// never fabricate an allow (invariant 2: never synthesizes an answer).
func TestDecide_QuestionAutoNeverAllows(t *testing.T) {
	r := Decide(contracts.PolicySet{contracts.KindQuestion: contracts.PolicyAuto},
		Request{Kind: contracts.KindQuestion}, nil)
	if r.Action == ActionAllow || r.Action == ActionDeny {
		t.Fatalf("question=auto resolved to %q, want ask/convert (never allow/deny)", r.Action)
	}
}

// HIGH #2 (Sonnet, security-validator, DeepSeek) — a question under
// PolicyPrompt must never short-circuit to allow via a scope grant.
func TestDecide_QuestionPromptScopeNeverAllows(t *testing.T) {
	r := Decide(contracts.PolicySet{contracts.KindQuestion: contracts.PolicyPrompt},
		Request{Kind: contracts.KindQuestion, SessionID: "s1"}, alwaysMatchStore{})
	if r.Action == ActionAllow {
		t.Fatalf("question+prompt+matching-scope resolved to allow (fabricated answer): %+v", r)
	}
}

// Defense in depth — a question kind has no allow/deny scope semantics (spec
// §3: renders as a question card, not the allow/deny modal), so Grant must
// refuse to persist a question-kind scope allow.
func TestGrant_RejectsQuestionKind(t *testing.T) {
	s := NewMemScopeStore()
	err := s.Grant(ScopeAllow{Kind: contracts.KindQuestion, Scope: contracts.ScopeSession, Key: "s1", By: "x"})
	if err == nil {
		t.Fatal("Grant accepted a question-kind scope allow, want rejection")
	}
}

// MED (all three) — ScopeHost is network-only (spec §1); Grant must reject it
// for a non-network kind, symmetric to ScopePrefix's exec-only check.
func TestGrant_HostRequiresEscalationKind(t *testing.T) {
	s := NewMemScopeStore()
	if err := s.Grant(ScopeAllow{Kind: contracts.KindExec, Scope: contracts.ScopeHost, Key: "evil.example.com", By: "x"}); err == nil {
		t.Fatal("Grant accepted a host allow for KindExec, want ErrScopeKindMismatch")
	}
	// The network kind (escalation) is allowed.
	if err := s.Grant(ScopeAllow{Kind: contracts.KindEscalation, Scope: contracts.ScopeHost, Key: "ok.example.com", By: "x"}); err != nil {
		t.Fatalf("Grant rejected a legitimate host allow for KindEscalation: %v", err)
	}
}
