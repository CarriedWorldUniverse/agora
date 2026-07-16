package skills

import "path/filepath"

// Scope names a discovery root's provenance tier.
// Spec: agora-spec-skills.md §2 (roots), used for both dedup precedence
// (Repo > User > System > Admin, the discovery-list order) and catalog
// render order (System < Admin < Repo < User, §2 "Prompt-ordering rank").
type Scope string

const (
	ScopeSystem Scope = "system"
	ScopeAdmin  Scope = "admin"
	ScopeRepo   Scope = "repo"
	ScopeUser   Scope = "user"
)

// discoveryRank orders scopes for dedup-winner selection while walking
// roots in discovery order: Repo (project + repo .agents/skills) beats
// User, which beats System, which beats Admin.
// Spec §2 numbered root list (1&2 Repo, 3 User, 4 System, 5 Admin).
func discoveryRank(s Scope) int {
	switch s {
	case ScopeRepo:
		return 0
	case ScopeUser:
		return 1
	case ScopeSystem:
		return 2
	case ScopeAdmin:
		return 3
	default:
		return 4
	}
}

// renderRank orders scopes for catalog display.
// Spec §2: "Prompt-ordering rank: System < Admin < Repo < User, then name,
// then path."
func renderRank(s Scope) int {
	switch s {
	case ScopeSystem:
		return 0
	case ScopeAdmin:
		return 1
	case ScopeRepo:
		return 2
	case ScopeUser:
		return 3
	default:
		return 4
	}
}

// Skill is one discovered SKILL.md, parsed and sanitized.
// Spec: agora-spec-skills.md §1 (format), §1.1 (frontmatter).
type Skill struct {
	// Name defaults to the parent directory name when frontmatter omits it.
	Name string
	// Description is required non-empty; a skill whose frontmatter has an
	// empty description fails to parse (§1.1).
	Description string
	// ShortDescription is metadata.short-description, optional.
	ShortDescription string

	// Dir is the skill's directory (canonical, absolute).
	Dir string
	// Path is the SKILL.md file path within Dir.
	Path string
	// Scope is the discovery root's provenance tier.
	Scope Scope
	// RootPath is the discovery root this skill was found under (for the
	// catalog's path-alias optimization, §3.2).
	RootPath string

	// Sidecar is the parsed agents/openai.yaml, if present. Never nil —
	// a missing or unparsable sidecar yields a zero-value Sidecar with
	// AllowImplicitInvocation defaulting true.
	Sidecar Sidecar
}

// AllowImplicitInvocation reports whether this skill belongs in the
// every-turn catalog. Spec §1.2 policy.allow_implicit_invocation (default
// true); false hides it from the catalog but leaves it $-mention-invocable
// (§3.1, §4).
func (s *Skill) AllowImplicitInvocation() bool {
	if s.Sidecar.Policy.AllowImplicitInvocation == nil {
		return true
	}
	return *s.Sidecar.Policy.AllowImplicitInvocation
}

// ScriptsDir is the conventional scripts/ subdir used by implicit
// script-run detection (§5).
func (s *Skill) ScriptsDir() string {
	return filepath.Join(s.Dir, "scripts")
}

// SidecarTool is one dependencies.tools[] entry.
// Spec: agora-spec-skills.md §1.2.
type SidecarTool struct {
	Type        string `yaml:"type"`
	Value       string `yaml:"value"`
	Description string `yaml:"description"`
	Transport   string `yaml:"transport"`
	Command     string `yaml:"command"`
	URL         string `yaml:"url"`
}

// Sidecar is the parsed agents/openai.yaml. All blocks optional; a parse
// failure anywhere yields a zero-value Sidecar (never blocks the skill).
// Spec: agora-spec-skills.md §1.2.
type Sidecar struct {
	Interface struct {
		DisplayName      string `yaml:"display_name"`
		ShortDescription string `yaml:"short_description"`
		IconSmall        string `yaml:"icon_small"`
		IconLarge        string `yaml:"icon_large"`
		BrandColor       string `yaml:"brand_color"`
		DefaultPrompt    string `yaml:"default_prompt"`
	} `yaml:"interface"`
	Dependencies struct {
		Tools []SidecarTool `yaml:"tools"`
	} `yaml:"dependencies"`
	Policy struct {
		// AllowImplicitInvocation is a pointer so "absent" (default true)
		// is distinguishable from an explicit false.
		AllowImplicitInvocation *bool    `yaml:"allow_implicit_invocation"`
		Products                []string `yaml:"products"`
	} `yaml:"policy"`
}
