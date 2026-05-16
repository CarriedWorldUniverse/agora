// notify_operator post-hoc parsing — NEX-63.
//
// The real engine (claudecode subprocess) can't call agora-hosted
// tools directly (SupportsCustomTools=false). Instead, the system
// prompt teaches the model to emit fenced `notify-operator` blocks
// in its reply, and this file strips them out post-turn — extracted
// content goes via TurnContext.NotifyOperator, the cleaned reply
// goes through normal Source-tag routing.
//
// This is a stopgap for v0. A proper in-process MCP server exposing
// notify_operator as a real tool call (so it can fire mid-turn) is
// a separate follow-up; the model still picks the right convention
// once that lands.
package engine

import (
	"regexp"
	"strings"
)

// notifyBlockPattern matches a fenced code block tagged
// `notify-operator`. The "(?s)" flag makes "." span newlines so the
// body can be multi-line. Leading/trailing whitespace inside the
// block is trimmed when the body is extracted.
var notifyBlockPattern = regexp.MustCompile("(?s)```notify-operator[ \\t]*\\n?(.*?)\\n?```[ \\t]*\\n?")

// extractNotifyBlocks scans text for `notify-operator` fenced blocks,
// returning the extracted bodies (in document order) and a cleaned
// version of text with those blocks removed. If no blocks match,
// returns (nil, text) unchanged.
func extractNotifyBlocks(text string) (notifications []string, cleaned string) {
	matches := notifyBlockPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil, text
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		// m[0]/m[1] = full-match start/end; m[2]/m[3] = group(1) body
		fullStart, fullEnd := m[0], m[1]
		bodyStart, bodyEnd := m[2], m[3]
		b.WriteString(text[last:fullStart])
		notifications = append(notifications, strings.TrimSpace(text[bodyStart:bodyEnd]))
		last = fullEnd
	}
	b.WriteString(text[last:])
	return notifications, strings.TrimSpace(b.String())
}

// AppendAgoraConventions returns base with the agora-side prompt
// conventions (notify_operator fenced block, etc.) appended. Safe
// to pass an empty base — the conventions stand on their own.
func AppendAgoraConventions(base string) string {
	if base == "" {
		return notifyConventionPrompt
	}
	return base + "\n\n---\n\n" + notifyConventionPrompt
}

// PanelSourcePrefix is the in-band marker agora wraps every tty-source
// inbox item with. Surfaces the routing intent directly in the user
// content so the model sees it on the same line as the operator's
// question; supplements (not replaces) the routing enforced by
// AgoraReturnHandler. NEX-129.
const PanelSourcePrefix = "[panel-route: this is a private operator question via the local TUI. " +
	"Your reply routes to the operator's panel only — do NOT call send_chat for the answer. " +
	"If you need to broadcast something to other aspects as part of answering, that's a " +
	"separate decision; the answer itself stays panel-local.]\n\n"

// notifyConventionPrompt is the agora-side system-prompt addendum
// that teaches the model the routing model + the notify-operator
// fenced-block convention. Appended after the nexus-composed
// personality so it always wins over any inherited convention.
const notifyConventionPrompt = "## Routing — chat vs. panel\n\n" +
	"agora drives two channels. Every turn arrives with a source tag visible in the " +
	"user content:\n\n" +
	"- **`[panel-route: ...]`** prefix → the operator typed this locally in the agora " +
	"TUI. Your reply stays in the panel — do NOT call `send_chat` for the answer. " +
	"Private debugging / introspection / one-on-one questions live here.\n" +
	"- **No panel-route prefix** → message came from nexus chat. Your reply goes to " +
	"the bus, threaded on the triggering message id. Visible to all peer aspects.\n\n" +
	"Routing is enforced agora-side regardless, but checking the source prefix lets " +
	"you avoid emitting chat-bound side-effects (mid-turn `send_chat` calls) on a " +
	"panel-route turn — those broadcast before the routing layer can intercept.\n\n" +
	"## Operator-only side channel\n\n" +
	"When you want to surface context to the operator on a chat-route turn without " +
	"broadcasting it, output a fenced block at the end of your reply:\n\n" +
	"```notify-operator\n" +
	"<short message for the operator>\n" +
	"```\n\n" +
	"agora extracts these and renders them in the operator's TUI only — " +
	"the rest of your reply routes normally (chat for chat-source, panel for tty-source).\n\n" +
	"Use sparingly. Good fits: mid-task status updates, heads-up about a " +
	"decision you're making, context the operator should see but the rest " +
	"of the cluster shouldn't. Not a substitute for chat replies."
