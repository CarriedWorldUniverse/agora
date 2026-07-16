package approval

import "encoding/json"

// AuditLine is the structured record of a single Decide outcome —
// invariant 3 (§4): "every decision is recorded with its stage + actor".
// Field order is fixed (a struct, never a map), so MarshalJSONLine is
// byte-stable for a stable Result: no map-iteration nondeterminism.
type AuditLine struct {
	RequestID string `json:"request_id"`
	Kind      string `json:"kind"`
	Action    Action `json:"action"`
	Scope     string `json:"scope,omitempty"`
	Stage     string `json:"stage"`
	By        string `json:"by"`
	Message   string `json:"message,omitempty"`
}

// NewAuditLine builds the audit record for a resolved Result, correlated to
// requestID (the wire ApprovalRequest.ID / Request.ID).
func NewAuditLine(requestID string, r Result) AuditLine {
	return AuditLine{
		RequestID: requestID,
		Kind:      string(r.Kind),
		Action:    r.Action,
		Scope:     string(r.Scope),
		Stage:     string(r.Stage),
		By:        r.By,
		Message:   r.Message,
	}
}

// MarshalJSONLine renders the audit line as a single deterministic JSON
// line (no trailing newline; callers append their own record separator to
// match the JSONL convention used elsewhere in the harness — see
// internal/persistence).
func (a AuditLine) MarshalJSONLine() (string, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
