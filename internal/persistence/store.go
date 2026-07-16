package persistence

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// Sentinel errors returned by both LocalStore and MemStore, so callers can
// errors.Is regardless of implementation.
// Spec: agora-spec-persistence.md §3.
var (
	ErrNotFound       = errors.New("persistence: thread not found")
	ErrExists         = errors.New("persistence: thread already exists")
	ErrInvalidForkSeq = errors.New("persistence: invalid fork seq")
)

// FsyncMode controls when Append fsyncs the JSONL file.
// Spec: agora-spec-persistence.md §1 ("append + fsync on turn boundaries
// (config: per-item for paranoid mode)").
type FsyncMode int

const (
	// FsyncTurn fsyncs once after the whole batch passed to a single Append
	// call (the normal case: one Append call = one turn boundary). Default.
	FsyncTurn FsyncMode = iota
	// FsyncItem fsyncs after every individual item — paranoid mode.
	FsyncItem
)

// Config configures a LocalStore.
// Spec: agora-spec-persistence.md §1.
type Config struct {
	// Fsync selects the durability knob. Zero value = FsyncTurn.
	Fsync FsyncMode
}

// wdChangedPayload is the payload shape persistence.go decodes when it sees
// a TIWDChanged item, so it can keep the mirror's working_dir/project_root
// current for /resume filtering — "the latest wins at resume" (spec §1
// line 9). The wire shape isn't part of the frozen contracts package (no
// ThreadItemType payload types are specified there), so this is an
// internal-only decode target: any payload matching this shape (JSON
// object with a working_dir string field) is honored; anything else is a
// no-op rather than an error, since payload is `any`.
type wdChangedPayload struct {
	WorkingDir  string `json:"working_dir"`
	ProjectRoot string `json:"project_root,omitempty"`
}

// monthDir returns the <root>/threads/<yyyy-mm> directory a thread created
// at t shards into. Spec §1: "month sharding keeps dirs sane."
func monthDir(root string, t time.Time) string {
	return filepath.Join(root, "threads", t.UTC().Format("2006-01"))
}

// threadPath returns the JSONL file path for a thread created at t.
func threadPath(root string, t time.Time, threadID string) string {
	return filepath.Join(monthDir(root, t), threadID+".jsonl")
}

// validateThreadID rejects any id that could escape the store root when
// joined into a filesystem path, or that isn't a clean single path element.
// This is the storage-layer trust boundary: it must defend its own
// invariant regardless of who calls Create/Fork (a future remote/TUI unit
// may pass externally-influenced ids). Allowed: non-empty [A-Za-z0-9._-],
// excluding "." and "..".
func validateThreadID(id string) error {
	if id == "" {
		return fmt.Errorf("persistence: empty thread id")
	}
	// Cap well below the common 255-byte filename limit, leaving room for the
	// ".jsonl" suffix threadPath appends — so an over-long id fails with this
	// clean validation error, not a later OS error.
	if len(id) > 240 {
		return fmt.Errorf("persistence: thread id too long")
	}
	if id == "." || id == ".." {
		return fmt.Errorf("persistence: invalid thread id %q", id)
	}
	// filepath.Base collapses any separator/traversal to a single element;
	// if the id isn't already that element it contains a separator or path
	// trickery (rejects "a/b", "/abs", "a\\b", "../x", ...).
	if filepath.Base(id) != id {
		return fmt.Errorf("persistence: thread id %q contains a path separator", id)
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("persistence: thread id %q contains an illegal character", id)
		}
	}
	return nil
}

// newThreadID generates a fresh thread id, used by Fork (Create takes an
// id supplied by the caller in meta.ThreadID; Fork must mint one since the
// contract's Fork signature has no id parameter).
func newThreadID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("persistence: generate thread id: %w", err)
	}
	return "th_" + hex.EncodeToString(b[:]), nil
}

// forkRefEqual reports whether two *contracts.ForkRef are equal, treating
// nil as distinct from a zero-value ref (used by tests/rebuild comparison).
func forkRefEqual(a, b *contracts.ForkRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
