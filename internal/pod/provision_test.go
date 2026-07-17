package pod

import (
	"context"
	"errors"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
)

func assertBlankNoMutation(t *testing.T, p *Pod, store contracts.ThreadStore) {
	t.Helper()
	if got := p.State(); got != StateBlank {
		t.Errorf("state after rejected provision: got %q, want %q (a rejected provision must leave the pod blank)", got, StateBlank)
	}
	if got := p.Info(); got != (ProvisionedInfo{}) {
		t.Errorf("info after rejected provision: got %+v, want zero value", got)
	}
	threads, err := store.List(contracts.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(threads) != 0 {
		t.Errorf("store has %d thread(s) after a rejected provision — apply-all-or-reject violated (partial state leaked)", len(threads))
	}
}

// TestProvision_Atomic_ProfileScopeRefused: a device whose AllowedProfiles
// constraint doesn't include the requested profile is REFUSED — the U16
// CheckProfile handoff, called at the provision boundary — and the refusal
// leaves no partial state (apply-all-or-reject).
func TestProvision_Atomic_ProfileScopeRefused(t *testing.T) {
	ctx := context.Background()
	p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})

	device := dispatchDevice("disp1", remote.DeviceConstraints{AllowedProfiles: []string{"aspect-reviewer"}})
	msg := validNewProvision("aspect-builder")

	_, err := p.Provision(ctx, device, msg)
	if !errors.Is(err, remote.ErrProfileNotAllowed) {
		t.Fatalf("Provision error = %v, want wrapping remote.ErrProfileNotAllowed", err)
	}
	assertBlankNoMutation(t, p, store)
}

// TestProvision_Atomic_ThreadScopeRefused: a device whose AllowedThreads
// constraint doesn't include the resume target is REFUSED (CheckThread
// handoff) before ever looking the thread up in the store, with zero
// partial state.
func TestProvision_Atomic_ThreadScopeRefused(t *testing.T) {
	ctx := context.Background()
	p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})

	// A thread that genuinely exists, so a pass here would prove the scope
	// check (not a bogus-thread-id check) is what refuses.
	if err := store.Create(contracts.ThreadMeta{ThreadID: "thread-other", Profile: "aspect-builder"}); err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	device := dispatchDevice("disp2", remote.DeviceConstraints{AllowedThreads: []string{"thread-mine"}})
	msg := contracts.Provision{Profile: "aspect-builder", Session: contracts.ProvisionSession{Resume: "thread-other"}}
	msg.Identity.Source = "keyring:anvil"

	_, err := p.Provision(ctx, device, msg)
	if !errors.Is(err, remote.ErrThreadNotAllowed) {
		t.Fatalf("Provision error = %v, want wrapping remote.ErrThreadNotAllowed", err)
	}
	// Pod-level state must be untouched (a pre-seeded thread exists on
	// purpose here, so the generic "zero threads" assertion doesn't apply —
	// checked directly instead of via assertBlankNoMutation).
	if got := p.State(); got != StateBlank {
		t.Errorf("state after rejected provision: got %q, want %q", got, StateBlank)
	}
	if got := p.Info(); got != (ProvisionedInfo{}) {
		t.Errorf("info after rejected provision: got %+v, want zero value", got)
	}

	// The seeded thread itself must be untouched — no provisioning item
	// appended to someone else's thread on a refused cross-scope attempt.
	items := threadItems(t, store, "thread-other")
	if len(items) != 0 {
		t.Errorf("thread-other has %d item(s) after a refused provision — should be untouched", len(items))
	}
}

// TestProvision_Atomic_MalformedSessionShape covers both "neither New nor
// Resume set" and "both set" — session.go's spec-required exactly-one-of.
func TestProvision_Atomic_MalformedSessionShape(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		session contracts.ProvisionSession
	}{
		{"neither", contracts.ProvisionSession{}},
		{"both", contracts.ProvisionSession{New: true, Resume: "thread-x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})
			device := dispatchDevice("disp3", remote.DeviceConstraints{})
			msg := contracts.Provision{Profile: "aspect-builder", Session: c.session}
			msg.Identity.Source = "keyring:anvil"

			_, err := p.Provision(ctx, device, msg)
			if !errors.Is(err, ErrInvalidProvision) {
				t.Fatalf("Provision error = %v, want wrapping ErrInvalidProvision", err)
			}
			assertBlankNoMutation(t, p, store)
		})
	}
}

