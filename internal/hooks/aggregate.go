package hooks

import (
	"sort"
	"strings"
)

// Outcome pairs one handler's HandlerOutcome with the ordering information
// Aggregate* functions need: Seq (declaration/discovery order, for "first
// block reason" and "declaration order" concatenation) and CompletionIndex
// (the order in which this handler's run actually finished, 0-based — only
// PreToolUse's "updatedInput from the LAST-COMPLETED handler" rule, §2.1,
// needs this; every other aggregation rule is declaration-order-based).
type Outcome struct {
	Handler         ResolvedHandler
	CompletionIndex int
	HandlerOutcome
}

func bySeq(outs []Outcome) []Outcome {
	sorted := append([]Outcome(nil), outs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Handler.Seq < sorted[j].Handler.Seq })
	return sorted
}

func joinNonEmpty(parts []string) string {
	return strings.Join(parts, "\n\n")
}

// --- §2.1 PreToolUse --------------------------------------------------

// PreToolUseAggregate is the combined result of every matched PreToolUse
// handler. Spec §2.1 aggregation bullet: "any block wins; first block
// reason; updatedInput from the LAST-completed handler; block drops any
// updatedInput." AdditionalContext concatenation is not spelled out in
// that bullet; this extends the same "flattened" idiom §2.3 states
// explicitly for PostToolUse (an inferred, not explicit, reading — see
// doc.go's file map note and ground rule 7).
type PreToolUseAggregate struct {
	Blocked           bool
	Reason            string
	UpdatedInput      []byte
	AdditionalContext string
}

func AggregatePreToolUse(outs []Outcome) PreToolUseAggregate {
	sorted := bySeq(outs)
	for _, o := range sorted {
		if o.Block {
			return PreToolUseAggregate{Blocked: true, Reason: o.Reason}
		}
	}
	var ctxParts []string
	var latest []byte
	latestCompletion := -1
	for _, o := range sorted {
		if o.AdditionalContext != "" {
			ctxParts = append(ctxParts, o.AdditionalContext)
		}
		if len(o.UpdatedInput) > 0 && o.CompletionIndex > latestCompletion {
			latest = o.UpdatedInput
			latestCompletion = o.CompletionIndex
		}
	}
	return PreToolUseAggregate{UpdatedInput: latest, AdditionalContext: joinNonEmpty(ctxParts)}
}

// --- §2.2 PermissionRequest --------------------------------------------

// PermissionRequestAggregate is the combined result of every matched
// PermissionRequest handler. Spec §2.2: "any deny wins immediately; else
// highest-precedence allow; else none." Precedence = the firing handler's
// Layer.Rank() (managed < user < project < plugin, §4.1) — the highest-rank
// allow wins when multiple handlers allow and none deny.
type PermissionRequestAggregate struct {
	Decision string // "allow" | "deny" | ""
	Message  string
}

func AggregatePermissionRequest(outs []Outcome) PermissionRequestAggregate {
	sorted := bySeq(outs)
	for _, o := range sorted {
		if o.PRBehavior == "deny" {
			return PermissionRequestAggregate{Decision: "deny", Message: o.PRMessage}
		}
	}
	bestRank := -1
	var bestMsg string
	for _, o := range sorted {
		if o.PRBehavior == "allow" {
			r := o.Handler.Source.Layer.Rank()
			if r > bestRank {
				bestRank = r
				bestMsg = o.PRMessage
			}
		}
	}
	if bestRank >= 0 {
		return PermissionRequestAggregate{Decision: "allow", Message: bestMsg}
	}
	return PermissionRequestAggregate{}
}

// --- §2.3 PostToolUse ----------------------------------------------------

// PostToolUseAggregate is the combined result of every matched PostToolUse
// handler. Spec §2.3: "any block; feedback joined \n\n; contexts flattened."
// continue:false additionally stops the turn (§2.3: "continue:false ->
// stops turn with reason") — the first non-empty StopReason in declaration
// order is used (the spec does not define a join rule for this field, so
// "first" is the simplest reading consistent with §2.1's "first block
// reason" idiom).
type PostToolUseAggregate struct {
	Blocked           bool
	Feedback          string
	AdditionalContext string
	Stopped           bool
	StopReason        string
}

