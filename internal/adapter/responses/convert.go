// Non-stream response conversion for the Responses adapter (FR-006,
// AC §D): an upstream /v1/responses HTTP result is translated back into
// the client protocol — validated passthrough for openai-response clients,
// Chat Completions / Messages synthesis otherwise.
package responses

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dillonzq/cpa-opencode-go/internal/adapter/shared"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
)

// ---- upstream Responses result shapes (decode-only) ----

type respTextPart struct {
	Type    string `json:"type"` // output_text | ...
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type respOutputItem struct {
	Summary   []respTextPart `json:"summary"`
	Type      string         `json:"type"` // message | function_call | ...
	CallID    string         `json:"call_id"`
	Name      string         `json:"name"`
	Arguments string         `json:"arguments"`
	Content   []respTextPart `json:"content"`
}

type respUsageIn struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	InputDetails *struct {
		CachedTokens     *int64 `json:"cached_tokens"`
		CacheWriteTokens *int64 `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type respResultIn struct {
	ID     string           `json:"id"`
	Model  string           `json:"model"`
	Status string           `json:"status"`
	Output []respOutputItem `json:"output"`
	Usage  respUsageIn      `json:"usage"`
}

// ConvertNonStreamResponse translates an upstream /v1/responses HTTP
// result back into the client protocol sourceFormat (FR-006, AC §D).
// Statuses >= 400 become classified redacted errors (§7); "openai-response"
// passes through; "openai" and "claude" are converted, preserving text,
// function calls, terminal status, and usage. Unknown formats are
// ClassUnsupported; malformed upstream bodies are ClassTranslation.
func ConvertNonStreamResponse(sourceFormat string, status int, upstreamBody []byte, tools ...*shared.ResponseTools) ([]byte, *errclass.Error) {
	if status >= 400 {
		return nil, shared.UpstreamStatusError(status, upstreamBody)
	}
	switch sourceFormat {
	case "openai-response":
		if !json.Valid(upstreamBody) {
			return nil, errclass.Translation("malformed Responses response JSON")
		}
		return restoreCustomPassthrough(upstreamBody, shared.ResponseToolContext(tools)), nil
	case "openai":
		return responsesToChat(upstreamBody)
	case "claude":
		return responsesToClaude(upstreamBody)
	default:
		return nil, shared.UnsupportedFormat(sourceFormat, EndpointPath)
	}
}

// restoreCustomPassthrough rewrites function_call output items for custom
// tools into their custom_tool_call shape on the native passthrough path;
// bodies without output arrays or custom tools pass through untouched.
func restoreCustomPassthrough(body []byte, rt *shared.ResponseTools) []byte {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	raw, ok := doc["output"].([]any)
	if !ok {
		return body
	}
	changed := false
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok || item["type"] != "function_call" {
			continue
		}
		name, _ := item["name"].(string)
		if !rt.IsCustom(name) {
			continue
		}
		args, _ := item["arguments"].(string)
		item["type"] = "custom_tool_call"
		item["input"] = shared.UnwrapCustomToolInput(args)
		delete(item, "arguments")
		changed = true
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// decodeResp validates and decodes an upstream non-stream Responses body;
// a missing output array is a translation failure, never silently empty
// (FR-006).
func decodeResp(body []byte) (*respResultIn, *errclass.Error) {
	var resp respResultIn
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, errclass.Translation("malformed Responses response JSON: " + err.Error())
	}
	if resp.Output == nil {
		return nil, errclass.Translation("Responses result carries no output items")
	}
	return &resp, nil
}

// ccFinishFromStatus derives a Chat Completions finish reason from the
// result status and emitted items via the shared precedence rule: any
// function_call means tool_calls (incomplete cannot downgrade it);
// otherwise incomplete maps to length, else stop.
func ccFinishFromStatus(status string, sawToolCall bool) string {
	statusFinish := shared.CCFinishFromResponseStatus(status)
	return shared.TerminalReason(sawToolCall, "tool_calls", statusFinish)
}

// respItemCarriesPayload reports whether an unrecognized output item
// carries real content the target protocol cannot represent; purely
// informational items are omittable per the compatibility policy.
func respItemCarriesPayload(item respOutputItem) bool {
	return len(item.Content) > 0 || item.Name != "" || item.CallID != "" ||
		strings.TrimSpace(item.Arguments) != ""
}

// responsesToChat converts a Responses result into a Chat Completions
// response: output_text parts concatenate into message.content,
// function_call items become indexed tool_calls with their arguments
// verbatim, status maps to finish_reason, and input/output tokens map to
// prompt/completion usage (FR-006, AC §D). Visible reasoning maps to
// reasoning_content. Unsupported items carrying payload fail descriptively.
func responsesToChat(body []byte) ([]byte, *errclass.Error) {
	resp, eErr := decodeResp(body)
	if eErr != nil {
		return nil, eErr
	}
	msg := shared.CCResponseMessage{CCMessage: shared.CCMessage{Role: "assistant"}}
	var text strings.Builder
	sawToolCall := false
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				} else if part.Type == "refusal" {
					msg.Refusal += part.Refusal
				}
			}
		case "function_call":
			tc := shared.CCToolCall{ID: item.CallID, Type: "function"}
			tc.Function.Name = item.Name
			tc.Function.Arguments = shared.DefaultArgs(item.Arguments)
			msg.ToolCalls = append(msg.ToolCalls, tc)
			sawToolCall = true
		case "reasoning":
			msg.ReasoningContent += item.reasoningText()
		default:
			if respItemCarriesPayload(item) {
				return nil, errclass.Translation(fmt.Sprintf(
					"%s output cannot represent %q output items", EndpointPath, item.Type))
			}
		}
	}
	if t := text.String(); t != "" {
		raw, _ := json.Marshal(t)
		msg.Content = raw
	}
	// Shared kernel computes total_tokens as prompt+completion so the
	// majority rule cannot diverge from the sibling mappers.
	out := shared.CompletionEnvelope(resp.ID, resp.Model, time.Now().Unix(),
		[]map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": shared.FinishWithRefusal(ccFinishFromStatus(resp.Status, sawToolCall), msg.Refusal != ""),
		}},
		shared.CCUsageFrom(resp.Usage.InputTokens, resp.Usage.OutputTokens, shared.UsageDetails{
			CachedTokens: func() *int64 {
				if resp.Usage.InputDetails != nil {
					return resp.Usage.InputDetails.CachedTokens
				}
				return nil
			}(),
			CacheWriteTokens: func() *int64 {
				if resp.Usage.InputDetails != nil {
					return resp.Usage.InputDetails.CacheWriteTokens
				}
				return nil
			}(),
			ReasoningTokens: func() *int64 {
				if resp.Usage.OutputDetails != nil {
					return resp.Usage.OutputDetails.ReasoningTokens
				}
				return nil
			}(),
		}))
	b, _ := json.Marshal(out) // composed marshallable types only; cannot fail
	return b, nil
}

// responsesToClaude converts a Responses result into a Messages response
// via the shared Claude-result kernel: output_text parts aggregate into
// text blocks (empty parts dropped, matching the Chat Completions
// route's block builder), function_call items become tool_use blocks with
// arguments decoded as input, completed status maps to stop_reason
// end_turn (tool_use when tools were called, max_tokens when incomplete),
// and input/output tokens map to input/output usage (FR-006). Visible
// reasoning maps to thinking blocks without fabricated signatures.
// Unsupported items carrying payload fail descriptively.
func responsesToClaude(body []byte) ([]byte, *errclass.Error) {
	resp, eErr := decodeResp(body)
	if eErr != nil {
		return nil, eErr
	}
	sawToolCall := false
	refused := false
	blocks := make([]map[string]any, 0, len(resp.Output))
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				// Uniform DROP policy (route parity): empty output_text
				// parts ship no dead text block.
				if part.Type == "refusal" {
					refused = true
					if part.Refusal != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": part.Refusal})
					}
				}
				if part.Type == "output_text" && part.Text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
				}
			}
		case "function_call":
			input, eErr := shared.DecodeArgs(item.Arguments)
			if eErr != nil {
				return nil, eErr
			}
			blocks = append(blocks, map[string]any{
				"type": "tool_use", "id": item.CallID, "name": item.Name, "input": input,
			})
			sawToolCall = true
		case "reasoning":
			if text := item.reasoningText(); text != "" {
				blocks = append(blocks, map[string]any{"type": "thinking", "thinking": text})
			}
		default:
			if respItemCarriesPayload(item) {
				return nil, errclass.Translation(fmt.Sprintf(
					"%s output cannot represent %q output items", EndpointPath, item.Type))
			}
		}
	}
	statusStop := shared.FinishToClaudeStop(shared.FinishWithRefusal(shared.CCFinishFromResponseStatus(resp.Status), refused))
	stop := shared.TerminalReason(sawToolCall, "tool_use", statusStop)
	var cacheRead, cacheWrite *int64
	if resp.Usage.InputDetails != nil {
		cacheRead = resp.Usage.InputDetails.CachedTokens
		cacheWrite = resp.Usage.InputDetails.CacheWriteTokens
	}
	b, _ := json.Marshal(shared.NewClaudeResult(resp.ID, resp.Model, stop,
		shared.ClampSubtract(resp.Usage.InputTokens, cacheRead, cacheWrite), resp.Usage.OutputTokens,
		cacheRead, cacheWrite, blocks))
	return b, nil // composed marshallable types only; cannot fail
}

// reasoningText retains both summaries and visible reasoning content (CPA
// also accepts reasoning_text content emitted by compatible providers).
func (item respOutputItem) reasoningText() string {
	var b strings.Builder
	for _, part := range item.Summary {
		if part.Type == "summary_text" {
			b.WriteString(part.Text)
		}
	}
	for _, part := range item.Content {
		if part.Type == "reasoning_text" || part.Type == "text" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}