// TestProvision_Atomic_IdentityResolutionFailure: an unresolvable identity
// source rejects the whole provision even though profile/session validation
// already passed — no thread gets created for an identity that never
// resolved.
func TestProvision_Atomic_IdentityResolutionFailure(t *testing.T) {
	ctx := context.Background()
	p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})

	device := dispatchDevice("disp4", remote.DeviceConstraints{})
	msg := contracts.Provision{Profile: "aspect-builder", Session: contracts.ProvisionSession{New: true}}
	msg.Identity.Source = "keyring:nonexistent"

	_, err := p.Provision(ctx, device, msg)
	if !errors.Is(err, ErrIdentityResolve) {
		t.Fatalf("Provision error = %v, want wrapping ErrIdentityResolve", err)
	}
	assertBlankNoMutation(t, p, store)
}

// TestProvision_Atomic_ResumeUnknownThread: session.resume naming a thread
// the store has never heard of rejects atomically (distinct from the scope
// refusal above — here the device is unconstrained but the thread simply
// doesn't exist).
func TestProvision_Atomic_ResumeUnknownThread(t *testing.T) {
	ctx := context.Background()
	p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})

	device := dispatchDevice("disp5", remote.DeviceConstraints{})
	msg := contracts.Provision{Profile: "aspect-builder", Session: contracts.ProvisionSession{Resume: "ghost-thread"}}
	msg.Identity.Source = "keyring:anvil"

	_, err := p.Provision(ctx, device, msg)
	if !errors.Is(err, ErrResumeThreadUnknown) {
		t.Fatalf("Provision error = %v, want wrapping ErrResumeThreadUnknown", err)
	}
	assertBlankNoMutation(t, p, store)
}

// TestProvision_Atomic_MissingProfile: an empty profile is rejected before
// any state changes (CheckProfile is never even reached with a blank
// profile — reject at the shape-validation stage).
func TestProvision_Atomic_MissingProfile(t *testing.T) {
	ctx := context.Background()
	p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})

	device := dispatchDevice("disp6", remote.DeviceConstraints{})
	msg := contracts.Provision{Session: contracts.ProvisionSession{New: true}}
	msg.Identity.Source = "keyring:anvil"

	_, err := p.Provision(ctx, device, msg)
	if !errors.Is(err, ErrInvalidProvision) {
		t.Fatalf("Provision error = %v, want wrapping ErrInvalidProvision", err)
	}
	assertBlankNoMutation(t, p, store)
}

// TestProvision_Atomic_MissingWorkspaceDir: a set-but-empty workspace.dir is
// a malformed shape, refused before mutation.
func TestProvision_Atomic_MissingWorkspaceDir(t *testing.T) {
	ctx := context.Background()
	p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})

	device := dispatchDevice("disp7", remote.DeviceConstraints{})
	msg := validNewProvision("aspect-builder")
	msg.Workspace = &contracts.ProvisionWorkspace{}

	_, err := p.Provision(ctx, device, msg)
	if !errors.Is(err, ErrInvalidProvision) {
		t.Fatalf("Provision error = %v, want wrapping ErrInvalidProvision", err)
	}
	assertBlankNoMutation(t, p, store)
}

// TestProvision_Atomic_NonAdminDeviceRefused: RequiredForInput(InProvision)
// is CapAdmin (contracts §4) — a device without admin cannot provision at
// all, checked via remote.CheckInput before anything else.
func TestProvision_Atomic_NonAdminDeviceRefused(t *testing.T) {
	ctx := context.Background()
	p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})

	device := remote.Device{ID: "disp8", Capabilities: []contracts.Capability{contracts.CapInteractive, contracts.CapObserver}}
	msg := validNewProvision("aspect-builder")

	_, err := p.Provision(ctx, device, msg)
	if !errors.Is(err, remote.ErrCapabilityDenied) {
		t.Fatalf("Provision error = %v, want wrapping remote.ErrCapabilityDenied", err)
	}
	assertBlankNoMutation(t, p, store)
}

