// Package pod — pod mode: blank boot, atomic provision/deprovision,
// dispatch-as-controller, needs-input surfacing to the broker.
//
// Build unit: U17 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-remote.md §6a, docs/spec/agora-spec-planning-questions.md §5/§8.
//
// A Pod is "the same binary" booted blank (agora-spec-remote.md §6a:
// "`agora daemon --pod`: boots blank ... refuses turns until provisioned").
// NewPod starts a Pod in StateBlank; RunTurn refuses with ErrNotProvisioned
// until Provision succeeds. Wiring an actual `--pod` CLI flag into a daemon
// binary is U18's job (internal/daemon is a skeleton only as of this unit;
// cmd/agora is the operator-facing TUI client, not the daemon pod runtime,
// so it is deliberately left untouched here) — NewPod/Provision/Deprovision/
// RunTurn are the seam that flag will call.
//
// Layout:
//
//   - pod.go: Pod's state machine (blank/provisioned), constructor, and
//     the CheckInput/CheckProfile/CheckThread call sites this unit is
//     responsible for wiring (U16's HANDOFF NOTE on capability.go:
//     "U18's profile-switch and thread-attach paths MUST call these" —
//     provision IS the profile-switch/thread-attach decision for a pod,
//     since §6a's provision message carries `profile` and
//     `session.resume` as its only way to select either. This package is
//     that call site).
//   - provision.go: Provision/Deprovision — apply-all-or-reject atomicity
//     (§6a: "Provisioning is atomic: apply-all-or-reject, then
//     `provisioned {identity_fp, profile}` event"). Every validation
//     (capability, profile/thread scope, identity resolution, session
//     shape, workspace shape) runs BEFORE any state mutation or
//     persistence write; the first failure rejects the whole message with
//     zero partial state.
//   - turn.go: RunTurn drives one turn through an io.Session/Attachment
//     like any interactive client (§6a: "dispatch drives it like any
//     interactive client"). It watches the event stream for a blocking
//     question.asked and performs the harness-side ladder conversion
//     (internal/planning's Resolve/Terminate, ContextDispatchPod always
//     resolves to DispositionDieHonestly per planning-questions §5) —
//     returning a typed Blocked result instead of forwarding the model
//     past an unanswered blocking question. "Die honestly" here means the
//     TURN terminates immediately (the work-item lease this unit models);
//     the pod itself stays warm per §6a's lifecycle note ("warm-pool
//     reuse — cheaper than pod churn") until an explicit Deprovision.
//   - errors.go: sentinel errors, all fail-closed (ground rule: every
//     capability/scope decision in this package refuses on error, never
//     falls through to allow).
//
// Explicitly OUT of scope here (later units / deferred, noted so a caller
// doesn't assume more than this package does):
//   - the real turn engine (model calls, tool loop, the harness-intrinsic
//     `question` tool call itself) — io.Engine is an injected seam
//     (EngineFactory); tests drive it with io.ScriptedEngine. This
//     package only performs the CONVERSION once a blocking
//     question.asked event is observed on the wire, per planning-questions
//     §5's "the harness converts the call" language — it does not raise
//     the question itself.
//   - workspace checkout (cairn/git) and MCP overlay interpolation:
//     Provision validates only the SHAPE of contracts.ProvisionWorkspace/
//     ProvisionContext (non-empty dir when set) and records them verbatim
//     on the thread's provisioning audit item; actually running a
//     checkout or resolving `{identity}` interpolation is a later unit's
//     job (workspace/MCP wiring), not modeled here.
//   - the nexus-side dispatch `needs_input` re-dispatch contract
//     (planning-questions §8: "this is a nexus change, not an agora
//     one") — this package produces contracts.BlockedNeedsInput; routing
//     it back into a re-dispatched work item is out of the repo.
package pod
