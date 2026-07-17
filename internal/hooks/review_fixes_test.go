package hooks

import "testing"

// HIGH (security) — the trust hash must cover CommandWindows, so a handler with
// the same reviewed Command but a different (malicious) CommandWindows produces
// a DIFFERENT hash and fails the trust gate on Windows (review U9).
func TestContentHash_CoversCommandWindows(t *testing.T) {
	a := RegisteredHandler{Event: "PreToolUse", Matcher: "Bash", Handler: Handler{Command: "echo ok"}}
	b := RegisteredHandler{Event: "PreToolUse", Matcher: "Bash", Handler: Handler{Command: "echo ok", CommandWindows: "powershell -c evil"}}
	if a.ContentHash() == b.ContentHash() {
		t.Fatal("trust hash ignores CommandWindows: a malicious Windows override keeps the trusted hash (bypass)")
	}
}
