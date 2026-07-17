package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// countingRunner returns badOutput for the first failAttempts calls, then
// goodOutput forever after. Deterministic, no model calls (ground rule 6).
type countingRunner struct {
	failAttempts int
	badOutput    json.RawMessage
	goodOutput   json.RawMessage
	calls        int
	feedbacks    []string
}

func (r *countingRunner) Run(_ context.Context, req RunRequest) (RunResult, error) {
	r.calls++
	r.feedbacks = append(r.feedbacks, req.Feedback)
	if req.Attempt < r.failAttempts {
		return RunResult{Output: r.badOutput}, nil
	}
	return RunResult{Output: r.goodOutput}, nil
}

var schemaWithRequired = json.RawMessage(`{"required":["answer"],"properties":{"answer":{"type":"string"}}}`)

func TestSchemaRetry_SucceedsAfterNAttempts(t *testing.T) {
	r := &countingRunner{
		failAttempts: 2,
		badOutput:    json.RawMessage(`{"nope": true}`),
		goodOutput:   json.RawMessage(`{"answer": "42"}`),
	}
	res, err := runWithSchemaRetry(context.Background(), r, RunRequest{Schema: schemaWithRequired})
	if err != nil {
		t.Fatalf("runWithSchemaRetry: %v", err)
	}
	if string(res.Output) != string(r.goodOutput) {
		t.Errorf("Output = %s, want %s", res.Output, r.goodOutput)
	}
	if r.calls != 3 {
		t.Errorf("calls = %d, want 3 (2 failures + 1 success)", r.calls)
	}
	// Feedback must be empty on the first attempt, non-empty on retries.
	if r.feedbacks[0] != "" {
		t.Errorf("feedbacks[0] = %q, want empty on first attempt", r.feedbacks[0])
	}
	if r.feedbacks[1] == "" {
		t.Error("feedbacks[1] is empty, want the mismatch reason from attempt 0")
	}
}

func TestSchemaRetry_GivesUpAfterCap(t *testing.T) {
	r := &countingRunner{
		failAttempts: 1000, // never converges
		badOutput:    json.RawMessage(`{"nope": true}`),
		goodOutput:   json.RawMessage(`{"answer": "unreachable"}`),
	}
	_, err := runWithSchemaRetry(context.Background(), r, RunRequest{Schema: schemaWithRequired})
	if !errors.Is(err, ErrSchemaGiveUp) {
		t.Fatalf("err = %v, want ErrSchemaGiveUp", err)
	}
	if r.calls != MaxSchemaRetries+1 {
		t.Errorf("calls = %d, want %d (MaxSchemaRetries+1 attempts)", r.calls, MaxSchemaRetries+1)
	}
}

func TestSchemaRetry_NoSchemaSkipsValidation(t *testing.T) {
	r := &countingRunner{goodOutput: json.RawMessage(`not even json`)}
	res, err := runWithSchemaRetry(context.Background(), r, RunRequest{})
	if err != nil {
		t.Fatalf("runWithSchemaRetry: %v", err)
	}
	if string(res.Output) != "not even json" {
		t.Errorf("Output = %s", res.Output)
	}
	if r.calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry loop without a schema)", r.calls)
	}
}

func TestValidateStructured(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		output  string
		wantErr bool
	}{
		{"missing required", `{"required":["x"]}`, `{}`, true},
		{"required present", `{"required":["x"]}`, `{"x":1}`, false},
		{"wrong type", `{"properties":{"x":{"type":"string"}}}`, `{"x":1}`, true},
		{"right type", `{"properties":{"x":{"type":"string"}}}`, `{"x":"s"}`, false},
		{"not an object", `{"required":["x"]}`, `[1,2,3]`, true},
		{"empty schema always valid", ``, `anything at all`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateStructured(json.RawMessage(c.schema), json.RawMessage(c.output))
			if (err != nil) != c.wantErr {
				t.Errorf("validateStructured(%q, %q) err = %v, wantErr %v", c.schema, c.output, err, c.wantErr)
			}
		})
	}
}
