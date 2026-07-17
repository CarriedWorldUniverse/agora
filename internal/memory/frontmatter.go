package memory

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatterDelim matches an exact `---` delimiter line, mirroring
// internal/skills's frontmatter block convention (agora-spec-memory.md §1
// is explicitly "identical to the Claude Code memory format").
var frontmatterDelim = func(l string) bool { return strings.TrimSpace(l) == "---" }

// splitFrontmatter finds the leading YAML frontmatter block and returns its
// text plus the remaining body. The opening `---` must be the first
// non-empty line of the file.
func splitFrontmatter(data []byte) (fm string, body string, err error) {
	// Strip a leading UTF-8 BOM (else the first line is not "---" and the
	// file is silently skipped) and normalize CRLF so a Windows-authored
	// memory parses and its body carries no leading/embedded \r (U13 review).
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if frontmatterDelim(l) {
			start = i
		}
		break
	}
	if start == -1 {
		return "", "", errors.New("memory: no frontmatter: first non-empty line is not '---'")
	}

	end := -1
	for i := start + 1; i < len(lines); i++ {
		if frontmatterDelim(lines[i]) {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", errors.New("memory: no frontmatter: unterminated '---' block")
	}

	fm = strings.Join(lines[start+1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	// A single blank separator line between the closing delimiter and the
	// body is conventional (matches what writeEntryFile emits) but not
	// required; trim at most the leading blank line, never body content.
	body = strings.TrimPrefix(body, "\n")
	return fm, body, nil
}

// parseFrontmatter parses one memory file's bytes into a Frontmatter +
// body. Validates Type is one of the four allowed values and Name is
// non-empty (§1).
func parseFrontmatter(data []byte) (Frontmatter, string, error) {
	fmText, body, err := splitFrontmatter(data)
	if err != nil {
		return Frontmatter{}, "", err
	}
	var fm Frontmatter
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return Frontmatter{}, "", fmt.Errorf("memory: frontmatter YAML: %w", err)
	}
	if fm.Name == "" {
		return Frontmatter{}, "", ErrEmptyName
	}
	if !fm.Type.Valid() {
		return Frontmatter{}, "", fmt.Errorf("%w: %q", ErrInvalidType, fm.Type)
	}
	return fm, body, nil
}

// renderEntryFile builds a memory file's full on-disk bytes: the YAML
// frontmatter block followed by a blank line and the body.
func renderEntryFile(fm Frontmatter, body string) ([]byte, error) {
	fmYAML, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("memory: encode frontmatter: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(fmYAML)
	sb.WriteString("---\n\n")
	sb.WriteString(body)
	return []byte(sb.String()), nil
}
