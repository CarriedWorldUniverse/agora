package skills

import (
	"path/filepath"
	"strings"
)

// scriptInterpreters is the argv[0] allowlist for script-run detection.
// Spec §5.
var scriptInterpreters = map[string]bool{
	"python": true, "python3": true, "bash": true, "zsh": true, "sh": true,
	"node": true, "deno": true, "ruby": true, "perl": true, "pwsh": true,
}

// scriptExtensions is the recognized-script-file extension set. Spec §5.
var scriptExtensions = map[string]bool{
	".py": true, ".sh": true, ".js": true, ".ts": true, ".rb": true,
	".pl": true, ".ps1": true,
}

// DetectScriptRunPath inspects an argv (as the harness observed a Bash
// tool call) and, if it looks like an interpreter invocation of a known
// script extension, returns the resolved script path. ok=false means this
// argv isn't a recognized script-run shape at all (not: "no skill
// matched" — that's MatchScriptRun's job).
// Spec: agora-spec-skills.md §5.
func DetectScriptRunPath(argv []string) (path string, ok bool) {
	if len(argv) < 2 {
		return "", false
	}
	interp := filepath.Base(argv[0])
	if !scriptInterpreters[interp] {
		return "", false
	}
	for _, a := range argv[1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(a))
		if scriptExtensions[ext] {
			return a, true
		}
		return "", false // first non-flag arg isn't a recognized script
	}
	return "", false
}

// MatchScriptRun resolves a script-run argv to the skill whose scripts/
// dir contains it, if any (§5: "walk ancestors, match a skill's scripts/
// dir"). scriptPath should already be resolved to an absolute path by the
// caller (the harness knows the tool call's cwd; this function is
// deliberately cwd-agnostic and takes the already-resolved path).
func MatchScriptRun(argv []string, all []*Skill) (*Skill, bool) {
	scriptPath, ok := DetectScriptRunPath(argv)
	if !ok {
		return nil, false
	}
	return matchUnderScripts(scriptPath, all)
}

func matchUnderScripts(scriptPath string, all []*Skill) (*Skill, bool) {
	scriptPath = filepath.Clean(scriptPath)
	for _, sk := range all {
		scriptsDir := filepath.Clean(sk.ScriptsDir())
		rel, err := filepath.Rel(scriptsDir, scriptPath)
		if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
			continue
		}
		return sk, true
	}
	return nil, false
}

// MatchDocRead resolves a Read tool call's path to the skill it belongs
// to, when the path is a known SKILL.md. Spec §5: "a Read of a known
// SKILL.md path → that skill."
func MatchDocRead(readPath string, all []*Skill) (*Skill, bool) {
	readPath = filepath.Clean(readPath)
	for _, sk := range all {
		if filepath.Clean(sk.Path) == readPath {
			return sk, true
		}
	}
	return nil, false
}
