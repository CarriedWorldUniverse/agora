package pod

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
)

// Provision makes a blank pod specific (§6a): validates the ENTIRE message
// against the provisioning device's capability grant and per-device scope
// constraints, resolves the identity, and only then applies any state —
// apply-all-or-reject (§6a: "Provisioning is atomic"). Every failure path
// below returns before any mutation (store write, session start, or Pod
// state change), so a rejected provision leaves the pod exactly as blank as
// it was before the call.
//
// This is the provision/dispatch boundary U16's capability.go HANDOFF NOTE
// names: CheckProfile gates msg.Profile and CheckThread gates
// msg.Session.Resume against device's registry-granted
// AllowedProfiles/AllowedThreads constraints — a device scoped to
// "aspect-builder wearing thread T" cannot provision a pod into a different
// profile or a different thread no matter what admin/interactive
// capabilities it otherwise holds (constraints only narrow, never widen,
// remote §4). Both checks are fail-closed: any error from either refuses
// the WHOLE provision.
func (p *Pod) Provision(ctx context.Context, device remote.Device, msg contracts.Provision) (ProvisionedInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state != StateBlank {
		return ProvisionedInfo{}, ErrAlreadyProvisioned
	}

	// 1. Capability: does this device even hold admin (contracts §4:
	// "admin: ... provision")? Defense-in-depth alongside whatever session
	// transport already enforced RequiredForInput(InProvision) == CapAdmin.
	if err := remote.CheckInput(device, contracts.InProvision); err != nil {
		return ProvisionedInfo{}, err
	}

	if msg.Profile == "" {
		return ProvisionedInfo{}, fmt.Errorf("%w: profile is required", ErrInvalidProvision)
	}

	// 2. Profile scope — CheckProfile (U16 handoff, fail-closed).
	if err := remote.CheckProfile(device, msg.Profile); err != nil {
		return ProvisionedInfo{}, err
	}

	// 3. Session shape: exactly one of New/Resume.
	if msg.Session.New == (msg.Session.Resume != "") {
		return ProvisionedInfo{}, fmt.Errorf("%w: session must set exactly one of new or resume", ErrInvalidProvision)
	}

	var threadID string
	if msg.Session.Resume != "" {
		threadID = msg.Session.Resume
		// 4. Thread scope — CheckThread (U16 handoff, fail-closed).
		if err := remote.CheckThread(device, threadID); err != nil {
			return ProvisionedInfo{}, err
		}
		if _, err := p.store.Meta(threadID); err != nil {
			return ProvisionedInfo{}, fmt.Errorf("%w: %q: %v", ErrResumeThreadUnknown, threadID, err)
		}
	} else {
		// 4b. A thread-scoped device (non-empty AllowedThreads) is leashed to
		// specific existing threads — it must NOT mint a brand-new thread via
		// session.new (a fresh random ID can never be in its allow-list, so
		// CheckThread on the resume path would refuse it; session.new would
		// otherwise silently escape the leash). Fail-closed, matching the
		// file-header invariant "cannot provision a pod into a different
		// thread no matter what". An unconstrained device (empty
		// AllowedThreads) is unaffected.
		if len(device.Constraints.AllowedThreads) > 0 {
			return ProvisionedInfo{}, fmt.Errorf("%w: thread-scoped device may not create a new thread via session.new", remote.ErrThreadNotAllowed)
		}
	}

	// 5. Identity resolution — must succeed before any mutation; a pod
	// cannot become "<aspect> wearing <profile>" without a real identity.
	if msg.Identity.Source == "" {
		return ProvisionedInfo{}, fmt.Errorf("%w: identity.source is required", ErrInvalidProvision)
	}
	if p.identities == nil {
		return ProvisionedInfo{}, fmt.Errorf("%w: no identity provider configured", ErrIdentityResolve)
	}
	identity, err := p.identities.Resolve(msg.Identity.Source)
	if err != nil {
		return ProvisionedInfo{}, fmt.Errorf("%w: %v", ErrIdentityResolve, err)
	}

	// 6. Workspace shape (checkout mechanics are a later unit's job — see
	// doc.go's scope note; only the shape is validated here).
	if msg.Workspace != nil && msg.Workspace.Dir == "" {
		return ProvisionedInfo{}, fmt.Errorf("%w: workspace.dir is required when workspace is set", ErrInvalidProvision)
	}

	if p.engineFactory == nil {
		return ProvisionedInfo{}, fmt.Errorf("%w: no engine factory configured", ErrInvalidProvision)
	}

	// --- Every validation passed: apply. Nothing above this line ever
	// wrote to p.store or mutated p.state/p.session/p.attach/p.info. ---

	now := p.clock()
	workDir := ""
	if msg.Workspace != nil {
		workDir = msg.Workspace.Dir
	}

	createdNew := false
	if msg.Session.New {
		id, err := newThreadID()
		if err != nil {
			return ProvisionedInfo{}, fmt.Errorf("pod: generate thread id: %w", err)
		}
		threadID = id
		if err := p.store.Create(contracts.ThreadMeta{
			ThreadID:     threadID,
			CreatedAt:    now,
			IdentityFP:   identity.Fingerprint,
			IdentityName: identity.ID,
			Profile:      msg.Profile,
			WorkingDir:   workDir,
		}); err != nil {
			return ProvisionedInfo{}, fmt.Errorf("pod: create thread: %w", err)
		}
		createdNew = true
	}

	if err := p.store.Append(threadID, []contracts.ThreadItem{{
		TS:       now,
		Type:     contracts.TIProvisioning,
		Identity: identity.Fingerprint,
		Device:   device.ID,
		Payload:  msg,
	}}); err != nil {
		// apply-all-or-reject (§6a): Create + Append are two separate durable
		// writes, not one transaction. If Append fails after we just Created
		// the thread, compensate by deleting it so no orphaned zero-item
		// ThreadMeta is left behind. (Resume path Created nothing, so there's
		// nothing to roll back — the existing thread is untouched.)
		if createdNew {
			_ = p.store.Delete(threadID)
		}
		return ProvisionedInfo{}, fmt.Errorf("pod: append provisioning item: %w", err)
	}

	info := ProvisionedInfo{IdentityFP: identity.Fingerprint, Profile: msg.Profile, ThreadID: threadID}

	inner := p.engineFactory(identity, msg.Profile)
	engine := provisionedEngine{
		inner:   inner,
		payload: mustMarshalEvent(contracts.EvProvisioned, threadID, provisionedPayload{IdentityFP: info.IdentityFP, Profile: info.Profile}),
	}
	// Per-session context derived from baseCtx: Deprovision cancels it (never
	// baseCtx itself, so the pod stays reusable — warm-pool §6a), which both
	// tears the engine goroutine down and unblocks any in-flight RunTurn.
	sessionCtx, sessionCancel := context.WithCancel(p.baseCtx)
	session := agoraio.NewSession(sessionCtx, threadID, engine)
	// replayN > 0: io.Session.Attach snapshots the backlog tail and registers
	// the client atomically under one lock (session.go's FIX 3a), so a small,
	// non-zero replay window guarantees the dispatch attach sees the
	// `provisioned` event exactly once regardless of the (engine-goroutine vs
	// Attach-call) scheduling race — either it's already in the backlog tail
	// by the time Attach snapshots it, or Attach registered first and the
	// live broadcast delivers it. Either way it is never lost.
	attach := session.Attach(remote.AttachInfo(device, "dispatch"), attachReplayWindow)

	p.info = info
	p.session = session
	p.attach = attach
	p.sessionCtx = sessionCtx
	p.sessionCancel = sessionCancel
	p.state = StateProvisioned

	return info, nil
}

// Deprovision clears identity/session/workspace back to blank (§6a:
// "deprovision clears identity/session/workspace back to blank (warm-pool
// reuse — cheaper than pod churn)"). Closing the session drains its engine
// cleanly (io.Session.Close's contract) before the pod reports itself
// blank again, so a caller that immediately re-Provisions never races the
// torn-down session's goroutines.
func (p *Pod) Deprovision() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state != StateProvisioned {
		return ErrNotProvisioned
	}

	// Cancel the per-session context first so any in-flight RunTurn unblocks
	// and a context-blocked engine goroutine can return, THEN Close drains it.
	if p.sessionCancel != nil {
		p.sessionCancel()
	}
	if p.session != nil {
		p.session.Close()
	}

	p.session = nil
	p.attach = nil
	p.sessionCtx = nil
	p.sessionCancel = nil
	p.info = ProvisionedInfo{}
	p.state = StateBlank
	return nil
}

func newThreadID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "pod_" + hex.EncodeToString(b[:]), nil
}
