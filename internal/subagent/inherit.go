package subagent

import "github.com/CarriedWorldUniverse/agora/contracts"

// ParentContext is what a spawning thread contributes to a child by
// inheritance (spec §2: "child gets parent's cwd, approval policy,
// permission profile, and tool set (minus def's allowlist); model/effort
// inherited unless overridden").
type ParentContext struct {
	Cwd    string
	Policy contracts.PolicySet
	Model  string
	Effort contracts.Effort
	// Tools is the parent's own effective tool set. Nil means "unrestricted"
	// (no tool-set narrowing to apply — the def's allowlist, if any, is used
	// as-is).
	Tools []string
}

// EffectiveSpawn is the resolved configuration a Manager.Spawn call actually
// runs with, after folding SpawnOpts overrides over the agent def over
// parent inheritance.
type EffectiveSpawn struct {
	Cwd    string
	Policy contracts.PolicySet
	Model  string
	Effort contracts.Effort
	Tools  []string
}

// ResolveInheritance computes the effective spawn configuration per spec §2:
//   - cwd, approval policy, permission profile: inherited from the parent,
//     unconditionally (subagents cannot widen approval — approvals §4
//     invariant 4: "Subagents inherit the parent's effective policy set").
//   - model/effort: SpawnOpts override > agent def override > parent
//     inherited.
//   - tools: parent's tool set narrowed to the def's allowlist. Ambiguity
//     call (spec §2 phrasing "tool set (minus def's allowlist)" is
//     terse): read as "the def's allowlist restricts the parent's set",
//     i.e. effective tools = intersection(parent.Tools, def.Tools),
//     preserving def.Tools order — a def cannot GRANT a tool the parent
//     didn't have, only narrow further. def.Tools == nil ("omit = all
//     tools", spec §1) means no narrowing: effective = parent.Tools
//     unchanged. A nil parent.Tools (parent itself unrestricted) with a
//     non-nil def.Tools yields def.Tools verbatim (nothing to intersect
//     against).
func ResolveInheritance(parent ParentContext, def *AgentDef, opts SpawnOpts) EffectiveSpawn {
	eff := EffectiveSpawn{
		Cwd:    parent.Cwd,
		Policy: parent.Policy,
		Model:  parent.Model,
		Effort: parent.Effort,
		Tools:  parent.Tools,
	}

	if def != nil && def.Model != "" {
		eff.Model = def.Model
	}
	if opts.Model != "" {
		eff.Model = opts.Model
	}

	if def != nil && def.Effort != "" {
		eff.Effort = contracts.Effort(def.Effort)
	}
	if opts.Effort != "" {
		eff.Effort = opts.Effort
	}

	if def != nil && def.Tools != nil {
		if parent.Tools == nil {
			eff.Tools = def.Tools
		} else {
			eff.Tools = intersectPreserveOrder(parent.Tools, def.Tools)
		}
	}

	return eff
}

func intersectPreserveOrder(have, allow []string) []string {
	haveSet := make(map[string]bool, len(have))
	for _, t := range have {
		haveSet[t] = true
	}
	var out []string
	for _, t := range allow {
		if haveSet[t] {
			out = append(out, t)
		}
	}
	return out
}
