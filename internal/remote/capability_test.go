package remote

import (
	"context"
	"errors"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// TestCapabilityEnforcementMatrix: an authenticated device may only
// exercise inputs its granted capabilities permit; over-reach is refused.
// Table-driven over contracts.RequiredForInput.
func TestCapabilityEnforcementMatrix(t *testing.T) {
	tests := []struct {
		name  string
		caps  []contracts.Capability
		input contracts.InputType
		want  bool
	}{
		{"observer sends nothing interactive", []contracts.Capability{contracts.CapObserver}, contracts.InUserMessage, false},
		{"interactive sends user_message", []contracts.Capability{contracts.CapInteractive}, contracts.InUserMessage, true},
		{"interactive sends question_response", []contracts.Capability{contracts.CapInteractive}, contracts.InQuestionResponse, true},
		{"approver alone cannot send user_message", []contracts.Capability{contracts.CapApprover}, contracts.InUserMessage, false},
		{"approver sends approval_response", []contracts.Capability{contracts.CapApprover}, contracts.InApprovalResponse, true},
		{"interactive alone cannot send approval_response", []contracts.Capability{contracts.CapInteractive}, contracts.InApprovalResponse, false},
		{"admin sends provision", []contracts.Capability{contracts.CapAdmin}, contracts.InProvision, true},
		{"interactive cannot send provision (over-reach)", []contracts.Capability{contracts.CapInteractive}, contracts.InProvision, false},
		{"admin sends config", []contracts.Capability{contracts.CapAdmin}, contracts.InConfig, true},
		{"no capabilities at all refuses everything", nil, contracts.InUserMessage, false},
		{
			"approver+interactive both present: each still gates its own input",
			[]contracts.Capability{contracts.CapApprover, contracts.CapInteractive},
			contracts.InApprovalResponse, true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Device{ID: "dev1", Capabilities: tc.caps}
			err := CheckInput(d, tc.input)
			got := err == nil
			if got != tc.want {
				t.Fatalf("CheckInput(%v, %q): got ok=%v (err=%v) want ok=%v", tc.caps, tc.input, got, err, tc.want)
			}
			if !tc.want && !errors.Is(err, ErrCapabilityDenied) {
				t.Errorf("refusal error should be ErrCapabilityDenied, got %v", err)
			}
		})
	}
}

// TestCapabilityEnforcementApprovalKindMatrix covers RequiredForApproval:
// questions need interactive, everything gate-shaped needs approver.
func TestCapabilityEnforcementApprovalKindMatrix(t *testing.T) {
	tests := []struct {
		name string
		caps []contracts.Capability
		kind contracts.ApprovalKind
		want bool
	}{
		{"interactive answers question", []contracts.Capability{contracts.CapInteractive}, contracts.KindQuestion, true},
		{"approver cannot answer question (wrong tier)", []contracts.Capability{contracts.CapApprover}, contracts.KindQuestion, false},
		{"approver resolves exec", []contracts.Capability{contracts.CapApprover}, contracts.KindExec, true},
		{"interactive cannot resolve exec", []contracts.Capability{contracts.CapInteractive}, contracts.KindExec, false},
		{"approver resolves plan", []contracts.Capability{contracts.CapApprover}, contracts.KindPlan, true},
		{"approver resolves gate", []contracts.Capability{contracts.CapApprover}, contracts.KindGate, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Device{ID: "dev1", Capabilities: tc.caps}
			err := CheckApproval(d, tc.kind)
			got := err == nil
			if got != tc.want {
				t.Fatalf("CheckApproval(%v, %q): got ok=%v (err=%v) want ok=%v", tc.caps, tc.kind, got, err, tc.want)
			}
		})
	}
}

// TestCapabilityConstraintNarrowsNeverWidens: a device with CapApprover but
// an explicit AllowedApprovalKinds constraint omitting a kind is refused
// for that kind even though the tier alone would permit it.
func TestCapabilityConstraintNarrowsNeverWidens(t *testing.T) {
	d := Device{
		ID:           "dev1",
		Capabilities: []contracts.Capability{contracts.CapApprover},
		Constraints:  DeviceConstraints{AllowedApprovalKinds: []contracts.ApprovalKind{contracts.KindExec}},
	}
	if err := CheckApproval(d, contracts.KindExec); err != nil {
		t.Errorf("exec (in allow-list): got %v want nil", err)
	}
	if err := CheckApproval(d, contracts.KindPatch); !errors.Is(err, ErrCapabilityDenied) {
		t.Errorf("patch (NOT in allow-list): got %v want ErrCapabilityDenied", err)
	}
}

// TestAttachInfoIgnoresClientDeclaredCapabilities proves the fix for
// io/session.go's DEFERRED note: capabilities on the wire seam always come
// from the authenticated device's registry grant, and a session built from
// that AttachInfo refuses an over-reach exactly as CheckInput predicts —
// even though nothing in io.AttachInfo itself stops a caller from setting
// Capabilities to anything.
func TestAttachInfoIgnoresClientDeclaredCapabilities(t *testing.T) {
	device := Device{ID: "dev1", Capabilities: []contracts.Capability{contracts.CapObserver}}
	info := AttachInfo(device, "tui")
	if len(info.Capabilities) != 1 || info.Capabilities[0] != contracts.CapObserver {
		t.Fatalf("AttachInfo capabilities: got %v want [observer]", info.Capabilities)
	}
	if info.ClientID != device.ID {
		t.Fatalf("AttachInfo.ClientID: got %q want %q (device fingerprint, unforgeable)", info.ClientID, device.ID)
	}

	sess := agoraio.NewSession(context.Background(), "th1", &stubEngine{})
	defer sess.Close()
	att := sess.Attach(info, 0)
	defer att.Detach()

	err := att.Send(context.Background(), contracts.Input{Type: contracts.InUserMessage, Text: "hi"})
	if !errors.Is(err, agoraio.ErrUnauthorized) {
		t.Fatalf("observer-only attach sending user_message: got %v want io.ErrUnauthorized", err)
	}
}

// stubEngine is a minimal contracts-shaped Engine for capability_test.go —
// it never needs to do anything, tests only exercise the capability gate
// before any Input would reach it.
type stubEngine struct{}

func (stubEngine) Run(ctx context.Context, in <-chan contracts.Input, out chan<- contracts.Event) error {
	<-ctx.Done()
	close(out)
	return nil
}
