package turnengine

import (
	"strings"
	"testing"
)

// TestWarnOnce_DedupesIdenticalMessages pins the operator-visible defect:
// starting agora printed the same skills-catalog warning repeatedly, and
// every agent() spawn printed it again over a live TUI.
//
// DevProfile() runs on every NewManager construction (deliberately — see
// NewManager's precedence doc comment) and composes the whole dev prompt,
// so the same warning is produced once per Manager. The message depends on
// (skills on disk, budget), which cannot change within a process, so a
// repeat carries no information.
func TestWarnOnce_DedupesIdenticalMessages(t *testing.T) {
	resetWarnOnce()
	var got []string
	restore := swapWarnSink(func(s string) { got = append(got, s) })
	defer restore()

	warnOnce("turnengine: %s", "skills catalog: descriptions truncated to fit budget")
	warnOnce("turnengine: %s", "skills catalog: descriptions truncated to fit budget")
	warnOnce("turnengine: %s", "skills catalog: descriptions truncated to fit budget")

	if len(got) != 1 {
		t.Fatalf("printed %d times (%v); want 1 — repeats carry no new information and land on a live TUI", len(got), got)
	}
	if !strings.Contains(got[0], "truncated") {
		t.Errorf("wrong message: %q", got[0])
	}
}

// A DIFFERENT warning must still get through — dedupe must not swallow new
// information.
func TestWarnOnce_DistinctMessagesAllPrint(t *testing.T) {
	resetWarnOnce()
	var got []string
	restore := swapWarnSink(func(s string) { got = append(got, s) })
	defer restore()

	warnOnce("turnengine: %s", "skills catalog: descriptions truncated to fit budget")
	warnOnce("turnengine: skills discovery warning (%s): %s", "/x/SKILL.md", "bad frontmatter")
	warnOnce("turnengine: %s", "skills catalog: descriptions truncated to fit budget")

	if len(got) != 2 {
		t.Fatalf("printed %d distinct messages (%v); want 2", len(got), got)
	}
}
