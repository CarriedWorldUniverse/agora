package hooks

import "testing"

func TestContentHash_ConvergesTOMLAndJSON(t *testing.T) {
	// §4.4: "Content hash over normalized identity (event + matcher +
	// normalized command) — equal hooks from TOML and JSON converge."
	// Simulate the only difference TOML vs JSON round-tripping typically
	// introduces: incidental whitespace in the command string.
	h1 := ContentHash(EventPreToolUse, "Bash", "echo hello", "")
	h2 := ContentHash(EventPreToolUse, "Bash", "echo   hello\n", "")
	if h1 != h2 {
		t.Errorf("content hash must converge across whitespace-only command differences: %q != %q", h1, h2)
	}
}

func TestContentHash_ChangesOnRealEdit(t *testing.T) {
	base := ContentHash(EventPreToolUse, "Bash", "echo hello", "")
	cases := map[string]string{
		"different event":   ContentHash(EventPostToolUse, "Bash", "echo hello", ""),
		"different matcher": ContentHash(EventPreToolUse, "Edit", "echo hello", ""),
		"different command": ContentHash(EventPreToolUse, "Bash", "echo goodbye", ""),
	}
	for name, h := range cases {
		if h == base {
			t.Errorf("%s: expected content hash to change, both were %q", name, h)
		}
	}
}

func TestContentHash_Prefix(t *testing.T) {
	h := ContentHash(EventPreToolUse, "Bash", "echo hi", "")
	if len(h) < 7 || h[:7] != "sha256:" {
		t.Errorf("ContentHash must be prefixed sha256:, got %q", h)
	}
}

// TestResolveTrust_Matrix is the trust/layer matrix required by the DoD:
// gate-runnability across trusted/untrusted/modified/managed/bypass/disabled
// combinations, transcribed from §4.4: "Run only if enabled &&
// (bypass_trust || managed || hash matches trusted_hash); mismatch ->
// status Modified; absent -> Untrusted."
func TestResolveTrust_Matrix(t *testing.T) {
	const computed = "sha256:aaaa"
	cases := []struct {
		name         string
		state        HandlerState
		stateKnown   bool
		managed      bool
		bypassTrust  bool
		wantRunnable bool
		wantEnabled  bool
		wantStatus   TrustState
	}{
		{
			name:         "no state record at all -> untrusted, still enabled by default, never runs",
			state:        HandlerState{},
			stateKnown:   false,
			wantRunnable: false,
			wantEnabled:  true,
			wantStatus:   TrustUntrusted,
		},
		{
			name:         "explicit state, empty trusted_hash -> untrusted, never runs",
			state:        HandlerState{Enabled: true, TrustedHash: ""},
			stateKnown:   true,
			wantRunnable: false,
			wantEnabled:  true,
			wantStatus:   TrustUntrusted,
		},
		{
			name:         "trusted_hash matches computed -> trusted, runs",
			state:        HandlerState{Enabled: true, TrustedHash: computed},
			stateKnown:   true,
			wantRunnable: true,
			wantEnabled:  true,
			wantStatus:   TrustTrusted,
		},
		{
			name:         "trusted_hash mismatches computed -> modified, fail closed, never runs",
			state:        HandlerState{Enabled: true, TrustedHash: "sha256:stale"},
			stateKnown:   true,
			wantRunnable: false,
			wantEnabled:  true,
			wantStatus:   TrustModified,
		},
		{
			name:         "disabled explicitly, even with a matching hash -> never runs",
			state:        HandlerState{Enabled: false, TrustedHash: computed},
			stateKnown:   true,
			wantRunnable: false,
			wantEnabled:  false,
			wantStatus:   TrustTrusted, // trust computation is independent of enabled; enabled gates Runnable
		},
		{
			name:         "managed hook, no trust record at all -> always trusted+runs (§4.3)",
			state:        HandlerState{},
			stateKnown:   false,
			managed:      true,
			wantRunnable: true,
			wantEnabled:  true,
			wantStatus:   TrustTrusted,
		},
		{
			name:         "managed hook overrides even a stale mismatched hash",
			state:        HandlerState{Enabled: true, TrustedHash: "sha256:stale"},
			stateKnown:   true,
			managed:      true,
			wantRunnable: true,
			wantEnabled:  true,
			wantStatus:   TrustTrusted,
		},
		{
			name:         "bypass_trust, no trust record -> runs (dev-mode override)",
			state:        HandlerState{},
			stateKnown:   false,
			bypassTrust:  true,
			wantRunnable: true,
			wantEnabled:  true,
			wantStatus:   TrustTrusted,
		},
		{
			name:         "managed but explicitly disabled -> still never runs (enabled gates first)",
			state:        HandlerState{Enabled: false},
			stateKnown:   true,
			managed:      true,
			wantRunnable: false,
			wantEnabled:  false,
			wantStatus:   TrustUntrusted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runnable, enabled, status := ResolveTrust(tc.state, tc.stateKnown, computed, tc.managed, tc.bypassTrust)
			if runnable != tc.wantRunnable {
				t.Errorf("runnable = %v, want %v", runnable, tc.wantRunnable)
			}
			if enabled != tc.wantEnabled {
				t.Errorf("enabled = %v, want %v", enabled, tc.wantEnabled)
			}
			if status != tc.wantStatus {
				t.Errorf("status = %v, want %v", status, tc.wantStatus)
			}
		})
	}
}

// TestResolveTrust_FailClosedNeverExecutesUntrustedOrModified is the
// security-relevant invariant, isolated as its own test: for every
// Untrusted/Modified status, runnable MUST be false, with no exceptions
// other than managed/bypass_trust (already covered by the matrix above
// resolving straight to Trusted before status is even computed against a
// mismatched hash).
func TestResolveTrust_FailClosedNeverExecutesUntrustedOrModified(t *testing.T) {
	states := []HandlerState{
		{Enabled: true, TrustedHash: ""},
		{Enabled: true, TrustedHash: "sha256:not-the-computed-one"},
	}
	for _, st := range states {
		runnable, _, status := ResolveTrust(st, true, "sha256:computed", false, false)
		if status == TrustTrusted {
			t.Fatalf("test setup bug: expected non-trusted status for %+v", st)
		}
		if runnable {
			t.Errorf("state %+v resolved to status %s but runnable=true — fail-closed violated", st, status)
		}
	}
}
