// pod.go: the daemon's minimal pod-mode wiring (blueprint §3.6 / fix
// finding #3) — internal/pod is frozen/merged and this package must not
// touch it, but internal/pod.Pod's own public Go API (NewPod, Provision,
// RunTurn, Deprovision) is fully exported, so the daemon assembly gap isn't
// "no seam exists" — it's that nothing in this package had ever CALLED that
// API using the daemon's own shared dependencies. PodMode closes that gap:
// it constructs a *pod.Pod wired to THIS daemon's clock/store/questions
// (not a fresh independent set a caller builds from scratch), so a bug in
// that wiring — the wrong store, a second independent QuestionLog with its
// own mutex, a stale clock — is now detectable by driving a pod through it,
// exactly the assembly proof blueprint §3.6 wanted.
//
// internal/pod exposes no second-attachment/observer hook (Pod.session is
// unexported) — so this is control-level wiring (construct + Provision +
// RunTurn), not multi-attach streaming. That's a real, separately-scoped
// pod API change this unit does not attempt (doc comment on PodMode below).
package daemon

import (
	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/pod"
)

// PodMode constructs a *pod.Pod that shares THIS daemon's clock, thread
// store, and QuestionLog — the daemon-level pod-mode entry point a dispatch
// controller (or a conformance drive proving daemon assembly, not just
// internal/pod in isolation) calls instead of pod.NewPod directly.
// identities/engineFactory are the two pod.Config dependencies with no
// daemon-level equivalent to default from (a pod's identity resolution and
// per-profile engine are caller-supplied concerns, same as pod.Config
// itself documents).
//
// This is deliberately the control-level seam only: internal/pod's Pod
// exposes no second-attachment/observer hook (Pod.session is unexported,
// and pod is a frozen/merged package this unit must not touch), so a
// caller drives a daemon-hosted pod thread via Provision/RunTurn exactly as
// pod/turn_test.go's own stubBroker does — full multi-attach streaming
// through the daemon's session-protocol wire (ServeConn/HandleWS) would
// need a pod API change that is out of this unit's scope.
func (d *Daemon) PodMode(identities contracts.IdentityProvider, engineFactory pod.EngineFactory) *pod.Pod {
	return pod.NewPod(d.baseCtx, pod.Config{
		Clock:         pod.Clock(d.clock),
		Identities:    identities,
		Store:         d.store,
		Questions:     d.questions,
		EngineFactory: engineFactory,
	})
}
