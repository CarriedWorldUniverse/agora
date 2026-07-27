package ctxmgr

import "encoding/json"

// ToolCallPayload / ToolResultPayload are the shape ctxmgr expects under
// contracts.ThreadItem.Payload for TIToolCall/TIToolResult items.
//
// Ambiguity note (spec-consistent reading, not invented): persistence
// (internal/persistence) leaves ThreadItem.Payload as `any` with no fixed
// tool-item schema — that normalization is a turn-engine-and-bridle
// concern (the tool_call/tool_result shapes bridle funnels per provider)
// that hasn't landed yet. ctxmgr needs SOME shape to read (tool name, key
// arg, content bytes) to run the curation algorithm at all, so this file
// defines the minimal one and documents it as the seam: whichever unit
// normalizes real provider tool items into ThreadItem.Payload should
// target this shape (or ctxmgr's decode helpers gain a second case).
type ToolCallPayload struct {
	ToolName string          `json:"tool_name"`
	ID       string          `json:"id"`
	Args     json.RawMessage `json:"args_json"`
}

// ToolResultPayload is a completed tool result.
type ToolResultPayload struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// decodePayload re-marshals item.Payload (which may already be the target
// type, a map[string]any from JSONL decode, or json.RawMessage) into out.
// Returns false if it isn't shaped like the target at all.
func decodePayload(payload any, out any) bool {
	switch p := payload.(type) {
	case nil:
		return false
	case ToolCallPayload:
		tc, ok := out.(*ToolCallPayload)
		if !ok {
			return false
		}
		*tc = p
		return true
	case ToolResultPayload:
		tr, ok := out.(*ToolResultPayload)
		if !ok {
			return false
		}
		*tr = p
		return true
	default:
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if err := json.Unmarshal(b, out); err != nil {
			return false
		}
		return true
	}
}

// argString extracts a string field named key from a raw JSON args object
// ("" if absent/not-a-string).
// argStringAny returns the first of keys present in args, in order — the
// fallback that lets one mapping accept either spelling of an argument.
func argStringAny(args json.RawMessage, keys []string) string {
	for _, k := range keys {
		if v := argString(args, k); v != "" {
			return v
		}
	}
	return ""
}

func argString(args json.RawMessage, key string) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
