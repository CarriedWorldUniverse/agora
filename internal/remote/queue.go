// Queue implements the stage-3+ approver queue and timeout fallback (spec
// §8): "fan-out to attached approver clients (first answer wins) → queue +
// push doorbell → approval_timeout fallback (default deny)." This package
// only models the queue/timeout state machine, not the fan-out (io's
// Session already does first-answer-wins fan-out, spec §0a) or the push
// notifier (v1.1, out of scope).
package remote

import (
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// DefaultApprovalTimeout is spec §8's default: "default deny after 15
// min".
const DefaultApprovalTimeout = 15 * time.Minute

// queueEntry is one queued approval/question awaiting resolution.
type queueEntry struct {
	kind       contracts.ApprovalKind
	enqueuedAt time.Time
	resolved   bool
	parked     bool
}

// Queue is the daemon-side approval queue for one profile/daemon's
// timeout policy. Safe for concurrent use.
type Queue struct {
	clock   Clock
	timeout time.Duration

	mu      sync.Mutex
	entries map[string]*queueEntry
}

// NewQueue builds a Queue with the given timeout (DefaultApprovalTimeout if
// zero) and clock (time.Now if nil).
func NewQueue(clock Clock, timeout time.Duration) *Queue {
	if clock == nil {
		clock = time.Now
	}
	if timeout <= 0 {
		timeout = DefaultApprovalTimeout
	}
	return &Queue{clock: clock, timeout: timeout, entries: make(map[string]*queueEntry)}
}

// Enqueue adds id (kind k) to the queue at the current clock time. Refuses
// a duplicate id still active in the queue (ErrApprovalAlreadyQueued) —
// the caller (the pipeline that raised approval.requested) owns id
// uniqueness; the queue just refuses to silently clobber an existing
// entry's clock.
func (q *Queue) Enqueue(id string, k contracts.ApprovalKind) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.entries[id]; ok {
		return ErrApprovalAlreadyQueued
	}
	q.entries[id] = &queueEntry{kind: k, enqueuedAt: q.clock()}
	return nil
}

// Resolve answers a permission-shaped entry (exec/patch/escalation/
// mcp_tool/plan/gate) with decision, gated on device holding the
// capability contracts.RequiredForApproval(kind) demands (and any
// per-device AllowedApprovalKinds constraint — CheckApproval, spec §4).
// Refuses contracts.KindQuestion (ErrQuestionNeedsAnswer — use
// AnswerQuestion), an unknown id (ErrApprovalUnknown), and a
// previously-resolved id (ErrApprovalAlreadyResolved — first-answer-wins,
// matching io's session-level arbitration, spec §0a/§5).
func (q *Queue) Resolve(id string, device Device, decision contracts.Decision, message string) (contracts.ApprovalResolution, error) {
	q.mu.Lock()
	e, ok := q.entries[id]
	if !ok {
		q.mu.Unlock()
		return contracts.ApprovalResolution{}, ErrApprovalUnknown
	}
	if e.kind == contracts.KindQuestion {
		q.mu.Unlock()
		return contracts.ApprovalResolution{}, ErrQuestionNeedsAnswer
	}
	if e.resolved {
		q.mu.Unlock()
		return contracts.ApprovalResolution{}, ErrApprovalAlreadyResolved
	}
	kind := e.kind
	q.mu.Unlock()

	if err := CheckApproval(device, kind); err != nil {
		return contracts.ApprovalResolution{}, err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	// Re-check under the lock: another Resolve could have raced between
	// the unlock above (needed so CheckApproval never runs while holding
	// q.mu) and here.
	if e.resolved {
		return contracts.ApprovalResolution{}, ErrApprovalAlreadyResolved
	}
	e.resolved = true
	return contracts.ApprovalResolution{
		ID:       id,
		Decision: decision,
		Scope:    contracts.ScopeOnce,
		Message:  message,
		By:       device.ID,
		Stage:    contracts.StageApprover,
	}, nil
}

// AnswerQuestion answers a contracts.KindQuestion entry with a structured
// answer, gated on device holding CapInteractive (spec §4: "interactive
// ... also answers question-kind cards"). The returned contracts.Answer
// stamps By from the AUTHENTICATED device — never from answer itself,
// which carries no attribution field to forge (contracts.AnswerInput).
func (q *Queue) AnswerQuestion(id string, device Device, answer contracts.AnswerInput) (contracts.Answer, error) {
	q.mu.Lock()
	e, ok := q.entries[id]
	if !ok {
		q.mu.Unlock()
		return contracts.Answer{}, ErrApprovalUnknown
	}
	if e.kind != contracts.KindQuestion {
		q.mu.Unlock()
		return contracts.Answer{}, ErrNotAQuestion
	}
	if e.resolved {
		q.mu.Unlock()
		return contracts.Answer{}, ErrApprovalAlreadyResolved
	}
	q.mu.Unlock()

	if err := CheckApproval(device, contracts.KindQuestion); err != nil {
		return contracts.Answer{}, err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if e.resolved {
		return contracts.Answer{}, ErrApprovalAlreadyResolved
	}
	e.resolved = true
	e.parked = false
	return contracts.Answer{AnswerInput: answer, By: device.ID}, nil
}

// Sweep advances every unresolved entry against now and returns the
// timeout-fallback resolutions for whichever permission-shaped entries
// just crossed q.timeout (spec §8: "approval_timeout fallback (default
// deny)... the timeout recorded as the decision reason"). A
// contracts.KindQuestion entry NEVER appears in the returned slice — it is
// instead marked parked (idempotent; Sweep may be called repeatedly) and
// stays queued indefinitely, per invariant 2 (never deny-fabricate a
// question) and spec §8's explicit exception. Sweep is the caller's poll
// point (e.g. a periodic timer in the daemon, U18) — this package has no
// internal goroutine/timer of its own (ground rule 4: no wall-clock
// inside the package; the caller drives time).
func (q *Queue) Sweep(now time.Time) []contracts.ApprovalResolution {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []contracts.ApprovalResolution
	for id, e := range q.entries {
		if e.resolved {
			continue
		}
		if now.Sub(e.enqueuedAt) < q.timeout {
			continue
		}
		if e.kind == contracts.KindQuestion {
			e.parked = true
			continue
		}
		e.resolved = true
		out = append(out, contracts.ApprovalResolution{
			ID:       id,
			Decision: contracts.DecisionDeny,
			By:       "timeout",
			Stage:    contracts.StageTimeout,
			Message:  "approval: timed out, default deny (remote §8)",
		})
	}
	return out
}

// Parked reports whether id is a question that has parked (timed out
// without an answer — thread waiting-on-answer, spec §8 exception).
func (q *Queue) Parked(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.entries[id]
	return ok && e.parked
}

// Resolved reports whether id has a final resolution (permission-shaped
// deny/allow or a question answer) — false for a still-pending or
// parked-but-unanswered question.
func (q *Queue) Resolved(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.entries[id]
	return ok && e.resolved
}
