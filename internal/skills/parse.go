package skills

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Length limits from the frontmatter table.
// Spec: agora-spec-skills.md §1.1.
const (
	MaxNameChars           = 64
	MaxNamespacedNameChars = 128
	MaxDescriptionChars    = 1024
)

// nameCap returns the char cap for a name: 128 when it is namespace-qualified
// (contains ':'), else 64. Spec §1.1: "≤64 chars (qualified w/ namespace
// ≤128)". Review finding F5.
func nameCap(name string) int {
	if strings.Contains(name, ":") {
		return MaxNamespacedNameChars
	}
	return MaxNameChars
}

// ErrEmptyDescription is returned when SKILL.md frontmatter lacks a
// description (or it sanitizes to empty). Spec §1.1: "required (non-empty
// or skill errors)".
var ErrEmptyDescription = errors.New("skills: description is required and must be non-empty")

// frontmatterDoc is the strict shape read out of the YAML block. Unknown
// keys are ignored by yaml.v3's default Unmarshal (§1.1 "Unknown
// frontmatter keys ignored").
type frontmatterDoc struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		ShortDescription string `yaml:"short-description"`
	} `yaml:"metadata"`
}

// ParseSkillMD parses one SKILL.md file's bytes. dirName is the parent
// directory's base name, used as the Name fallback (§1.1). dir/path are
// recorded on the result for callers to fill in (kept separate from
// parsing so this function is pure over bytes).
// Spec: agora-spec-skills.md §1, §1.1.
func ParseSkillMD(data []byte, dirName string) (*Skill, error) {
	fm, _, err := extractFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}

	var doc frontmatterDoc
	if err := yaml.Unmarshal([]byte(fm), &doc); err != nil {
		repaired, ok := repairYAML(fm)
		if !ok {
			return nil, fmt.Errorf("skills: frontmatter YAML: %w", err)
		}
		if err2 := yaml.Unmarshal([]byte(repaired), &doc); err2 != nil {
			return nil, fmt.Errorf("skills: frontmatter YAML (after repair): %w", err2)
		}
	}

	name := sanitizeLine(doc.Name)
	if name == "" {
		name = sanitizeLine(dirName)
	}
	name = truncateChars(name, nameCap(name))
	// If truncation stripped the namespace separator (a colon at/beyond the
	// 128 cap), the result is effectively a plain name and must not keep the
	// wider namespaced budget — re-cap to the plain 64-char limit (review
	// delta #2; nameCap was decided on the untruncated string).
	if !strings.Contains(name, ":") {
		name = truncateChars(name, MaxNameChars)
	}

	desc := sanitizeLine(doc.Description)
	if desc == "" {
		return nil, ErrEmptyDescription
	}
	// Cap WITH the "..." marker (spec §1.1: "≤1024 effective at render,
	// truncate with '...'"). A plain char-cut here followed by the catalog's
	// truncateWithEllipsis(1024) no-op'd (string already exactly 1024 runes),
	// silently dropping the marker (review finding F6).
	desc = truncateWithEllipsis(desc, MaxDescriptionChars)

	short := sanitizeLine(doc.Metadata.ShortDescription)

	return &Skill{
		Name:             name,
		Description:      desc,
		ShortDescription: short,
	}, nil
}

// ParseSidecar parses agents/openai.yaml. Per §1.2, all blocks are
// optional and a parse failure of any kind yields a zero-value Sidecar —
// this function never returns an error; a skill's sidecar is never a
// reason to fail loading the skill.
func ParseSidecar(data []byte) Sidecar {
	var sc Sidecar
	if err := yaml.Unmarshal(data, &sc); err != nil {
		return Sidecar{}
	}
	if len([]rune(sc.Interface.DisplayName)) > 64 {
		sc.Interface.DisplayName = ""
	}
	sc.Interface.ShortDescription = truncateChars(sc.Interface.ShortDescription, 1024)
	sc.Interface.DefaultPrompt = truncateChars(sc.Interface.DefaultPrompt, 1024)
	if sc.Interface.IconSmall != "" && !safeAssetPath(sc.Interface.IconSmall) {
		sc.Interface.IconSmall = ""
	}
	if sc.Interface.IconLarge != "" && !safeAssetPath(sc.Interface.IconLarge) {
		sc.Interface.IconLarge = ""
	}
	if sc.Interface.BrandColor != "" && len(sc.Interface.BrandColor) != 7 {
		sc.Interface.BrandColor = ""
	}
	return sc
}

