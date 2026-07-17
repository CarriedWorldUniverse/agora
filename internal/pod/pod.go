package pod

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
)

// State is the pod's coarse lifecycle state (§6a: "boots blank ... no
// working identity, no profile, no workspace; refuses turns until
// provisioned").
type State string

const (
	// StateBlank is the boot state: auto-gen'd local key only, no identity/
	// profile/workspace/session. The only thing a blank pod can do is
	// accept a provision message (or be deprovisioned again, a no-op).
	StateBlank State = "blank"
	// StateProvisioned: identity/profile/session are set; RunTurn is live.
	StateProvisioned State = "provisioned"
)

// attachReplayWindow is the replay tail size given to the dispatch
// attachment's io.Session.Attach call in Provision — see the comment there
// for why any small non-zero value closes the race against the
// provisionedEngine's very first (`provisioned`) event.
const attachReplayWindow = 16

// Clock returns the current time. Injected everywhere this package would
// otherwise call time.Now, so provisioning-timestamp tests are deterministic
// (no wall-clock in tests).
type Clock func() time.Time

// EngineFactory builds the io.Engine a freshly-provisioned pod session runs.
// Called once per successful Provision, after every validation has passed —
// the real implementation picks the profile's tool loop/model config; tests
// inject a constant io.ScriptedEngine. identity/profile are the just-applied
// provisioning parameters (§6a: "the pod becomes <aspect> wearing <profile>
// purely through control-plane data").
type EngineFactory func(identity contracts.Identity, profile string) agoraio.Engine

// ProvisionedInfo is what a successful Provision reports back — the same
// shape as the `provisioned {identity_fp, profile}` event (§6a).
type ProvisionedInfo struct {
	IdentityFP string
	Profile    string
	ThreadID   string
}

// Pod is one dispatch-controlled agora pod runtime: a blank-booted state
// machine that a dispatch controller (or, in tests, a stub broker) drives
// through Provision -> RunTurn* -> Deprovision. Spec: agora-spec-remote.md
// §6a. Safe for concurrent use.
type Pod struct {
	clock         Clock
	identities    contracts.IdentityProvider
	store         contracts.ThreadStore
	questions     *planning.QuestionLog
	engineFactory EngineFactory

	// baseCtx is the parent context every provisioned session's Engine.Run
	// derives from; Deprovision/Close cancel the per-session context, never
	// baseCtx itself, so a Pod can be provisioned more than once across its
	// lifetime (warm-pool reuse, §6a lifecycle).
	baseCtx context.Context

	mu      sync.Mutex
	state   State
	info    ProvisionedInfo
	session *agoraio.Session
	attach  *agoraio.Attachment
}

// Config is NewPod's dependency set. Identities and EngineFactory are
// required (a pod cannot resolve an identity or run a turn without them);
// Store/Questions default to a fresh in-memory, non-durable pair when nil —
// fine for an ephemeral pod (§6a: "the pod dies with the task (ephemeral
// identity mode)") but callers wanting cross-restart durability must inject
// a persistence.LocalStore-backed pair explicitly.
type Config struct {
	Clock         Clock
	Identities    contracts.IdentityProvider
	Store         contracts.ThreadStore
	Questions     *planning.QuestionLog
	EngineFactory EngineFactory
}

// NewPod boots a blank pod (§6a: "`agora daemon --pod`: boots blank ...
// refuses turns until provisioned"). ctx is the pod's lifetime context —
// canceling it tears down any live provisioned session.
func NewPod(ctx context.Context, cfg Config) *Pod {
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	store := cfg.Store
	if store == nil {
		store = persistence.NewMemStore()
	}
	questions := cfg.Questions
	if questions == nil {
		questions = planning.NewQuestionLog(store)
	}
	return &Pod{
		clock:         clock,
		identities:    cfg.Identities,
		store:         store,
		questions:     questions,
		engineFactory: cfg.EngineFactory,
		baseCtx:       ctx,
		state:         StateBlank,
	}
}

// State reports the pod's current lifecycle state.
func (p *Pod) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Info reports the last successful provisioning's info (zero value while
// blank).
func (p *Pod) Info() ProvisionedInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

// provisionedEngine wraps the profile's real engine so the FIRST thing a
// freshly-provisioned session's event stream carries is the `provisioned
// {identity_fp, profile}` event (§6a) — before anything the inner engine
// itself emits. This keeps io (already merged, U2) untouched: Session has
// no public "inject an event" API, so the injection point is the Engine
// seam Session already drives.
type provisionedEngine struct {
	inner   agoraio.Engine
	payload contracts.Event
}

func (e provisionedEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	select {
	case out <- e.payload:
	case <-ctx.Done():
		close(out)
		return ctx.Err()
	}
	return e.inner.Run(ctx, in, out)
}

// provisionedPayload is the {identity_fp, profile} shape carried on
// EvProvisioned (§6a).
type provisionedPayload struct {
	IdentityFP string `json:"identity_fp"`
	Profile    string `json:"profile"`
}

func mustMarshalEvent(t contracts.EventType, threadID string, v any) contracts.Event {
	b, err := json.Marshal(v)
	if err != nil {
		panic("pod: marshal payload: " + err.Error())
	}
	return contracts.Event{Type: t, ThreadID: threadID, Payload: b}
}
