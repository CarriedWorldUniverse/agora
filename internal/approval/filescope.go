package approval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// filescope.go is the durable ScopeStore the ScopeStore interface's own doc
// comment anticipated ("a clear, swappable seam ... so a future unit can
// back it with something durable without touching the decision pipeline").
// Without it every allow-always the operator granted died with the process,
// so each new session re-prompted for the same handful of safe commands.
//
// WHERE THE FILE LIVES IS A SECURITY DECISION, not a layout preference.
// Grants live in the USER's directory (~/.agora/permissions.json), bucketed
// by project root — NOT in the project's own .agora/. A project-layer
// permissions file would let a repository ship its own pre-granted command
// prefixes, so cloning a hostile repo and starting agora in it would
// auto-approve commands the operator never saw. Keeping the file outside
// every repo removes that path entirely: a repo cannot grant itself
// anything.
//
// Bucketing by project root is the other half. A prefix grant means "this
// command is fine", and whether a command is fine depends on the tree it
// runs in — `make deploy` in a scratch repo is not `make deploy` in the
// cluster config. Grants therefore never apply outside the project they
// were made in. The "*" bucket is the deliberate exception: it applies
// everywhere and is only ever created by an operator hand-editing the file,
// never by this code.

// PermissionsFileVersion is the on-disk schema version. Bumped only for a
// breaking shape change; an unknown version is refused rather than guessed
// at (see load).
const PermissionsFileVersion = 1

// GlobalProjectBucket is the project key whose grants apply in every
// project. Never written by Grant — operator hand-edit only.
const GlobalProjectBucket = "*"

// persistedGrant is one grant's on-disk shape. Separate from ScopeAllow so
// the in-memory type stays free of json tags and the wire format can evolve
// independently (same split internal/turnengine uses for hook state).
type persistedGrant struct {
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	Key       string `json:"key"`
	By        string `json:"by"`
	GrantedAt string `json:"grantedAt,omitempty"`
}

// permissionsFile is the whole document.
type permissionsFile struct {
	Version int `json:"version"`
	// Projects maps a project-root path (or GlobalProjectBucket) to the
	// grants made in it.
	Projects map[string][]persistedGrant `json:"projects"`
}

// FileScopeStore is a ScopeStore backed by a JSON file. It delegates ALL
// validation and matching to an embedded MemScopeStore rather than
// reimplementing them — the rules about which scopes are persistable, which
// kinds accept prefix vs host, and how matching works are subtle and must
// not drift between two implementations.
type FileScopeStore struct {
	mem         *MemScopeStore
	path        string
	projectRoot string

	mu sync.Mutex // serializes read-modify-write of the file
}

// OpenFileScopeStore loads the operator's grants for projectRoot from path,
// returning a store that persists later grants back to it.
//
// It never fails the session: a missing file is the normal first-run case,
// and a corrupt or unreadable one degrades to "no prior grants" with a
// warning rather than blocking startup. The cost of a lost grant is one
// extra prompt; the cost of refusing to start is the whole session. The
// returned warning is non-nil in that degraded case so the caller can
// surface it.
func OpenFileScopeStore(path, projectRoot string) (*FileScopeStore, error) {
	s := &FileScopeStore{
		mem:         NewMemScopeStore(),
		path:        path,
		projectRoot: projectRoot,
	}
	doc, warn := load(path)
	if doc != nil {
		// Replay this project's grants plus the operator's global bucket.
		for _, key := range []string{GlobalProjectBucket, projectRoot} {
			for _, g := range doc.Projects[key] {
				// Grant (not direct map writes) so a hand-edited file with an
				// invalid combination — host scope on an exec kind, say — is
				// rejected by the same rules that govern a live grant.
				_ = s.mem.Grant(ScopeAllow{
					Kind:  contracts.ApprovalKind(g.Kind),
					Scope: contracts.Scope(g.Scope),
					Key:   g.Key,
					By:    g.By,
				})
			}
		}
	}
	return s, warn
}

// load reads and validates the document. Returns (nil, warning) for any
// problem short of "file absent", which is (nil, nil).
func load(path string) (*permissionsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("approval: reading %s: %w (continuing with no saved permissions)", path, err)
	}
	var doc permissionsFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("approval: %s is not valid JSON: %w (continuing with no saved permissions)", path, err)
	}
	if doc.Version != PermissionsFileVersion {
		// Refuse rather than guess: a future version may mean something
		// different by the same field names, and silently misreading a
		// PERMISSIONS file is exactly the wrong failure mode.
		return nil, fmt.Errorf("approval: %s has version %d, expected %d (continuing with no saved permissions)",
			path, doc.Version, PermissionsFileVersion)
	}
	return &doc, nil
}

// Grant validates and records the allow, then persists it. A grant that
// fails validation is never written. A write failure does NOT fail the
// grant: the allow is already live in memory and honouring it for this
// session is better than rejecting an approval the operator just gave —
// the error is returned so the caller can warn.
func (s *FileScopeStore) Grant(a ScopeAllow) error {
	if err := s.mem.Grant(a); err != nil {
		return err
	}
	return s.persist(a)
}

