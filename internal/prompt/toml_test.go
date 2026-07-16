package prompt

// Dedicated tests for the hand-rolled TOML subset (toml.go) — the highest-risk
// file (it parses untrusted override packages) had no direct coverage, which is
// how the parser bugs the U4 review found survived. Each test cites the review
// finding it pins.

import (
	"strings"
	"testing"
)

// DeepSeek #1 / Sonnet #1 — tomlStripComment must honor backslash escaping so a
// value with an escaped quote before a '#' is not truncated. Write→parse must
// be the identity function the writer implies.
func TestTOML_EscapedQuoteRoundTrips(t *testing.T) {
	in := map[string]string{"note": `a"#b`}
	out := writeTOMLFlat(in)
	got, err := parseTOMLFlat(out)
	if err != nil {
		t.Fatalf("re-parse of writeTOMLFlat output failed: %v (wrote %q)", err, out)
	}
	if got["note"] != `a"#b` {
		t.Fatalf("round-trip lost data: got %q, want %q", got["note"], `a"#b`)
	}
}

// DeepSeek #3 / Sonnet #3 — a leading UTF-8 BOM must be stripped, not folded
// into the first key (which silently blanks manifest.name/base_version and
// defeats the §2a drift rail).
func TestTOML_LeadingBOMStripped(t *testing.T) {
	flat, err := parseTOMLFlat([]byte("\ufeffname = \"art\"\nbase_version = \"1.0.0\"\n"))
	if err != nil {
		t.Fatalf("parseTOMLFlat: %v", err)
	}
	if flat["name"] != "art" {
		t.Fatalf("BOM corrupted first key: got %#v (want name=art)", flat)
	}
	secs, err := parseTOMLSections([]byte("\ufeff[models.ornith]\nformat = \"flat\"\n"))
	if err != nil {
		t.Fatalf("parseTOMLSections: %v", err)
	}
	if _, ok := secs["models.ornith"]; !ok {
		t.Fatalf("BOM defeated section detection: got %#v", secs)
	}
}

// Security #4 — a value must not carry newline/control bytes (via strconv's \n,
// \x00 escapes): they are the parse-layer root of the dialect CONTRACT-line
// injection and can embed NUL into downstream text.
func TestTOML_RejectsControlCharsInValue(t *testing.T) {
	if _, err := parseTOMLFlat([]byte("k = \"a\\nb\"\n")); err == nil {
		t.Fatal("value with embedded newline was accepted, want error")
	}
	if _, err := parseTOMLFlat([]byte("k = \"a\\x00b\"\n")); err == nil {
		t.Fatal("value with embedded NUL was accepted, want error")
	}
	// A tab is benign and must still be allowed.
	if _, err := parseTOMLFlat([]byte("k = \"a\\tb\"\n")); err != nil {
		t.Fatalf("value with a tab was rejected: %v", err)
	}
}

// DeepSeek #7 / Sonnet — a duplicate key (flat or within a section) is a config
// mistake and must error rather than silently last-wins.
func TestTOML_DuplicateKeyErrors(t *testing.T) {
	if _, err := parseTOMLFlat([]byte("k = \"1\"\nk = \"2\"\n")); err == nil {
		t.Fatal("duplicate flat key accepted, want error")
	}
	if _, err := parseTOMLSections([]byte("[models.a]\nk = \"1\"\nk = \"2\"\n")); err == nil {
		t.Fatal("duplicate section key accepted, want error")
	}
}

// DeepSeek #4 — writeTOMLFlat must round-trip an empty value rather than drop
// the key.
func TestTOML_EmptyValueRoundTrips(t *testing.T) {
	out := writeTOMLFlat(map[string]string{"a": "x", "b": ""})
	if !strings.Contains(string(out), "b =") {
		t.Fatalf("empty value dropped on write: %q", out)
	}
}
