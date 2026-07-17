package memory

import "errors"

// Sentinel errors returned by Store. Spec: agora-spec-memory.md §1, §3.
var (
	// ErrNotFound is returned by Read/Delete when the named memory does not
	// exist.
	ErrNotFound = errors.New("memory: not found")
	// ErrInvalidName is returned when a name is not a safe single-path-
	// element slug (empty, contains a path separator, ".", "..", or is
	// over-long) — the write-outside-the-memory-dir guard the tool family
	// exists to enforce (§3: "Write outside the memory dir via these tools
	// = impossible by construction").
	ErrInvalidName = errors.New("memory: invalid name")
	// ErrReservedName is returned when a name collides with the index
	// file's own basename ("MEMORY"), which is not an individual memory.
	ErrReservedName = errors.New("memory: name is reserved for the index")
	// ErrInvalidType is returned when frontmatter.Type is not one of the
	// four allowed values (§1: "type: user|feedback|project|reference").
	ErrInvalidType = errors.New("memory: invalid frontmatter type")
	// ErrTooLarge is returned when a memory file exceeds MaxMemoryFileBytes
	// (on Write, or when reading a symlinked/oversized file). Review (U13).
	ErrTooLarge = errors.New("memory: file too large")
	// ErrEmptyName is returned when frontmatter.Name (the display title
	// used in the index) is empty.
	ErrEmptyName = errors.New("memory: frontmatter name is required")
)
