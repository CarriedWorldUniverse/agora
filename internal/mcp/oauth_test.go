package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCredentialKey_Deterministic(t *testing.T) {
	k1 := CredentialKey("herald", "https://herald.internal/mcp")
	k2 := CredentialKey("herald", "https://herald.internal/mcp")
	if k1 != k2 {
		t.Fatalf("key not stable: %q vs %q", k1, k2)
	}
	k3 := CredentialKey("herald", "https://herald.internal/other")
	if k1 == k3 {
		t.Fatalf("expected distinct key for distinct url payload")
	}
}

func TestCallbackID_Length12(t *testing.T) {
	id := CallbackID("https://example.com/mcp")
	if len(id) != 12 {
		t.Fatalf("CallbackID len = %d, want 12", len(id))
	}
}

func TestGeneratePKCE_VerifierAndChallengeDiffer(t *testing.T) {
	p, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if p.Verifier == "" || p.Challenge == "" {
		t.Fatalf("empty pkce: %+v", p)
	}
	if p.Verifier == p.Challenge {
		t.Fatalf("verifier and challenge must differ")
	}
	if len(p.Verifier) < 43 {
		t.Fatalf("verifier too short: %d", len(p.Verifier))
	}
}

func TestCredential_NeedsRefreshSkew(t *testing.T) {
	now := time.Unix(1000, 0)
	tests := []struct {
		name       string
		expiresAt  time.Time
		wantRefres bool
	}{
		{"far in future", now.Add(time.Hour), false},
		{"exactly at skew boundary", now.Add(RefreshSkew), true}, // now+30s >= expires_at
		{"already expired", now.Add(-time.Second), true},
		{"just outside skew", now.Add(RefreshSkew + time.Second), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Credential{ExpiresAtMs: tt.expiresAt.UnixMilli()}
			if got := c.NeedsRefresh(now); got != tt.wantRefres {
				t.Errorf("NeedsRefresh = %v, want %v", got, tt.wantRefres)
			}
		})
	}
}

func TestCredential_UsableRequiresRefreshTokenWhenExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	expired := Credential{ExpiresAtMs: now.Add(-time.Minute).UnixMilli()}
	if expired.Usable(now) {
		t.Fatalf("expired credential with no refresh token must not be usable")
	}
	expired.TokenResponse.RefreshToken = "rt"
	if !expired.Usable(now) {
		t.Fatalf("expired-but-refreshable credential must be usable")
	}
}

func TestCredential_ReconstructExpiresIn(t *testing.T) {
	now := time.Unix(1000, 0)
	fresh := Credential{ExpiresAtMs: now.Add(60 * time.Second).UnixMilli()}
	got := fresh.ReconstructExpiresIn(now)
	if got.TokenResponse.ExpiresIn != 60 {
		t.Fatalf("ExpiresIn = %d, want 60", got.TokenResponse.ExpiresIn)
	}

	expired := Credential{ExpiresAtMs: now.Add(-60 * time.Second).UnixMilli()}
	got2 := expired.ReconstructExpiresIn(now)
	if got2.TokenResponse.ExpiresIn != 0 {
		t.Fatalf("known-expired ExpiresIn = %d, want 0 (SDK refreshes before first request)", got2.TokenResponse.ExpiresIn)
	}
}

type stubDiscoverer struct {
	ok  bool
	err error
}

func (s stubDiscoverer) Discover(ctx context.Context, url string) (bool, error) { return s.ok, s.err }