func (s *FileScopeStore) Match(kind contracts.ApprovalKind, sessionID, scopeKey string) (ScopeAllow, bool) {
	return s.mem.Match(kind, sessionID, scopeKey)
}

// persist adds a to the project's bucket and rewrites the file atomically.
// Re-reads under the lock so a concurrently-running agora in another
// terminal does not lose its grants to a stale in-memory snapshot.
func (s *FileScopeStore) persist(a ScopeAllow) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, _ := load(s.path)
	if doc == nil {
		doc = &permissionsFile{Version: PermissionsFileVersion}
	}
	if doc.Projects == nil {
		doc.Projects = make(map[string][]persistedGrant)
	}

	g := persistedGrant{
		Kind:      string(a.Kind),
		Scope:     string(a.Scope),
		Key:       a.Key,
		By:        a.By,
		GrantedAt: time.Now().UTC().Format(time.RFC3339),
	}
	bucket := doc.Projects[s.projectRoot]
	for _, existing := range bucket {
		if existing.Kind == g.Kind && existing.Scope == g.Scope && existing.Key == g.Key {
			return nil // already granted; nothing to write
		}
	}
	doc.Projects[s.projectRoot] = append(bucket, g)

	// Stable ordering so the file is diffable and an operator reviewing it
	// sees a consistent shape rather than map iteration order.
	sortGrants(doc.Projects[s.projectRoot])
	return writeAtomic(s.path, doc)
}

func sortGrants(gs []persistedGrant) {
	sort.SliceStable(gs, func(i, j int) bool {
		if gs[i].Kind != gs[j].Kind {
			return gs[i].Kind < gs[j].Kind
		}
		if gs[i].Scope != gs[j].Scope {
			return gs[i].Scope < gs[j].Scope
		}
		return gs[i].Key < gs[j].Key
	})
}

// writeAtomic writes via a temp file in the same directory then renames, so
// a crash mid-write cannot leave a half-written permissions file that would
// then fail to parse and silently drop every saved grant.
func writeAtomic(path string, doc *permissionsFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("approval: creating %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("approval: encoding permissions: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".permissions-*.tmp")
	if err != nil {
		return fmt.Errorf("approval: creating temp permissions file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	// 0600: this file records what the operator has authorised. Another
	// local user being able to APPEND to it is a privilege-escalation path.
	//
	// Unix only, in effect: on Windows os.Chmod merely toggles the
	// read-only attribute, and access is governed by ACLs inherited from
	// the containing directory instead. The file still lands under the
	// user's home there, so it inherits that profile's protection — but
	// this specific defence is not what is enforcing it.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("approval: securing temp permissions file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("approval: writing permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("approval: closing permissions: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("approval: replacing %s: %w", path, err)
	}
	return nil
}

// Grants returns the saved grants that apply to this store's project, for
// display (a /permissions command) — sorted, and including the global
// bucket, flagged so the operator can tell which are which.
func (s *FileScopeStore) Grants() []DisplayGrant {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, _ := load(s.path)
	if doc == nil {
		return nil
	}
	var out []DisplayGrant
	for _, bucket := range []string{GlobalProjectBucket, s.projectRoot} {
		gs := append([]persistedGrant(nil), doc.Projects[bucket]...)
		sortGrants(gs)
		for _, g := range gs {
			out = append(out, DisplayGrant{
				Kind: g.Kind, Scope: g.Scope, Key: g.Key, By: g.By,
				GrantedAt: g.GrantedAt, Global: bucket == GlobalProjectBucket,
			})
		}
	}
	return out
}

// DisplayGrant is one saved grant, flattened for presentation.
type DisplayGrant struct {
	Kind      string
	Scope     string
	Key       string
	By        string
	GrantedAt string
	// Global marks a grant from the "*" bucket — it applies in every
	// project, not just this one.
	Global bool
}

// Revoke removes a saved grant from this project's bucket and rewrites the
// file. It does NOT remove it from the in-memory store: the running session
// already resolved calls against it, and silently tightening mid-session
// would be a surprising, hard-to-explain change of behaviour. The revoke
// takes effect next session. Reports whether anything was removed.
func (s *FileScopeStore) Revoke(kind, scope, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, warn := load(s.path)
	if doc == nil {
		return false, warn
	}
	bucket := doc.Projects[s.projectRoot]
	kept := bucket[:0]
	removed := false
	for _, g := range bucket {
		if g.Kind == kind && g.Scope == scope && g.Key == key {
			removed = true
			continue
		}
		kept = append(kept, g)
	}
	if !removed {
		return false, nil
	}
	doc.Projects[s.projectRoot] = kept
	if len(kept) == 0 {
		delete(doc.Projects, s.projectRoot)
	}
	return true, writeAtomic(s.path, doc)
}
