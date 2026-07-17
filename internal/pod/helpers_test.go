package pod

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/planning"
	"github.com/CarriedWorldUniverse/agora/internal/remote"
)

// fixedClock is a deterministic Clock — no wall-clock in tests (ground rule).
func fixedClock(t time.Time) Clock {
	return func() time.Time { return t }
}

// testIdentities is a minimal contracts.IdentityProvider stub: known refs
// resolve to a canned Identity; anything else fails (ErrUnknownIdentity),
// exercising Provision's identity-resolution-failure rejection path.
type testIdentities struct {
	known map[string]contracts.Identity
}

var errUnknownIdentity = errors.New("test: unknown identity source")

func (t *testIdentities) Resolve(ref string) (contracts.Identity, error) {
	if id, ok := t.known[ref]; ok {
		return id, nil
	}
	return contracts.Identity{}, fmt.Errorf("%w: %q", errUnknownIdentity, ref)
}

func newTestIdentities() *testIdentities {
	return &testIdentities{known: map[string]contracts.Identity{
		"keyring:anvil": {ID: "anvil", Fingerprint: "agora:anvilfp", Kind: contracts.IdentityAspect, Source: "keyring:anvil"},
		"keyring:maren": {ID: "maren", Fingerprint: "agora:marenfp", Kind: contracts.IdentityAspect, Source: "keyring:maren"},
	}}
}

// constEngineFactory always returns the same io.Engine — good enough for
// tests that don't care which identity/profile built it.
func constEngineFactory(e agoraio.Engine) EngineFactory {
	return func(contracts.Identity, string) agoraio.Engine { return e }
}

// dispatchDevice is the standard fully-privileged controller grant §6a
// describes for the pod's baked-in dispatch enrollment: "broker pubkey +
// capability grant admin + interactive + observer".
func dispatchDevice(id string, constraints remote.DeviceConstraints) remote.Device {
	return remote.Device{
		ID:           id,
		Capabilities: []contracts.Capability{contracts.CapAdmin, contracts.CapInteractive, contracts.CapObserver},
		Constraints:  constraints,
	}
}

// newTestPod wires a Pod over an injected, inspectable MemStore so
// provision-atomicity tests can assert "no partial state" directly against
// the store, plus a fixed clock and the standard test identity provider.
// engine is what every provisioned session's EngineFactory returns
// (constEngineFactory) unless the caller overrides Config fields via opts.
func newTestPod(t *testing.T, ctx context.Context, engine agoraio.Engine) (*Pod, contracts.ThreadStore) {
	t.Helper()
	store := persistence.NewMemStore()
	p := NewPod(ctx, Config{
		Clock:         fixedClock(time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)),
		Identities:    newTestIdentities(),
		Store:         store,
		Questions:     planning.NewQuestionLog(store),
		EngineFactory: constEngineFactory(engine),
	})
	return p, store
}

// threadItems replays threadID's full item log for assertions.
func threadItems(t *testing.T, store contracts.ThreadStore, threadID string) []contracts.ThreadItem {
	t.Helper()
	it, err := store.Resume(threadID)
	if err != nil {
		t.Fatalf("resume %s: %v", threadID, err)
	}
	defer it.Close()
	var out []contracts.ThreadItem
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		out = append(out, item)
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate %s: %v", threadID, err)
	}
	return out
}

func validNewProvision(profile string) contracts.Provision {
	msg := contracts.Provision{Profile: profile, Session: contracts.ProvisionSession{New: true}}
	msg.Identity.Source = "keyring:anvil"
	return msg
}
