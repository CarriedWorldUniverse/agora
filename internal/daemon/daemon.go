package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/approval"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
	"github.com/CarriedWorldUniverse/agora/internal/subagent"
)

// Clock returns the current time. Injected everywhere this package would
// otherwise call time.Now, matching the house pattern (pod.Clock,
// remote.Clock, subagent.Clock) — no wall-clock in tests.
type Clock func() time.Time

// Config is NewDaemon's dependency set, mirroring pod.Config's shape
// (internal/pod/pod.go). Only EngineFactory is required; every other field
// defaults to an in-memory/permissive/no-op implementation so a daemon is
// usable in tests with minimal setup, while still being able to accept the
// real internal/persistence.LocalStore, internal/remote.Registry, etc. for
// production wiring or a durability-focused test (§3.3).
type Config struct {
	Clock         Clock
	Store         contracts.ThreadStore
	Identities    contracts.IdentityProvider
	Registry      *remote.Registry
	Scopes        approval.ScopeStore
	Policy        contracts.PolicySet
	Subagents     *subagent.Manager
	EngineFactory EngineFactory
}

// Daemon is the assembled runtime: a thread registry (SessionLookup) over
// io.Session, wired to the real approval/planning/persistence/subagent
// seams. Safe for concurrent use.
type Daemon struct {
	clock      Clock
	store      contracts.ThreadStore
	identities contracts.IdentityProvider
	registry   *remote.Registry
	questions  *planning.QuestionLog
	plans      *planning.PlanLog
	scopes     approval.ScopeStore
	policy     contracts.PolicySet
	subagents  *subagent.Manager
	engineFor  EngineFactory
	baseCtx    context.Context

	by *byLookup

	mu       sync.Mutex
	sessions map[string]*agoraio.Session
}

var _ agoraio.SessionLookup = (*Daemon)(nil)

// NewDaemon builds a Daemon over cfg. ctx is the daemon's lifetime context —
// canceling it tears down every live Session the next time Close is called
// on them (Close itself is what actually cancels each session).
func NewDaemon(ctx context.Context, cfg Config) *Daemon {
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	store := cfg.Store
	if store == nil {
		store = persistence.NewMemStore()
	}
	scopes := cfg.Scopes
	if scopes == nil {
		scopes = approval.NewMemScopeStore()
	}
	policy := cfg.Policy
	if policy == nil {
		policy = contracts.BuiltinPresets()[contracts.PresetPrompt]
	}
	subagents := cfg.Subagents
	if subagents == nil {
		subagents = subagent.NewManager(store, subagent.NewMemGraphStore(), subagent.NewRegistry(nil), noopRunner{})
	}
	return &Daemon{
		clock:      clock,
		store:      store,
		identities: cfg.Identities,
		registry:   cfg.Registry,
		questions:  planning.NewQuestionLog(store),
		plans:      planning.NewPlanLog(store),
		scopes:     scopes,
		policy:     policy,
		subagents:  subagents,
		engineFor:  cfg.EngineFactory,
		baseCtx:    ctx,
		by:         newByLookup(),
		sessions:   make(map[string]*agoraio.Session),
	}
}

// noopRunner is the default subagent.AgentRunner a Daemon built without an
// explicit one uses — U18 wires the seam (RegisterRoot, Spawn), not agent
// EXECUTION (subagent/doc.go's own scope boundary: that's the turn-engine's
// job, another unit).
type noopRunner struct{}

func (noopRunner) Run(ctx context.Context, req subagent.RunRequest) (subagent.RunResult, error) {
	return subagent.RunResult{}, fmt.Errorf("daemon: no agent runner configured (turn-engine seam, out of U18 scope)")
}

// Store returns the daemon's persistence seam.
func (d *Daemon) Store() contracts.ThreadStore { return d.store }

// Questions returns the daemon's shared QuestionLog (park/resume routing).
func (d *Daemon) Questions() *planning.QuestionLog { return d.questions }

// Plans returns the daemon's shared PlanLog.
func (d *Daemon) Plans() *planning.PlanLog { return d.plans }

// Scopes returns the daemon's approval scope store.
func (d *Daemon) Scopes() approval.ScopeStore { return d.scopes }

// Policy returns the daemon's configured policy set.
func (d *Daemon) Policy() contracts.PolicySet { return d.policy }

// Subagents returns the daemon's subagent manager (U10 handoff).
func (d *Daemon) Subagents() *subagent.Manager { return d.subagents }

// Registry returns the daemon's device registry (U16 handoff), or nil if
// none was configured (capability enforcement then falls back to the
// permissive dev-only mode documented on serveConn).
func (d *Daemon) Registry() *remote.Registry { return d.registry }

// CreateThread mints (or, if meta.ThreadID is already set, adopts) a thread:
// it calls store.Create, then RegisterRoot's the thread with the subagent
// manager (§4 U10 handoff) BEFORE returning — a thread that can spawn
// subagents before this call would fail closed on tools per
// subagent.Manager's own fail-closed-unregistered-parent rule; CreateThread
// is what closes that gap for every daemon-minted thread.
func (d *Daemon) CreateThread(meta contracts.ThreadMeta) (string, error) {
	if meta.ThreadID == "" {
		id, err := newThreadID()
		if err != nil {
			return "", fmt.Errorf("daemon: generate thread id: %w", err)
		}
		meta.ThreadID = id
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = d.clock()
	}
	if err := d.store.Create(meta); err != nil {
		return "", fmt.Errorf("daemon: create thread: %w", err)
	}
	d.subagents.RegisterRoot(meta.ThreadID, subagent.ParentContext{
		Cwd:    meta.WorkingDir,
		Policy: d.policy,
		// Tools: nil == unrestricted (subagent.ParentContext's own doc
		// comment) — a daemon-minted root thread is a real interactive/
		// dispatch thread, not an anonymous unregistered one, so it is
		// deliberately NOT narrowed here; a profile wanting a narrower tool
		// set for its subagents is a later wiring's job (agent def
		// allowlists still apply on top via ResolveInheritance).
	})
	return meta.ThreadID, nil
}

// Close tears down every live session this daemon has opened (io.Session's
// own Close contract: cancel + drain). Safe to call once; callers needing
// a fresh daemon over the same store afterward (§3.3 restart drive) should
// construct a new Daemon rather than reuse this one.
func (d *Daemon) Close() {
	d.mu.Lock()
	sessions := make([]*agoraio.Session, 0, len(d.sessions))
	for _, s := range d.sessions {
		sessions = append(sessions, s)
	}
	d.sessions = make(map[string]*agoraio.Session)
	d.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
}

func newThreadID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "th_" + hex.EncodeToString(b[:]), nil
}
