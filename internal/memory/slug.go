package memory

import (
	"regexp"
	"strings"
)

// indexBasename is MEMORY.md's stem — reserved, never an individual
// memory's slug (§1).
const indexBasename = "MEMORY"

// MaxSlugChars caps a memory's filename stem, leaving headroom below the
// common 255-byte filename limit for the ".md" suffix and filesystem
// overhead — mirrors persistence's thread-id cap for the same reason.
const MaxSlugChars = 200

// slugPattern is the safe single-path-element charset: no separators, no
// "." or "..", so a name can never escape the memory dir when joined into
// a path (§3: write outside the dir is "impossible by construction").
var slugPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateSlug rejects any name that is not a safe, single-path-element
// slug: empty, containing a path separator, "." or "..", over-long, or
// colliding with the reserved index basename.
func validateSlug(name string) error {
	if name == "" {
		return ErrInvalidName
	}
	if len(name) > MaxSlugChars {
		return ErrInvalidName
	}
	if name == "." || name == ".." {
		return ErrInvalidName
	}
	if !slugPattern.MatchString(name) {
		return ErrInvalidName
	}
	if strings.EqualFold(name, indexBasename) {
		return ErrReservedName
	}
	return nil
}