func AggregatePostToolUse(outs []Outcome) PostToolUseAggregate {
	sorted := bySeq(outs)
	var feedback, ctxParts []string
	stopped := false
	var stopReason string
	for _, o := range sorted {
		if o.Block && o.Reason != "" {
			feedback = append(feedback, o.Reason)
		}
		if o.AdditionalContext != "" {
			ctxParts = append(ctxParts, o.AdditionalContext)
		}
		if !o.Continue {
			stopped = true
			if stopReason == "" {
				stopReason = o.StopReason
			}
		}
	}
	return PostToolUseAggregate{
		Blocked:           len(feedback) > 0,
		Feedback:          joinNonEmpty(feedback),
		AdditionalContext: joinNonEmpty(ctxParts),
		Stopped:           stopped,
		StopReason:        stopReason,
	}
}

// --- §2.4/§2.5 PreCompact / PostCompact -----------------------------------

// CompactAggregate is the combined result of every matched Pre/PostCompact
// handler: universal fields only (§2.4/§2.5). continue:false from any
// handler aborts/halts compaction.
type CompactAggregate struct {
	Halted bool
}

func AggregateCompact(outs []Outcome) CompactAggregate {
	for _, o := range bySeq(outs) {
		if !o.Continue {
			return CompactAggregate{Halted: true}
		}
	}
	return CompactAggregate{}
}

// --- §2.6/§2.7/§2.8 context-injecting events ------------------------------

// ContextAggregate is the combined result for SessionStart, UserPromptSubmit,
// and SubagentStart: additionalContext concatenated in declaration order,
// plus (where the event honors it) a stop. Spec doesn't give an explicit
// join rule for these events' multi-handler context concatenation either;
// same "flattened" idiom applied consistently (see PreToolUseAggregate's
// doc comment).
type ContextAggregate struct {
	AdditionalContext string
	Stopped           bool // only meaningful for SessionStart/UserPromptSubmit
	StopReason        string
	// Blocked: UserPromptSubmit's decision:block, or any event's exit-2
	// (uniform convention, §2 top) — SessionStart/SubagentStart don't
	// define their OWN block semantics beyond that, so a blocked
	// SessionStart/SubagentStart surfaces here for the caller to decide
	// what "deny session start" means operationally.
	Blocked bool
	Reason  string
}

func AggregateContext(outs []Outcome) ContextAggregate {
	sorted := bySeq(outs)
	var ctxParts []string
	stopped := false
	var stopReason, blockReason string
	blocked := false
	for _, o := range sorted {
		if o.AdditionalContext != "" {
			ctxParts = append(ctxParts, o.AdditionalContext)
		}
		if o.Block && !blocked {
			blocked = true
			blockReason = o.Reason
		}
		if !o.Continue {
			stopped = true
			if stopReason == "" {
				stopReason = o.StopReason
			}
		}
	}
	return ContextAggregate{
		AdditionalContext: joinNonEmpty(ctxParts),
		Stopped:           stopped,
		StopReason:        stopReason,
		Blocked:           blocked,
		Reason:            blockReason,
	}
}

// --- §2.9/§2.10 SubagentStop / Stop ----------------------------------------

// StopAggregate is the combined result of every matched Stop/SubagentStop
// handler. Spec §2.9/§2.10: "any stop wins; else any block, reasons joined
// \n\n, continuation fragments concatenated in declaration order." A "stop"
// here is continue:false, which OVERRIDES a block outright.
type StopAggregate struct {
	// Stopped: continue:false from any handler -> end the turn (overrides Looped).
	Stopped bool
	// Looped: at least one handler blocked (and no handler stopped) -> the
	// turn continues with a continuation prompt, stop_hook_active=true.
	Looped       bool
	Continuation string
}

func AggregateStop(outs []Outcome) StopAggregate {
	sorted := bySeq(outs)
	for _, o := range sorted {
		if !o.Continue {
			return StopAggregate{Stopped: true}
		}
	}
	var frags []string
	for _, o := range sorted {
		if o.Block && o.Reason != "" {
			frags = append(frags, o.Reason)
		}
	}
	if len(frags) == 0 {
		return StopAggregate{}
	}
	return StopAggregate{Looped: true, Continuation: joinNonEmpty(frags)}
}
