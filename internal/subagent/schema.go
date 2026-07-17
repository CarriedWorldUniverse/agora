package subagent

import (
	"context"
	"encoding/json"
	"fmt"
)

// MaxSchemaRetries caps forced-structured-output retries before giving up
// (spec §2: "validated, retried on mismatch") — without a cap a
// stubbornly-noncompliant child would retry forever. Attempt 0 is the first
// try; up to MaxSchemaRetries additional attempts follow, so a schema call
// makes at most MaxSchemaRetries+1 attempts total before ErrSchemaGiveUp.
const MaxSchemaRetries = 3

// minimalSchema is the small subset of JSON Schema this unit validates
// against: object type, required keys, and top-level property types. Full
// JSON Schema validation is a turn-engine/bridle concern (structured
// forcing lives at the contracts.Request.Structured layer) — this package
// only needs enough to drive and test the retry loop deterministically
// (ground rule 6: scope discipline). Ambiguity call, noted per ground rule 6.
type minimalSchema struct {
	Required   []string                  `json:"required,omitempty"`
	Properties map[string]minimalPropDef `json:"properties,omitempty"`
}

type minimalPropDef struct {
	Type string `json:"type"`
}

// validateStructured reports whether output satisfies schema. An empty
// schema always validates (no structured output was requested).
func validateStructured(schema, output json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}
	var sch minimalSchema
	if err := json.Unmarshal(schema, &sch); err != nil {
		return fmt.Errorf("subagent: parse schema: %w", err)
	}
	var val map[string]any
	if err := json.Unmarshal(output, &val); err != nil {
		return fmt.Errorf("subagent: structured output is not a JSON object: %w", err)
	}
	for _, req := range sch.Required {
		if _, ok := val[req]; !ok {
			return fmt.Errorf("subagent: missing required field %q", req)
		}
	}
	for name, def := range sch.Properties {
		v, ok := val[name]
		if !ok {
			continue
		}
		if def.Type != "" && !jsonTypeMatches(def.Type, v) {
			return fmt.Errorf("subagent: field %q: want type %q", name, def.Type)
		}
	}
	return nil
}

func jsonTypeMatches(t string, v any) bool {
	switch t {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	default:
		// An unrecognized declared type is this validator's own gap, not
		// the output's fault — don't fail-close on it.
		return true
	}
}

// runWithSchemaRetry drives one Manager spawn/continue call through
// AgentRunner, enforcing the schema-forced retry loop when req.Schema is
// set (spec §2). Returns ErrSchemaGiveUp (wrapped) if the cap is exhausted.
func runWithSchemaRetry(ctx context.Context, runner AgentRunner, req RunRequest) (RunResult, error) {
	for attempt := 0; ; attempt++ {
		req.Attempt = attempt
		res, err := runner.Run(ctx, req)
		if err != nil {
			return RunResult{}, err
		}
		if len(req.Schema) == 0 || res.Question != nil {
			// No structured output required, or the child bubbled a
			// question instead of completing (spec §2 question bubbling) —
			// either way there is nothing to validate.
			return res, nil
		}
		verr := validateStructured(req.Schema, res.Output)
		if verr == nil {
			return res, nil
		}
		if attempt >= MaxSchemaRetries {
			return RunResult{}, fmt.Errorf("%w: %v", ErrSchemaGiveUp, verr)
		}
		req.Feedback = verr.Error()
	}
}
