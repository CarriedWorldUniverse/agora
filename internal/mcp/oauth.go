package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RefreshSkew is the §3 refresh window: "now + 30s >= expires_at ⇒ refresh".
const RefreshSkew = 30 * time.Second

// LoginTimeout is the §3 overall loopback-callback budget.
const LoginTimeout = 300 * time.Second

// TokenResponse is the OAuth token payload as stored (§3 "Stored shape").
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// ExpiresIn is seconds-from-issue, the wire shape; on load it is
	// reconstructed from ExpiresAtMs rather than trusted verbatim (§3).
	ExpiresIn int64 `json:"expires_in,omitempty"`
}

// Credential is one stored server credential (§3 "Stored shape":
// {server_name, url, client_id, token_response, expires_at_ms}).
type Credential struct {
	ServerName    string        `json:"server_name"`
	URL           string        `json:"url"`
	ClientID      string        `json:"client_id"`
	TokenResponse TokenResponse `json:"token_response"`
	ExpiresAtMs   int64         `json:"expires_at_ms"`
}

// ExpiresAt is Credential.ExpiresAtMs as a time.Time.
func (c Credential) ExpiresAt() time.Time { return time.UnixMilli(c.ExpiresAtMs) }

// NeedsRefresh reports whether c should be refreshed at now (§3 skew rule).
func (c Credential) NeedsRefresh(now time.Time) bool {
	return !now.Add(RefreshSkew).Before(c.ExpiresAt())
}

// Refreshable reports whether c can be refreshed without user interaction —
// a refresh token must be present (§3: "near-expired tokens usable only if
// a refresh token exists").
func (c Credential) Refreshable() bool {
	return c.TokenResponse.RefreshToken != ""
}

// Usable reports whether c can be used as-is (not expired past skew, or
// expired-but-refreshable — the SDK is expected to refresh transparently
// before first request per the ExpiresIn-reconstruction rule below).
func (c Credential) Usable(now time.Time) bool {
	if !c.NeedsRefresh(now) {
		return true
	}
	return c.Refreshable()
}

// ReconstructExpiresIn fills TokenResponse.ExpiresIn from ExpiresAtMs on
// load: "known-expired ⇒ zero so the SDK refreshes before first request"
// (§3). Returns a copy; does not mutate c.
func (c Credential) ReconstructExpiresIn(now time.Time) Credential {
	out := c
	d := c.ExpiresAt().Sub(now)
	if d < 0 {
		out.TokenResponse.ExpiresIn = 0
	} else {
		out.TokenResponse.ExpiresIn = int64(d.Seconds())
	}
	return out
}

// CredentialKey derives the §3 store key: "<server>|<sha256(url-payload)[..16]>".
func CredentialKey(server, urlPayload string) string {
	sum := sha256.Sum256([]byte(urlPayload))
	return fmt.Sprintf("%s|%s", server, hex.EncodeToString(sum[:])[:16])
}

// CallbackID derives the §3 per-server loopback callback id: "12 chars of
// base64url(sha256(server_url))".
func CallbackID(serverURL string) string {
	sum := sha256.Sum256([]byte(serverURL))
	id := base64.RawURLEncoding.EncodeToString(sum[:])
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}

