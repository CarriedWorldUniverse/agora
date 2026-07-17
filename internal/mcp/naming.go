package mcp

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
)

// Model-visible MCP tool naming. Spec: agora-spec-mcp.md §2 ("Tool naming").
const (
	ToolNamePrefix = "mcp__"
	ToolNameDelim  = "__"
	// MaxToolNameLen is the hard cap; overflow/collision truncates and
	// appends a 12-hex SHA1 suffix.
	MaxToolNameLen = 64
	hashSuffixLen  = 12
)

// ToolIdentity is a raw (server, tool) pair before qualification — kept
// separately from the qualified visible name so protocol calls always use
// the raw names (§2: "keep raw server/tool names separately for protocol
// calls").
type ToolIdentity struct {
	Server string
	Tool   string
}

// NamedTool pairs a raw identity with its assigned model-visible Name.
type NamedTool struct {
	ToolIdentity
	Name string
}

// qualify builds the unqualified candidate name: mcp__<server>__<tool>.
// Because server/tool names themselves may contain "__", two distinct
// identities can produce the identical raw candidate (e.g. server "foo",
// tool "bar__baz" vs server "foo__bar", tool "baz") — this is exactly the
// "collision" §2 has the hash-suffix rule for, not just length overflow.
func qualify(server, tool string) string {
	return ToolNamePrefix + server + ToolNameDelim + tool
}

// hashSuffix is the 12-hex-char SHA1 suffix §2 specifies, computed over the
// raw (unqualified) candidate name so it is stable regardless of how many
// other tools happen to be registered alongside it.
func hashSuffix(raw string) string {
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])[:hashSuffixLen]
}

// truncatedWithHash renders raw truncated to fit MaxToolNameLen with a
// "-<12hex>" suffix appended, deterministic for a given raw string.
func truncatedWithHash(raw string) string {
	suffix := "-" + hashSuffix(raw)
	maxBase := MaxToolNameLen - len(suffix)
	base := raw
	if len(base) > maxBase {
		base = base[:maxBase]
	}
	return base + suffix
}

// AssignNames deterministically qualifies a batch of (server, tool) pairs:
// sorted by raw identity (§2: "deterministic ordering by raw identity"),
// short/unique names pass through as mcp__<server>__<tool>, and any name
// that overflows MaxToolNameLen or collides with an already-assigned name
// is truncated + hash-suffixed. A residual collision after hash-suffixing
// (astronomically unlikely — would need a raw-string collision under the
// hash-suffix truncation) is broken by appending an incrementing numeric
// disambiguator, kept deterministic by the same sort order.
func AssignNames(pairs []ToolIdentity) []NamedTool {
	sorted := make([]ToolIdentity, len(pairs))
	copy(sorted, pairs)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Server != sorted[j].Server {
			return sorted[i].Server < sorted[j].Server
		}
		return sorted[i].Tool < sorted[j].Tool
	})

	seen := make(map[string]bool, len(sorted))
	out := make([]NamedTool, 0, len(sorted))
	for _, id := range sorted {
		raw := qualify(id.Server, id.Tool)
		name := raw
		if len(raw) > MaxToolNameLen || seen[name] {
			name = truncatedWithHash(raw)
		}
		for i := 2; seen[name]; i++ {
			name = fmt.Sprintf("%s-%d", truncatedWithHash(raw+fmt.Sprint(i)), i)
			if len(name) > MaxToolNameLen {
				name = name[len(name)-MaxToolNameLen:]
			}
		}
		seen[name] = true
		out = append(out, NamedTool{ToolIdentity: id, Name: name})
	}
	return out
}