// safeAssetPath reports whether a sidecar icon path is a safe repo-relative
// asset reference: under "assets/", not absolute, and free of ".." traversal.
// The old check was prefix-only, so "assets/../../etc/passwd" passed (review
// finding S3, defense-in-depth for a future asset-serving consumer).
func safeAssetPath(p string) bool {
	return strings.HasPrefix(p, "assets/") &&
		!strings.HasPrefix(p, "/") &&
		!strings.Contains(p, "..")
}

// frontmatterDelim matches the exact `---` delimiter lines.
var frontmatterDelim = regexp.MustCompile(`^---\s*$`)

// extractFrontmatter finds the YAML frontmatter block. Per §1.1, the
// delimiters must be exact `---` lines and the opener must be the first
// non-empty content of the file. Returns the frontmatter text and the
// remaining body.
func extractFrontmatter(data []byte) (fm string, body string, err error) {
	text := string(data)
	lines := strings.Split(text, "\n")

	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if frontmatterDelim.MatchString(l) {
			start = i
		}
		break
	}
	if start == -1 {
		return "", "", errors.New("no frontmatter: first non-empty line is not '---'")
	}

	end := -1
	for i := start + 1; i < len(lines); i++ {
		if frontmatterDelim.MatchString(lines[i]) {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", errors.New("no frontmatter: unterminated '---' block")
	}

	fm = strings.Join(lines[start+1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return fm, body, nil
}

// yamlOffendingValue matches a `key: value` line whose value contains a
// literal ": " or starts with one of [ { @ ` — the shapes strict YAML
// chokes on unquoted. Spec §1.1 lenient-YAML repair.
var yamlKeyValueLine = regexp.MustCompile(`^(\s*[A-Za-z0-9_.\-]+:)(\s+)(.*)$`)

// repairYAML quotes offending unquoted scalar values so a second parse
// attempt can succeed. Spec §1.1: "if strict YAML fails, quote unquoted
// scalar values containing '": "' or leading `[ { @ \“ ... then retry."
// Returns ok=false if no line looked repairable (caller then reports the
// original error).
func repairYAML(fm string) (string, bool) {
	lines := strings.Split(fm, "\n")
	changed := false
	for i, l := range lines {
		m := yamlKeyValueLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		key, sep, val := m[1], m[2], m[3]
		if val == "" {
			continue
		}
		// Already quoted — leave alone.
		if (strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`)) ||
			(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
			continue
		}
		offends := strings.Contains(val, ": ") ||
			strings.HasPrefix(val, "[") || strings.HasPrefix(val, "{") ||
			strings.HasPrefix(val, "@") || strings.HasPrefix(val, "`")
		if !offends {
			continue
		}
		quoted := `"` + strings.ReplaceAll(val, `"`, `\"`) + `"`
		lines[i] = key + sep + quoted
		changed = true
	}
	if !changed {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

// whitespaceRun collapses any run of whitespace (including newlines) to a
// single space. Spec §1.1: "Whitespace runs collapse to single spaces."
var whitespaceRun = regexp.MustCompile(`\s+`)

func sanitizeLine(s string) string {
	return strings.TrimSpace(whitespaceRun.ReplaceAllString(s, " "))
}

// truncateChars truncates s to at most n runes, safe for multi-byte text.
func truncateChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
