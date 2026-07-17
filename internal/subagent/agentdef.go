package subagent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentDef is one parsed agent definition — a markdown file with YAML
// frontmatter, Claude Code's format adopted verbatim.
// Spec: agora-spec-subagents.md §1.
type AgentDef struct {
	// Name identifies the agent type (the agent_type opt on agent()).
	Name string
	// Description is ROUTING TEXT for the calling model — when to delegate
	// to this agent. Required, non-empty.
	Description string
	// Tools is the allowlist; nil (frontmatter key omitted) means "all
	// tools" — spec §1: "tools: ...  # allowlist; omit = all tools".
	Tools []string
	// Model optionally overrides the inherited model. "" = inherit parent.
	Model string
	// Effort optionally overrides the inherited reasoning effort. "" = inherit.
	Effort string
	// Prompt is the body — the agent's system prompt (spec §1: "the body is
	// written for the agent — how to do the job").
	Prompt string
	// Path is the source file, when loaded from disk ("" for built-ins).
	Path string
}

// frontmatterDoc is the strict YAML shape read out of an agent def's
// frontmatter block. Unknown keys are ignored by yaml.v3's default Unmarshal.
type agentFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Tools       string `yaml:"tools"`
	Model       string `yaml:"model"`
	Effort      string `yaml:"effort"`
}

// frontmatterDelim matches the exact `---` delimiter lines (Claude Code
// frontmatter convention — same shape internal/skills' SKILL.md uses; kept
// package-local since each consumer owns its own file format per spec).
var agentFrontmatterDelim = regexp.MustCompile(`^---\s*$`)

// ParseAgentDefMD parses one agent-def markdown file's bytes. fallbackName is
// used as Name when the frontmatter omits it (conventionally the file's base
// name without extension). Spec: agora-spec-subagents.md §1.
func ParseAgentDefMD(data []byte, fallbackName string) (*AgentDef, error) {
	fm, body, err := extractAgentFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("subagent: %w", err)
	}

	var doc agentFrontmatter
	if err := yaml.Unmarshal([]byte(fm), &doc); err != nil {
		return nil, fmt.Errorf("subagent: frontmatter YAML: %w", err)
	}

	name := strings.TrimSpace(doc.Name)
	if name == "" {
		name = strings.TrimSpace(fallbackName)
	}
	if name == "" {
		return nil, ErrAgentDefEmptyName
	}

	desc := strings.TrimSpace(doc.Description)
	if desc == "" {
		return nil, ErrAgentDefEmptyDescription
	}

	var tools []string
	if strings.TrimSpace(doc.Tools) != "" {
		for _, t := range strings.Split(doc.Tools, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tools = append(tools, t)
			}
		}
	}

	return &AgentDef{
		Name:        name,
		Description: desc,
		Tools:       tools,
		Model:       strings.TrimSpace(doc.Model),
		Effort:      strings.TrimSpace(doc.Effort),
		Prompt:      strings.TrimSpace(strings.TrimPrefix(body, "\n")),
	}, nil
}

// extractAgentFrontmatter finds the YAML frontmatter block. The opening
// `---` must be the first non-empty line of the file.
func extractAgentFrontmatter(data []byte) (fm string, body string, err error) {
	lines := strings.Split(string(data), "\n")

	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if agentFrontmatterDelim.MatchString(l) {
			start = i
		}
		break
	}
	if start == -1 {
		return "", "", fmt.Errorf("no frontmatter: first non-empty line is not '---'")
	}

	end := -1
	for i := start + 1; i < len(lines); i++ {
		if agentFrontmatterDelim.MatchString(lines[i]) {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", fmt.Errorf("no frontmatter: unterminated '---' block")
	}

	fm = strings.Join(lines[start+1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return fm, body, nil
}

// BuiltinGeneralPurpose and BuiltinExplore ship without any user definition
// (spec §1: "Built-ins: a general-purpose agent (all tools) and an explore
// agent (read-only search) cover most delegation without any user
// definitions").
const (
	BuiltinGeneralPurpose = "general-purpose"
	BuiltinExplore        = "explore"
)

// BuiltinAgentDefs returns the two shipped built-in agent defs.
func BuiltinAgentDefs() []*AgentDef {
	return []*AgentDef{
		{
			Name:        BuiltinGeneralPurpose,
			Description: "General-purpose agent for researching complex questions, searching for code, and executing multi-step tasks. Use when the task doesn't match a more specific agent.",
			Tools:       nil, // all tools
			Prompt:      "You are a general-purpose subagent. Complete the delegated task and return the result as your final message — raw data for the parent, not prose for a human.",
		},
		{
			Name:        BuiltinExplore,
			Description: "Read-only exploration agent for searching/understanding a codebase without making changes. Use for research delegations that must not mutate anything.",
			Tools:       []string{"Read", "Glob", "Grep"},
			Prompt:      "You are a read-only exploration subagent. Investigate and return findings as your final message; you have no write/execute tools.",
		},
	}
}

// Registry indexes agent defs by name, builtins plus discovered defs, with
// deterministic lookup/listing.
type Registry struct {
	byName map[string]*AgentDef
}

// NewRegistry builds a Registry from builtins plus defs, in precedence
// order: an earlier def in defs wins a name collision over a later one;
// any def wins over a builtin of the same name (a project may redefine
// "explore", say).
func NewRegistry(defs []*AgentDef) *Registry {
	r := &Registry{byName: make(map[string]*AgentDef)}
	for _, d := range BuiltinAgentDefs() {
		r.byName[d.Name] = d
	}
	// Later entries in defs must NOT override earlier ones (defs is already
	// caller-ordered highest-precedence-first, e.g. project before user).
	for _, d := range defs {
		if _, exists := r.byName[d.Name]; exists {
			// Only builtins are pre-seeded above; a real (non-builtin) prior
			// def already in the map takes precedence per caller order.
			if !isBuiltinName(d.Name) {
				continue
			}
		}
		r.byName[d.Name] = d
	}
	return r
}

func isBuiltinName(name string) bool {
	return name == BuiltinGeneralPurpose || name == BuiltinExplore
}

// Get returns the named agent def, ok=false if unknown.
func (r *Registry) Get(name string) (*AgentDef, bool) {
	d, ok := r.byName[name]
	return d, ok
}

// Names returns all registered names, sorted (deterministic listing).
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
