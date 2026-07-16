package approval

import "errors"

// Sentinel errors returned by the scoped-allow store. Decide itself never
// errors — an unresolvable/malformed request always yields a Result (fail
// closed), never an error, because callers need a decidable value even for
// the worst input. Spec: agora-spec-approvals.md; ground rule 5.
var (
	// ErrScopeNotPersistable is returned by ScopeStore.Grant for
	// contracts.ScopeOnce — "once" is the default, non-persisted scope; there
	// is nothing to store.
	ErrScopeNotPersistable = errors.New("approval: scope \"once\" is not persisted (nothing to grant)")

	// ErrScopeKindMismatch is returned when a scope is granted for a kind the
	// spec does not permit it for (§1: prefix is exec-only).
	ErrScopeKindMismatch = errors.New("approval: scope not valid for this kind")

	// ErrScopeKeyEmpty is returned when Grant is called with an empty key —
	// an empty key can never be matched narrowly/exactly, so it must never
	// be stored as an implicit wildcard.
	ErrScopeKeyEmpty = errors.New("approval: scope key must not be empty")

	// ErrScopeUnknown is returned for a Scope value that is none of
	// session/prefix/host.
	ErrScopeUnknown = errors.New("approval: unknown scope value")
)
