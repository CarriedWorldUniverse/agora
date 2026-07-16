package skills

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DefaultAGENTSBudgetBytes is the 32 KiB default context-doc budget.
// Spec: agora-spec-subagents.md §6.
const DefaultAGENTSBudgetBytes = 32 * 1024

// MaxAgentsFileBytes caps a single context-doc read BEFORE buffering, so a
// symlinked AGENTS.md → /dev/zero (or a huge real file) cannot OOM the host
// (review finding S2). Well above any realistic doc and the default budget;
// the per-doc budget truncation still applies on top of this.
const MaxAgentsFileBytes = 1 << 20 // 1 MiB

// DefaultAGENTSFilenames is the per-directory precedence list: first hit
// wins. Spec §6: "AGENTS.override.md > AGENTS.md > configured fallbacks
// (agora adds CLAUDE.md as a fallback filename for compat)."
var DefaultAGENTSFilenames = []string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.md"}

// AgentsDoc is one directory's chosen context-doc.
type AgentsDoc struct {
	Dir     string
	File    string // the filename that won precedence in this dir
	Content string
}

// AgentsDocs is the result of DiscoverAGENTSMD.
type AgentsDocs struct {
	ProjectRoot string
	Docs        []AgentsDoc
	Warnings    []string
}

// DiscoverAGENTSMD finds project root (nearest ancestor of cwd bearing a
// root marker) and collects context docs root -> cwd inclusive, one per
// directory (first hit of filenames precedence), skipping empty files,
// consumed in order until budgetBytes is exhausted (last doc truncated to
// fit). budgetBytes<=0 disables collection entirely.
// Spec: agora-spec-subagents.md §6.
func DiscoverAGENTSMD(cwd string, markers []string, filenames []string, budgetBytes int) *AgentsDocs {
	root := FindProjectRoot(cwd, markers)
	result := &AgentsDocs{ProjectRoot: root}
	if budgetBytes <= 0 {
		return result
	}
	if len(filenames) == 0 {
		filenames = DefaultAGENTSFilenames
	}

	dirs := ancestorChain(root, cwd)
	remaining := budgetBytes
	for _, dir := range dirs {
		if remaining <= 0 {
			break
		}
		file, content, found := pickAgentsFile(dir, filenames, root)
		if !found {
			continue
		}
		if len(content) > remaining {
			content = content[:remaining]
			result.Warnings = append(result.Warnings, fmt.Sprintf("AGENTS.md: %s truncated to fit the %d-byte budget", filepath.Join(dir, file), budgetBytes))
		}
		result.Docs = append(result.Docs, AgentsDoc{Dir: dir, File: file, Content: content})
		remaining -= len(content)
	}
	return result
}

// pickAgentsFile returns the first filenames-precedence file in dir whose
// content is non-empty. An empty (or whitespace-only) candidate is skipped and
// the next filename in the SAME directory is tried — so an empty
// AGENTS.override.md never suppresses a populated AGENTS.md beside it (spec §6:
// "first hit of ...; empty files skipped"; review finding F7). Reads are
// symlink-contained under projectRoot and size-capped (findings S1/S2).
func pickAgentsFile(dir string, filenames []string, projectRoot string) (file, content string, found bool) {
	for _, fn := range filenames {
		p := filepath.Join(dir, fn)
		data, _, err := safeReadUnder(p, projectRoot, true, MaxAgentsFileBytes)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == "" {
			continue // empty candidate: fall through to the next filename (§6)
		}
		return fn, string(data), true
	}
	return "", "", false
}

// RenderAGENTSFragment wraps the collected docs (plus an optional
// user-level instructions block, injected first) as the §6 user-role
// fragment:
//
//	# AGENTS.md instructions for <dir>
//
//	<INSTRUCTIONS>
//	{user-level}
//
//	--- project-doc ---
//
//	{project docs joined}
//	</INSTRUCTIONS>
//
// The "--- project-doc ---" separator is inserted exactly once (§6:
// "joined with a one-time separator"), between the user-level block and
// the project docs, not between individual project docs.
func RenderAGENTSFragment(dir string, userLevel string, docs *AgentsDocs) string {
	var body strings.Builder
	if strings.TrimSpace(userLevel) != "" {
		body.WriteString(userLevel)
	}
	if docs != nil && len(docs.Docs) > 0 {
		var parts []string
		for _, d := range docs.Docs {
			parts = append(parts, d.Content)
		}
		joined := strings.Join(parts, "\n\n")
		if body.Len() > 0 {
			body.WriteString("\n\n--- project-doc ---\n\n")
		}
		body.WriteString(joined)
	}
	return fmt.Sprintf("# AGENTS.md instructions for %s\n\n<INSTRUCTIONS>\n%s\n</INSTRUCTIONS>", dir, body.String())
}
