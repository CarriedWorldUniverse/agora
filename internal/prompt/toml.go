package prompt

// A minimal, purpose-built TOML subset for this package's two file shapes:
// manifest.toml (flat string keys) and dialects.toml (one level of
// [section.subsection] tables holding flat string keys). Not a general TOML
// implementation — the module proxy available to this build had no reliable
// route to a third-party TOML library, and the schema here is small and
// entirely our own, so a tiny hand-rolled reader/writer is the pragmatic
// "tiny pure-Go" option the spec allows.
//
// Supported grammar:
//   - blank lines and full-line/trailing "# comment" are ignored
//   - "key = \"value\"" at top level (before any table header)
//   - "[a.b.c]" table headers; keys after a header nest under that path
//   - values are always double-quoted strings (no numbers/arrays/dates —
//     unneeded by manifest.toml / dialects.toml)

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func tomlStripComment(line string) string {
	inQuote := false
	esc := false
	for i, r := range line {
		if esc {
			// Previous rune was a backslash inside a quoted string: this rune
			// is literal (a \" does not close the string). Without this, an
			// escaped quote desynced the quote tracker and a later '#' was
			// wrongly treated as a comment (review finding, U4).
			esc = false
			continue
		}
		switch r {
		case '\\':
			if inQuote {
				esc = true
			}
		case '"':
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return line[:i]
			}
		}
	}
	return line
}

// stripBOM removes a leading UTF-8 byte-order mark, so a BOM-prefixed file
// (many Windows editors emit one) does not fold the mark into the first key or
// defeat "[section]" detection (review finding, U4).
func stripBOM(s string) string {
	return strings.TrimPrefix(s, "\ufeff")
}

// parseTOMLFlat parses top-level "key = \"value\"" pairs only. Any table
// header ends parsing of flat keys (manifest.toml never has tables).
func parseTOMLFlat(data []byte) (map[string]string, error) {
	out := map[string]string{}
	for lineNo, raw := range strings.Split(stripBOM(string(data)), "\n") {
		line := strings.TrimSpace(tomlStripComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// manifest.toml has no tables; stop rather than misparse.
			break
		}
		k, v, err := tomlParseKV(line)
		if err != nil {
			return nil, fmt.Errorf("toml: line %d: %w", lineNo+1, err)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("toml: line %d: duplicate key %q", lineNo+1, k)
		}
		out[k] = v
	}
	return out, nil
}

// parseTOMLSections parses "[a.b]" table headers followed by flat
// "key = \"value\"" pairs, returning section-path (joined by ".") -> key/value.
func parseTOMLSections(data []byte) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	section := ""
	for lineNo, raw := range strings.Split(stripBOM(string(data)), "\n") {
		line := strings.TrimSpace(tomlStripComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				return nil, fmt.Errorf("toml: line %d: empty table name %q", lineNo+1, line)
			}
			if _, ok := out[section]; ok {
				return nil, fmt.Errorf("toml: line %d: duplicate table [%s]", lineNo+1, section)
			}
			out[section] = map[string]string{}
			continue
		}
		k, v, err := tomlParseKV(line)
		if err != nil {
			return nil, fmt.Errorf("toml: line %d: %w", lineNo+1, err)
		}
		if section == "" {
			return nil, fmt.Errorf("toml: line %d: key %q outside any table", lineNo+1, k)
		}
		if _, dup := out[section][k]; dup {
			return nil, fmt.Errorf("toml: line %d: duplicate key %q in [%s]", lineNo+1, k, section)
		}
		out[section][k] = v
	}
	return out, nil
}

func tomlParseKV(line string) (key, value string, err error) {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return "", "", fmt.Errorf("expected key = value, got %q", line)
	}
	key = strings.TrimSpace(line[:eq])
	valPart := strings.TrimSpace(line[eq+1:])
	value, err = strconv.Unquote(valPart)
	if err != nil {
		return "", "", fmt.Errorf("expected quoted string value for %q, got %q: %w", key, valPart, err)
	}
	// A manifest/dialect value is a single flat string: reject embedded control
	// characters (newlines via \n, NUL via \x00, etc. survive strconv.Unquote).
	// Newlines here are the parse-layer root of the dialect CONTRACT-line
	// injection; NUL/controls can corrupt downstream consumers (review, U4).
	for _, r := range value {
		if r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return "", "", fmt.Errorf("value for %q contains a control character (U+%04X)", key, r)
		}
	}
	return key, value, nil
}

// writeTOMLFlat renders flat key/value pairs deterministically (sorted keys).
func writeTOMLFlat(kv map[string]string) []byte {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		// Write empty values too — dropping them made write→parse lossy for a
		// key that legitimately holds "" (review finding, U4).
		fmt.Fprintf(&b, "%s = %s\n", k, strconv.Quote(kv[k]))
	}
	return []byte(b.String())
}

// writeTOMLSections renders "[section]\nkey = \"value\"\n" blocks
// deterministically (sorted section names, sorted keys within).
func writeTOMLSections(sections map[string]map[string]string) []byte {
	names := make([]string, 0, len(sections))
	for s := range sections {
		names = append(names, s)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, s := range names {
		fmt.Fprintf(&b, "[%s]\n", s)
		kv := sections[s]
		keys := make([]string, 0, len(kv))
		for k := range kv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s = %s\n", k, strconv.Quote(kv[k]))
		}
		b.WriteString("\n")
	}
	return []byte(b.String())
}
