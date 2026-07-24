package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discover.go loads agent defs off disk. ParseAgentDefMD (agentdef.go) has
// always known how to read one file; nothing ever found the files. This is
// that half: walk the spec's directories, parse each *.md, and hand the
// result to NewRegistry in the precedence order it documents.
//
// Spec: agora-spec-subagents.md §1 — "Location: <project>/.agora/agents/*.md
// and ~/.agora/agents/*.md (also read .claude/agents/*.md for import/compat).
// One file = one agent type."

// AgentRoot is one directory to scan for agent defs, with the scope it
// contributes. Order in a []AgentRoot IS precedence order: earlier roots
// win on a name collision (NewRegistry's documented contract — "defs is
// already caller-ordered highest-precedence-first").
type AgentRoot struct {
	Path  string
	Scope string
}

// Agent-def scopes, mirroring internal/skills' vocabulary so the two
// discovery surfaces read the same way in /agents and /skills output.
const (
	AgentScopeRepo = "repo"
	AgentScopeUser = "user"
)

// AgentWarning is a non-fatal problem hit while scanning — an unreadable
// directory, a malformed def. Discovery never fails the session over one
// bad file: a typo'd agent def must not stop the harness from starting.
type AgentWarning struct {
	Path    string
	Message string
}

// DefaultAgentRoots builds the production root list in precedence order:
// project before user, agora before claude-compat. projectRoot and home
// must be absolute, cleaned paths.
//
// The .claude/agents entries are the spec's compat lane, kept LAST within
// each scope so a project's own .agora def always beats an imported one.
func DefaultAgentRoots(projectRoot, home string) []AgentRoot {
	return []AgentRoot{
		{Path: filepath.Join(projectRoot, ".agora", "agents"), Scope: AgentScopeRepo},
		{Path: filepath.Join(projectRoot, ".claude", "agents"), Scope: AgentScopeRepo},
		{Path: filepath.Join(home, ".agora", "agents"), Scope: AgentScopeUser},
		{Path: filepath.Join(home, ".claude", "agents"), Scope: AgentScopeUser},
	}
}

// DiscoverAgentDefs scans roots in order and returns every def found,
// ordered highest-precedence-first and deduped by NAME (first-seen wins,
// which is the highest-precedence occurrence given roots are pre-ordered).
// The result is ready to hand straight to NewRegistry.
//
// Deduping here rather than leaning on NewRegistry's own collision rule is
// deliberate: NewRegistry lets a later def override a BUILTIN name, and we
// want that (a user's own "explore" should win), but we do not want a
// user-scope def silently shadowing the project-scope def of the same name
// that we already accepted.
func DiscoverAgentDefs(roots []AgentRoot) ([]*AgentDef, []AgentWarning) {
	var defs []*AgentDef
	var warnings []AgentWarning
	seenName := map[string]bool{}
	seenDir := map[string]bool{}

	for _, root := range roots {
		// A root listed twice (home == projectRoot, a symlinked alias) must
		// not double-report its defs as collisions.
		key := canonicalAgentDir(root.Path)
		if seenDir[key] {
			continue
		}
		seenDir[key] = true

		found, warns := scanAgentRoot(root)
		warnings = append(warnings, warns...)
		for _, d := range found {
			if seenName[d.Name] {
				continue
			}
			seenName[d.Name] = true
			defs = append(defs, d)
		}
	}
	return defs, warnings
}

// scanAgentRoot reads one directory's *.md files. A missing directory is
// the common case (most projects have no .agora/agents), not a warning.
func scanAgentRoot(root AgentRoot) ([]*AgentDef, []AgentWarning) {
	entries, err := os.ReadDir(root.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []AgentWarning{{Path: root.Path, Message: fmt.Sprintf("cannot read agent directory: %v", err)}}
	}

	var defs []*AgentDef
	var warnings []AgentWarning
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		path := filepath.Join(root.Path, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, AgentWarning{Path: path, Message: fmt.Sprintf("cannot read: %v", err)})
			continue
		}
		fallback := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		def, err := ParseAgentDefMD(data, fallback)
		if err != nil {
			warnings = append(warnings, AgentWarning{Path: path, Message: err.Error()})
			continue
		}
		def.Path = path
		defs = append(defs, def)
	}

	// Stable within a root: filename order, so two defs in one directory
	// resolve deterministically rather than by readdir order.
	sort.SliceStable(defs, func(i, j int) bool { return defs[i].Path < defs[j].Path })
	return defs, warnings
}

// canonicalAgentDir resolves symlinks for use as a dedup key, so a
// directory reachable under two root entries is scanned once. Falls back
// to a lexical clean when the path can't be resolved (the usual reason
// being that it doesn't exist, which scanAgentRoot then skips anyway).
func canonicalAgentDir(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	return filepath.Clean(dir)
}