// PKCE is a generated PKCE verifier/challenge pair (S256).
type PKCE struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE creates an RFC 7636 S256 PKCE pair. verifier is 43-128
// url-safe chars (we generate 32 random bytes -> 43 base64url chars, the
// RFC floor).
func GeneratePKCE() (PKCE, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return PKCE{}, fmt.Errorf("mcp: pkce: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return PKCE{Verifier: verifier, Challenge: challenge}, nil
}

// AuthStatus is the §3 resolved auth status for a server.
type AuthStatus string

const (
	// AuthStatusBearerToken: a bearer_token_env_var or Authorization header
	// is configured — no OAuth flow needed.
	AuthStatusBearerToken AuthStatus = "bearer_token"
	// AuthStatusOAuth: a stored, usable OAuth credential exists.
	AuthStatusOAuth AuthStatus = "oauth"
	// AuthStatusLoggedOutReauth: stored credential exists but is
	// unrefreshable (expired, no refresh token).
	AuthStatusLoggedOutReauth AuthStatus = "logged_out_reauth"
	// AuthStatusLoggedOutLogin: no stored credential, but OAuth metadata is
	// discoverable — `agora mcp login <server>` will work.
	AuthStatusLoggedOutLogin AuthStatus = "logged_out_login"
	// AuthStatusUnsupported: no credential, no discoverable OAuth metadata.
	AuthStatusUnsupported AuthStatus = "unsupported"
)

// MetadataDiscoverer probes RFC 9728 protected-resource metadata
// (WWW-Authenticate-driven discovery). Production wiring makes an HTTP
// call with a 5s timeout (§3); tests supply a stub — no network in this
// package's test suite.
type MetadataDiscoverer interface {
	// Discover reports whether serverURL advertises OAuth metadata.
	Discover(ctx context.Context, serverURL string) (bool, error)
}

// DiscoverTimeout is the §3 discovery call budget.
const DiscoverTimeout = 5 * time.Second

// ResolveAuthStatus implements the §3 auth-status resolution order:
// bearer_token_env_var set -> BearerToken; Authorization header present ->
// BearerToken; stored OAuth usable -> OAuth; stored but unrefreshable ->
// LoggedOutReauth; none -> discover metadata -> LoggedOutLogin or
// Unsupported. stored is nil when no credential is on file.
func ResolveAuthStatus(ctx context.Context, cfg ServerConfig, stored *Credential, now time.Time, discoverer MetadataDiscoverer) AuthStatus {
	if strings.TrimSpace(cfg.BearerTokenEnvVar) != "" {
		return AuthStatusBearerToken
	}
	for k := range cfg.HTTPHeaders {
		if strings.EqualFold(k, "authorization") {
			return AuthStatusBearerToken
		}
	}

	if stored != nil {
		if stored.Usable(now) {
			return AuthStatusOAuth
		}
		return AuthStatusLoggedOutReauth
	}

	if discoverer == nil {
		return AuthStatusUnsupported
	}
	dctx, cancel := context.WithTimeout(ctx, DiscoverTimeout)
	defer cancel()
	ok, err := discoverer.Discover(dctx, cfg.URL)
	if err != nil || !ok {
		return AuthStatusUnsupported
	}
	return AuthStatusLoggedOutLogin
}

// LoginState is where a loopback login flow sits.
type LoginState string

const (
	LoginAwaitingCallback LoginState = "awaiting_callback"
	LoginCompleted        LoginState = "completed"
	LoginTimedOut         LoginState = "timed_out"
	LoginFailed           LoginState = "failed"
)

// LoginFlow is the §3 login-flow state machine: local loopback callback +
// PKCE, 300s overall timeout, `resource` query param when configured.
// "Interactive" (open browser) vs "return-URL" (hand the auth URL to a
// frontend) are both just: construct the AuthURL, then Complete/Fail/
// CheckTimeout drive the same state machine — the variant is only in how
// the caller SURFACES AuthURL, not in this type.
type LoginFlow struct {
	ServerName string
	CallbackID string
	PKCE       PKCE
	Resource   string // oauth_resource, when configured (§1/§3)
	StartedAt  time.Time
	State      LoginState
}

// NewLoginFlow starts a flow (state AwaitingCallback) for serverURL at now.
func NewLoginFlow(serverName, serverURL, resource string, pkce PKCE, now time.Time) *LoginFlow {
	return &LoginFlow{
		ServerName: serverName,
		CallbackID: CallbackID(serverURL),
		PKCE:       pkce,
		Resource:   resource,
		StartedAt:  now,
		State:      LoginAwaitingCallback,
	}
}

// CheckTimeout transitions to TimedOut if now is past the 300s budget while
// still awaiting the callback, and reports whether it did.
func (f *LoginFlow) CheckTimeout(now time.Time) bool {
	if f.State != LoginAwaitingCallback {
		return false
	}
	if now.Sub(f.StartedAt) >= LoginTimeout {
		f.State = LoginTimedOut
		return true
	}
	return false
}

// Complete transitions AwaitingCallback -> Completed, unless the timeout has
// already elapsed (returns ErrOAuthLoginTimeout) or the flow was not
// awaiting a callback (returns an error).
func (f *LoginFlow) Complete(now time.Time) error {
	if f.State != LoginAwaitingCallback {
		return fmt.Errorf("mcp: login flow for %q not awaiting callback (state=%s)", f.ServerName, f.State)
	}
	if f.CheckTimeout(now) {
		return ErrOAuthLoginTimeout
	}
	f.State = LoginCompleted
	return nil
}

// Fail transitions AwaitingCallback -> Failed (IdP error, denied, etc).
func (f *LoginFlow) Fail() {
	if f.State == LoginAwaitingCallback {
		f.State = LoginFailed
	}
}

// Store persists OAuth credentials keyed by CredentialKey. Spec: §3
// ("keyring or fallback JSON file ~/.agora/.credentials.json chmod 0600");
// this package ships the file-store fallback — keyring integration is
// platform-specific (Secret Service / Keychain / Credential Manager) and
// belongs to whichever unit wires OS keyring bindings; GlobalConfig's
// CredentialsStore == "keyring" selects it there, "file"/"auto"-fallback
// use this Store.
type Store interface {
	Load(key string) (Credential, bool, error)
	Save(key string, cred Credential) error
	Delete(key string) error
}

// FileStore is the §3 fallback JSON-file credential store: one file holding
// all credentials keyed by CredentialKey, chmod 0600, every read-modify-
// write wrapped in a file lock so concurrent agora processes don't race
// (§3). Locking uses an atomic-mkdir lockdir — portable across
// Linux/macOS/Windows without a third-party flock dependency (ground rule
// 2: minimize deps).
type FileStore struct {
	Path string
	// LockTimeout bounds how long Save/Load/Delete wait for the lock before
	// giving up (default 5s if zero).
	LockTimeout time.Duration
}

// NewFileStore builds a FileStore at path (typically ~/.agora/.credentials.json).
func NewFileStore(path string) *FileStore {
	return &FileStore{Path: path}
}

type credentialFile struct {
	Credentials map[string]Credential `json:"credentials"`
}

func (s *FileStore) lockTimeout() time.Duration {
	if s.LockTimeout > 0 {
		return s.LockTimeout
	}
	return 5 * time.Second
}

func (s *FileStore) withLock(fn func() error) error {
	lockPath := s.Path + ".lock"
	deadline := time.Now().Add(s.lockTimeout())
	for {
		err := os.Mkdir(lockPath, 0700)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return fmt.Errorf("mcp: oauth store lock: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("mcp: oauth store lock: timed out waiting for %s", lockPath)
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer os.Remove(lockPath)
	return fn()
}

func (s *FileStore) read() (credentialFile, error) {
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return credentialFile{Credentials: map[string]Credential{}}, nil
	}
	if err != nil {
		return credentialFile{}, err
	}
	var f credentialFile
	if len(data) == 0 {
		return credentialFile{Credentials: map[string]Credential{}}, nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return credentialFile{}, err
	}
	if f.Credentials == nil {
		f.Credentials = map[string]Credential{}
	}
	return f, nil
}

func (s *FileStore) write(f credentialFile) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return err
	}
	// Deterministic serialization (house style): marshal keys sorted rather
	// than relying on Go's map-iteration-order-independent-but-unspecified
	// json.Marshal(map) — json.Marshal already sorts map[string]V keys, but
	// we assert that explicitly here rather than depending on stdlib
	// behavior being read as "the spec".
	keys := make([]string, 0, len(f.Credentials))
	for k := range f.Credentials {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]Credential, len(keys))
	for _, k := range keys {
		ordered[k] = f.Credentials[k]
	}
	data, err := json.MarshalIndent(credentialFile{Credentials: ordered}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Load returns the credential for key, or ok=false if none stored.
func (s *FileStore) Load(key string) (Credential, bool, error) {
	var out Credential
	var found bool
	err := s.withLock(func() error {
		f, err := s.read()
		if err != nil {
			return err
		}
		out, found = f.Credentials[key]
		return nil
	})
	return out, found, err
}

// Save stores cred under key, changed-only in spirit (§3 "persist refreshed
// credentials only when changed") — callers should skip calling Save when
// the credential is byte-identical to what Load returned; Save itself
// always writes what it's given (idempotent, not a diff engine).
func (s *FileStore) Save(key string, cred Credential) error {
	return s.withLock(func() error {
		f, err := s.read()
		if err != nil {
			return err
		}
		f.Credentials[key] = cred
		return s.write(f)
	})
}

// Delete removes key's credential ("delete when SDK reports none", §3). A
// missing key is not an error (delete is idempotent).
func (s *FileStore) Delete(key string) error {
	return s.withLock(func() error {
		f, err := s.read()
		if err != nil {
			return err
		}
		delete(f.Credentials, key)
		return s.write(f)
	})
}