// TestProvision_Success_NewSession: the happy path — unconstrained device,
// valid message, session.new — applies atomically: pod becomes provisioned,
// a thread is created carrying the resolved identity/profile, and a
// TIProvisioning audit item records both identity and device attribution.
func TestProvision_Success_NewSession(t *testing.T) {
	ctx := context.Background()
	p, store := newTestPod(t, ctx, &agoraio.ScriptedEngine{})

	device := dispatchDevice("disp-ok", remote.DeviceConstraints{})
	msg := validNewProvision("aspect-builder")

	info, err := p.Provision(ctx, device, msg)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if info.IdentityFP != "agora:anvilfp" || info.Profile != "aspect-builder" || info.ThreadID == "" {
		t.Fatalf("unexpected ProvisionedInfo: %+v", info)
	}
	if got := p.State(); got != StateProvisioned {
		t.Fatalf("state = %q, want %q", got, StateProvisioned)
	}
	if got := p.Info(); got != info {
		t.Fatalf("Info() = %+v, want %+v", got, info)
	}

	meta, err := store.Meta(info.ThreadID)
	if err != nil {
		t.Fatalf("store.Meta: %v", err)
	}
	if meta.IdentityFP != "agora:anvilfp" || meta.Profile != "aspect-builder" {
		t.Errorf("thread meta = %+v, want identity/profile from provisioning", meta)
	}

	items := threadItems(t, store, info.ThreadID)
	found := false
	for _, it := range items {
		if it.Type == contracts.TIProvisioning {
			found = true
			if it.Identity != "agora:anvilfp" || it.Device != "disp-ok" {
				t.Errorf("provisioning item attribution = identity=%q device=%q, want anvilfp/disp-ok", it.Identity, it.Device)
			}
		}
	}
	if !found {
		t.Errorf("no TIProvisioning item found in thread %s: %+v", info.ThreadID, items)
	}
}

// TestProvision_AlreadyProvisioned_Refused: a second Provision on an already
// -provisioned pod is refused (must Deprovision first).
func TestProvision_AlreadyProvisioned_Refused(t *testing.T) {
	ctx := context.Background()
	p, _ := newTestPod(t, ctx, &agoraio.ScriptedEngine{})
	device := dispatchDevice("disp-again", remote.DeviceConstraints{})

	if _, err := p.Provision(ctx, device, validNewProvision("aspect-builder")); err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	if _, err := p.Provision(ctx, device, validNewProvision("aspect-builder")); !errors.Is(err, ErrAlreadyProvisioned) {
		t.Fatalf("second Provision error = %v, want ErrAlreadyProvisioned", err)
	}
}

// TestDeprovision_WarmPoolReuse: deprovision clears back to blank (§6a) and
// the same Pod can be re-provisioned with a different profile — warm-pool
// reuse, not pod churn.
func TestDeprovision_WarmPoolReuse(t *testing.T) {
	ctx := context.Background()
	p, _ := newTestPod(t, ctx, &agoraio.ScriptedEngine{})
	device := dispatchDevice("disp-reuse", remote.DeviceConstraints{})

	first, err := p.Provision(ctx, device, validNewProvision("aspect-builder"))
	if err != nil {
		t.Fatalf("first Provision: %v", err)
	}

	if err := p.Deprovision(); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if got := p.State(); got != StateBlank {
		t.Fatalf("state after Deprovision = %q, want %q", got, StateBlank)
	}
	if got := p.Info(); got != (ProvisionedInfo{}) {
		t.Fatalf("Info() after Deprovision = %+v, want zero", got)
	}

	msg := validNewProvision("aspect-reviewer")
	msg.Identity.Source = "keyring:maren"
	second, err := p.Provision(ctx, device, msg)
	if err != nil {
		t.Fatalf("second Provision: %v", err)
	}
	if second.ThreadID == first.ThreadID {
		t.Errorf("second provisioning reused the first thread id %q — session.new must mint a fresh thread", first.ThreadID)
	}
	if second.Profile != "aspect-reviewer" || second.IdentityFP != "agora:marenfp" {
		t.Errorf("second provisioning didn't apply new identity/profile: %+v", second)
	}
}

// TestDeprovision_WhileBlank_Refused: deprovisioning an already-blank pod is
// refused, not a silent no-op — callers must be able to tell "nothing to
// clear" from "cleared".
func TestDeprovision_WhileBlank_Refused(t *testing.T) {
	ctx := context.Background()
	p, _ := newTestPod(t, ctx, &agoraio.ScriptedEngine{})
	if err := p.Deprovision(); !errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("Deprovision on blank pod error = %v, want ErrNotProvisioned", err)
	}
}
