package skills

import (
	"errors"
	"regexp"
	"strings"
)

// envVarGuard is the case-insensitive set of shell env-var names that
// look like $mentions but must be ignored. Spec §4.
var envVarGuard = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "SHELL": true, "PWD": true,
	"TMPDIR": true, "TEMP": true, "TMP": true, "LANG": true, "TERM": true,
	"XDG_CONFIG_HOME": true,
}

// Mention is one $-sigil reference found in text. Path is set only for
// the linked form [$name](path); Linked is false for the bare $name form.
type Mention struct {
	Raw    string
	Name   string
	Path   string
	Linked bool
}

// mentionNameChars is the sigil's allowed name-character class.
// Spec §4: "[A-Za-z0-9_\-:]".
const mentionNameChars = `[A-Za-z0-9_\-:]+`

var linkedMentionRe = regexp.MustCompile(`\[\$(` + mentionNameChars + `)\]\(([^)]+)\)`)
var bareMentionRe = regexp.MustCompile(`\$(` + mentionNameChars + `)`)

// ExtractMentions scans text for $mentions, linked form first (so a bare
// bareMentionRe pass doesn't re-match the $name inside a already-matched
// linked mention).
// Spec: agora-spec-skills.md §4.
func ExtractMentions(text string) []Mention {
	var out []Mention
	consumed := make([]bool, len(text))

	for _, m := range linkedMentionRe.FindAllStringSubmatchIndex(text, -1) {
		full := text[m[0]:m[1]]
		name := text[m[2]:m[3]]
		path := text[m[4]:m[5]]
		for i := m[0]; i < m[1]; i++ {
			consumed[i] = true
		}
		if isEnvVarGuarded(name) {
			continue
		}
		out = append(out, Mention{Raw: full, Name: name, Path: path, Linked: true})
	}

	for _, m := range bareMentionRe.FindAllStringSubmatchIndex(text, -1) {
		if consumed[m[0]] {
			continue
		}
		full := text[m[0]:m[1]]
		name := text[m[2]:m[3]]
		if isEnvVarGuarded(name) {
			continue
		}
		out = append(out, Mention{Raw: full, Name: name})
	}
	return out
}

func isEnvVarGuarded(name string) bool {
	return envVarGuard[strings.ToUpper(name)]
}

// ErrAmbiguousMention is returned by ResolveMention when a bare name
// matches more than one skill. Spec §4: "Ambiguous ⇒ ignored."
var ErrAmbiguousMention = errors.New("skills: ambiguous mention name")

// ErrMentionNotFound is returned when neither the linked path nor the
// plain name resolves to a known skill.
var ErrMentionNotFound = errors.New("skills: mention does not resolve to a known skill")

// ResolveMention resolves a Mention against the known skill set, honoring
// the disabled-paths set (path present ⇒ disabled). Spec §4: "Resolution:
// exact path match first; then plain name only if globally unambiguous...
// Ambiguous ⇒ ignored."
func ResolveMention(m Mention, all []*Skill, disabledPaths map[string]bool) (*Skill, error) {
	if m.Linked && m.Path != "" {
		for _, sk := range all {
			if sk.Path == m.Path || sk.Dir == m.Path {
				if disabledPaths[sk.Path] {
					return nil, ErrMentionNotFound
				}
				return sk, nil
			}
		}
		// Linked form gave an explicit path that didn't match anything —
		// per §4 this is exact-path-first; fall through to name
		// resolution only if the path scheme suggests it's not a literal
		// skill path (best-effort; skip that nuance and just fail here,
		// matching "exact path match first").
		return nil, ErrMentionNotFound
	}

	var matches []*Skill
	for _, sk := range all {
		if sk.Name == m.Name && !disabledPaths[sk.Path] {
			matches = append(matches, sk)
		}
	}
	switch len(matches) {
	case 0:
		return nil, ErrMentionNotFound
	case 1:
		return matches[0], nil
	default:
		return nil, ErrAmbiguousMention
	}
}
