package hooks

import "testing"

// TestMatchMatcher_Semantics is transcribed from spec §1.5.
func TestMatchMatcher_Semantics(t *testing.T) {
	cases := []struct {
		name    string
		matcher string
		matched string
		want    bool
		wantErr bool
	}{
		{"empty matches everything", "", "Bash", true, false},
		{"star matches everything", "*", "anything", true, false},
		{"exact match", "Bash", "Bash", true, false},
		{"exact no substring match", "Bash", "BashOutput", false, false},
		{"alternation matches first", "Edit|Write", "Edit", true, false},
		{"alternation matches second", "Edit|Write", "Write", true, false},
		{"alternation no match", "Edit|Write", "Read", false, false},
		{"literal mcp tool name", "mcp__memory__create_entities", "mcp__memory__create_entities", true, false},
		{"literal mcp tool name no match", "mcp__memory__create_entities", "mcp__memory__other", false, false},
		// §1.5's own example: "^Bash matches BashOutput" — unanchored search
		// means "^" only pins the match to the START of the string, and
		// "BashOutput" starts with "Bash", so it matches.
		{"regex mode leading-anchor still matches prefix (spec's own example)", "^Bash", "BashOutput", true, false},
		{"regex mode substring match via dot", "B.sh", "XXBashXX", true, false},
		{"invalid regex dropped with error", "(unclosed", "anything", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchMatcher(tc.matcher, tc.matched)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("MatchMatcher(%q, %q): want error, got nil", tc.matcher, tc.matched)
				}
				return
			}
			if err != nil {
				t.Fatalf("MatchMatcher(%q, %q): unexpected error: %v", tc.matcher, tc.matched, err)
			}
			if got != tc.want {
				t.Errorf("MatchMatcher(%q, %q) = %v, want %v", tc.matcher, tc.matched, got, tc.want)
			}
		})
	}
}

// TestMatchMatcher_RegexModeUnanchored proves §1.5's specific example:
// "regex mode, UNANCHORED match (^Bash matches BashOutput; anchor
// explicitly)". Note the anchor is INSIDE the matcher here (no leading ^),
// so the unanchored regex "Bash" matches "BashOutput" as a substring.
func TestMatchMatcher_RegexModeUnanchored(t *testing.T) {
	// "Bash.*" contains a regex metachar (.) so it's regex mode, not exact
	// mode, and unanchored — matches anywhere in the string.
	got, err := MatchMatcher("Bash.*", "XBashOutput")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Errorf("unanchored regex %q should match %q as a substring", "Bash.*", "XBashOutput")
	}
}

// TestMatchMatcher_AnchoredExplicit proves the anchor-explicitly half of
// §1.5's example: "^Bash matches BashOutput; anchor explicitly [to prevent
// that]" — i.e. ^Bash$ anchors both ends and rejects BashOutput.
func TestMatchMatcher_AnchoredExplicit(t *testing.T) {
	got, err := MatchMatcher("^Bash$", "BashOutput")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Errorf("fully-anchored ^Bash$ must not match BashOutput")
	}
	got, err = MatchMatcher("^Bash$", "Bash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Errorf("fully-anchored ^Bash$ must match exact Bash")
	}
}
