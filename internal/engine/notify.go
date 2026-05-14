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

// notifyConventionPrompt is the agora-side system-prompt addendum
// that teaches the model the fenced-block convention. Appended after
// the nexus-composed personality so it always wins over any
// inherited convention.
const notifyConventionPrompt = "## Operator-only side channel\n\n" +
	"When you want to surface context to the operator without sending " +
	"it to nexus chat, output a fenced block at the end of your reply:\n\n" +
	"```notify-operator\n" +
	"<short message for the operator>\n" +
	"```\n\n" +
	"agora extracts these and renders them in the operator's TUI only — " +
	"the rest of your reply routes normally (to nexus chat for chat-source " +
	"turns, to the panel for tty-source turns).\n\n" +
	"Use sparingly. Good fits: mid-task status updates, heads-up about a " +
	"decision you're making, context the operator should see but the rest " +
	"of the cluster shouldn't. Not a substitute for chat replies."
