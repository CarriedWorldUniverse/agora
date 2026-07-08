// Package normalize provides helpers for mapping provider-specific wire
// values to bridle's canonical StopReason values.
package normalize

import backend "github.com/CarriedWorldUniverse/agora/internal/backend"

// ClaudeStopReason maps Claude API stop_reason strings to bridle StopReason values.
func ClaudeStopReason(raw string) backend.StopReason {
	switch raw {
	case "end_turn":
		return backend.StopReasonModelDone
	case "max_tokens":
		return backend.StopReasonMaxSteps
	case "tool_use":
		// tool_use is not terminal in bridle; the caller manages the loop.
		return backend.StopReasonModelDone
	default:
		return backend.StopReasonModelDone
	}
}

// OpenAIStopReason maps OpenAI finish_reason strings to bridle StopReason values.
func OpenAIStopReason(raw string) backend.StopReason {
	switch raw {
	case "stop":
		return backend.StopReasonModelDone
	case "length":
		return backend.StopReasonMaxSteps
	case "tool_calls", "function_call":
		return backend.StopReasonModelDone
	default:
		return backend.StopReasonModelDone
	}
}

// GeminiStopReason maps Gemini FinishReason values to bridle StopReason values.
func GeminiStopReason(raw string) backend.StopReason {
	switch raw {
	case "STOP", "FINISH_REASON_STOP":
		return backend.StopReasonModelDone
	case "MAX_TOKENS":
		return backend.StopReasonMaxSteps
	default:
		return backend.StopReasonModelDone
	}
}

// BedrockStopReason maps AWS Bedrock Converse stop_reason strings to bridle StopReason values.
func BedrockStopReason(raw string) backend.StopReason {
	switch raw {
	case "end_turn", "stop_sequence":
		return backend.StopReasonModelDone
	case "max_tokens":
		return backend.StopReasonMaxSteps
	case "tool_use":
		// non-terminal in bridle — caller manages the tool loop
		return backend.StopReasonModelDone
	case "guardrail_intervened", "content_filtered":
		return backend.StopReasonError
	default:
		return backend.StopReasonModelDone
	}
}

// OllamaStopReason maps Ollama done_reason strings to bridle StopReason values.
func OllamaStopReason(raw string) backend.StopReason {
	switch raw {
	case "stop":
		return backend.StopReasonModelDone
	case "length":
		return backend.StopReasonMaxSteps
	default:
		return backend.StopReasonModelDone
	}
}
