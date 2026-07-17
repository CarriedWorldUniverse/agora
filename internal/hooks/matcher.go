package hooks

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalidMatcherRegex is returned by MatchMatcher when the matcher isn't
// in exact/alternation mode and fails to compile as a regex. Spec §1.5:
// "Invalid regex → drop the group at discovery with a warning" — the caller
// (discover.go's Registry.ForEvent) is what turns this into a dropped group
// + warning; MatchMatcher itself just reports the error.
var ErrInvalidMatcherRegex = errors.New("hooks: invalid matcher regex")

// exactModeChars is the character class that puts a matcher in
// exact/alternation mode. Spec §1.5: "If the matcher contains only
// [A-Za-z0-9_|] → exact/alternation mode".
func isExactMode(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '|':
		default:
			return false
		}
	}
	return true
}

// MatchMatcher reports whether matcher matches the given matched string, per
// the semantics of §1.5:
//   - "", "*" → match everything.
//   - exact/alternation mode ([A-Za-z0-9_|] only) → split on '|', whole-string
//     equality against each alternative.
//   - otherwise → regex mode, UNANCHORED (regexp.MatchString semantics
//     already are; callers wanting anchoring write "^..." themselves).
//
// A nil matcher is represented by the empty string (MatcherGroup.Matcher is
// a plain string field, not *string — see config.go); "" and "*" are
// handled identically to a JSON `null` per spec.
func MatchMatcher(matcher, matched string) (bool, error) {
	if matcher == "" || matcher == "*" {
		return true, nil
	}
	if isExactMode(matcher) {
		for _, alt := range strings.Split(matcher, "|") {
			if alt == matched {
				return true, nil
			}
		}
		return false, nil
	}
	re, err := regexp.Compile(matcher)
	if err != nil {
		return false, ErrInvalidMatcherRegex
	}
	return re.MatchString(matched), nil
}
