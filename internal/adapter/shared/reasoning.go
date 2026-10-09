package shared

import (
	"encoding/json"
	"strings"
)

// CCResponseMessage extends the response wire shape without changing request decoding.
type CCResponseMessage struct {
	CCMessage
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Refusal          string `json:"refusal,omitempty"`
}

// ReasoningFields accepts CPA's preferred reasoning_content and provider aliases.
// The first field with readable text wins; encrypted metadata is never text.
type ReasoningFields struct {
	ReasoningContent json.RawMessage `json:"reasoning_content,omitempty"`
	Reasoning        json.RawMessage `json:"reasoning,omitempty"`
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
}

func (r ReasoningFields) Text() string {
	for _, raw := range []json.RawMessage{r.ReasoningContent, r.Reasoning, r.ReasoningDetails} {
		var node any
		if json.Unmarshal(raw, &node) == nil {
			if text := reasoningText(node); text != "" {
				return text
			}
		}
	}
	return ""
}

func reasoningText(node any) string {
	switch v := node.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, part := range v {
			b.WriteString(reasoningText(part))
		}
		return b.String()
	case map[string]any:
		if text, ok := v["text"].(string); ok {
			return text
		}
	}
	return ""
}

// ReasoningItem carries visible reasoning as a Responses summary without
// fabricating encrypted_content or a provider signature.
func ReasoningItem(id, text string) RespItem {
	return RespItem{Type: "reasoning", ID: id, Summary: []ReasoningSummary{{Type: "summary_text", Text: text}}}
}

type ReasoningSummary struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text"`
}

func (a *OutputAssembler) AppendReasoning(id, text string) {
	a.items = append(a.items, ReasoningItem(id, text))
}

func (e ResponsesEventEmitter) ReasoningAdded(id string, index int) [][]byte {
	return [][]byte{
		e.ItemAdded(index, map[string]any{"type": "reasoning", "id": id, "summary": []any{}}),
		SSEEvent("response.reasoning_summary_part.added", map[string]any{"type": "response.reasoning_summary_part.added", "item_id": id, "output_index": index, "summary_index": 0, "part": ReasoningSummary{Type: "summary_text"}}),
	}
}

func (e ResponsesEventEmitter) ReasoningDelta(id string, index int, text string) []byte {
	return SSEEvent("response.reasoning_summary_text.delta", map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": id, "output_index": index, "summary_index": 0, "delta": text})
}

func (e ResponsesEventEmitter) ReasoningDone(item RespItem, index int) [][]byte {
	text := ""
	for _, p := range item.Summary {
		text += p.Text
	}
	return [][]byte{
		SSEEvent("response.reasoning_summary_text.done", map[string]any{"type": "response.reasoning_summary_text.done", "item_id": item.ID, "output_index": index, "summary_index": 0, "text": text}),
		SSEEvent("response.reasoning_summary_part.done", map[string]any{"type": "response.reasoning_summary_part.done", "item_id": item.ID, "output_index": index, "summary_index": 0, "part": ReasoningSummary{Type: "summary_text", Text: text}}),
		e.ItemDone(index, item),
	}
}