func TestResolveAuthStatus_Order(t *testing.T) {
	now := time.Unix(1000, 0)

	t.Run("bearer_token_env_var wins first", func(t *testing.T) {
		cfg := ServerConfig{BearerTokenEnvVar: "TOKEN", URL: "https://x"}
		got := ResolveAuthStatus(context.Background(), cfg, nil, now, nil)
		if got != AuthStatusBearerToken {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("authorization header present", func(t *testing.T) {
		cfg := ServerConfig{HTTPHeaders: map[string]string{"Authorization": "Bearer abc"}, URL: "https://x"}
		got := ResolveAuthStatus(context.Background(), cfg, nil, now, nil)
		if got != AuthStatusBearerToken {
			t.Fatalf("got %v", got)
		}
	})

	// env_http_headers is a documented way to source Authorization (header
	// -> env-var-name); ResolveAuthStatus must recognize it the same as a
	// literal HTTPHeaders entry, or an env-sourced-auth server wrongly
	// resolves to Unsupported and triggers spurious discovery/login-hint UX.
	t.Run("authorization via env_http_headers", func(t *testing.T) {
		cfg := ServerConfig{EnvHTTPHeaders: map[string]string{"Authorization": "TOKEN_VAR"}, URL: "https://x"}
		got := ResolveAuthStatus(context.Background(), cfg, nil, now, nil)
		if got != AuthStatusBearerToken {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("stored usable oauth", func(t *testing.T) {
		cfg := ServerConfig{URL: "https://x"}
		cred := Credential{ExpiresAtMs: now.Add(time.Hour).UnixMilli()}
		got := ResolveAuthStatus(context.Background(), cfg, &cred, now, nil)
		if got != AuthStatusOAuth {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("stored but unrefreshable", func(t *testing.T) {
		cfg := ServerConfig{URL: "https://x"}
		cred := Credential{ExpiresAtMs: now.Add(-time.Hour).UnixMilli()}
		got := ResolveAuthStatus(context.Background(), cfg, &cred, now, nil)
		if got != AuthStatusLoggedOutReauth {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("none stored, discoverable", func(t *testing.T) {
		cfg := ServerConfig{URL: "https://x"}
		got := ResolveAuthStatus(context.Background(), cfg, nil, now, stubDiscoverer{ok: true})
		if got != AuthStatusLoggedOutLogin {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("none stored, not discoverable", func(t *testing.T) {
		cfg := ServerConfig{URL: "https://x"}
		got := ResolveAuthStatus(context.Background(), cfg, nil, now, stubDiscoverer{ok: false})
		if got != AuthStatusUnsupported {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("none stored, discovery error", func(t *testing.T) {
		cfg := ServerConfig{URL: "https://x"}
		got := ResolveAuthStatus(context.Background(), cfg, nil, now, stubDiscoverer{err: errors.New("timeout")})
		if got != AuthStatusUnsupported {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("none stored, nil discoverer", func(t *testing.T) {
		cfg := ServerConfig{URL: "https://x"}
		got := ResolveAuthStatus(context.Background(), cfg, nil, now, nil)
		if got != AuthStatusUnsupported {
			t.Fatalf("got %v", got)
		}
	})
}

func TestLoginFlow_StateMachine(t *testing.T) {
	start := time.Unix(1000, 0)
	pkce, _ := GeneratePKCE()

	t.Run("completes within timeout", func(t *testing.T) {
		f := NewLoginFlow("herald", "https://herald.internal/mcp", "", pkce, start)
		if f.State != LoginAwaitingCallback {
			t.Fatalf("initial state = %s", f.State)
		}
		if err := f.Complete(start.Add(10 * time.Second)); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if f.State != LoginCompleted {
			t.Fatalf("state after complete = %s", f.State)
		}
	})

	t.Run("times out at 300s budget", func(t *testing.T) {
		f := NewLoginFlow("herald", "https://herald.internal/mcp", "", pkce, start)
		err := f.Complete(start.Add(LoginTimeout))
		if !errors.Is(err, ErrOAuthLoginTimeout) {
			t.Fatalf("err = %v, want ErrOAuthLoginTimeout", err)
		}
		if f.State != LoginTimedOut {
			t.Fatalf("state = %s, want TimedOut", f.State)
		}
	})

	t.Run("CheckTimeout without completing", func(t *testing.T) {
		f := NewLoginFlow("herald", "https://herald.internal/mcp", "", pkce, start)
		if f.CheckTimeout(start.Add(100 * time.Second)) {
			t.Fatalf("expected not timed out yet")
		}
		if !f.CheckTimeout(start.Add(LoginTimeout + time.Second)) {
			t.Fatalf("expected timed out")
		}
		if f.State != LoginTimedOut {
			t.Fatalf("state = %s", f.State)
		}
	})

	t.Run("Fail transitions to Failed", func(t *testing.T) {
		f := NewLoginFlow("herald", "https://herald.internal/mcp", "", pkce, start)
		f.Fail()
		if f.State != LoginFailed {
			t.Fatalf("state = %s", f.State)
		}
	})

	t.Run("Complete after already settled errors", func(t *testing.T) {
		f := NewLoginFlow("herald", "https://herald.internal/mcp", "", pkce, start)
		f.Fail()
		if err := f.Complete(start); err == nil {
			t.Fatalf("expected error completing an already-failed flow")
		}
	})
}

func TestFileStore_SaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, ".credentials.json"))

	key := CredentialKey("herald", "https://herald.internal/mcp")
	cred := Credential{ServerName: "herald", URL: "https://herald.internal/mcp", TokenResponse: TokenResponse{AccessToken: "at"}}

	if _, ok, err := s.Load(key); err != nil || ok {
		t.Fatalf("expected miss before save: ok=%v err=%v", ok, err)
	}
	if err := s.Save(key, cred); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := s.Load(key)
	if err != nil || !ok {
		t.Fatalf("Load after save: ok=%v err=%v", ok, err)
	}
	if got.TokenResponse.AccessToken != "at" {
		t.Fatalf("loaded cred wrong: %+v", got)
	}
	if err := s.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Load(key); ok {
		t.Fatalf("expected miss after delete")
	}
	// Delete of a missing key is not an error (idempotent).
	if err := s.Delete(key); err != nil {
		t.Fatalf("Delete of missing key: %v", err)
	}
}

func TestFileStore_ConcurrentSavesDoNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	s := NewFileStore(filepath.Join(dir, ".credentials.json"))
	s.LockTimeout = 2 * time.Second

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := CredentialKey("server", string(rune('a'+i)))
			err := s.Save(key, Credential{ServerName: "server", TokenResponse: TokenResponse{AccessToken: string(rune('a' + i))}})
			if err != nil {
				t.Errorf("Save: %v", err)
			}
		}()
	}
	wg.Wait()

	for i := 0; i < 8; i++ {
		key := CredentialKey("server", string(rune('a'+i)))
		got, ok, err := s.Load(key)
		if err != nil || !ok {
			t.Fatalf("Load %d: ok=%v err=%v", i, ok, err)
		}
		if got.TokenResponse.AccessToken != string(rune('a'+i)) {
			t.Fatalf("Load %d wrong: %+v", i, got)
		}
	}
}
