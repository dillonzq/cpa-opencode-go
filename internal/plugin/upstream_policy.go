package plugin

import (
	"encoding/json"
	"strings"

	translator "github.com/dillonzq/api-translator"
	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
	"github.com/dillonzq/cpa-opencode-go/internal/protocol"
)

// normalizeNativeMessages prepares a native Messages request,
// folding any in-history role:"system" turns into the top-level system
// field (Anthropic rejects system roles inside messages). Text joins with
// blank lines; a string system stays a string, a block-array system gains
// a text block, an absent system becomes a string. Non-text system content
// is rejected descriptively, never dropped. Without system turns it is a
// no-op; the library rewrites model afterward.
func normalizeNativeMessages(sourceBody []byte) ([]byte, *errclass.Error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(sourceBody, &req); err != nil {
		return nil, errclass.Translation("malformed claude request JSON: " + err.Error())
	}
	if req == nil {
		return nil, errclass.Translation("malformed request body: JSON null is not a valid request")
	}
	rawMsgs, ok := req["messages"]
	if !ok || !hasContent(rawMsgs) {
		return sourceBody, nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(rawMsgs, &arr); err != nil {
		return nil, errclass.Translation("malformed claude request JSON: " + err.Error())
	}
	hasSystem := false
	for _, raw := range arr {
		// Malformed turns fail explicitly rather than passing through.
		var w struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, errclass.Translation("malformed claude request JSON: " + err.Error())
		}
		if w.Role == "system" {
			hasSystem = true
			break
		}
	}
	if !hasSystem {
		return sourceBody, nil
	}
	var folded []string
	kept := make([]json.RawMessage, 0, len(arr))
	for _, raw := range arr {
		var wire struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, errclass.Translation("malformed claude request JSON: " + err.Error())
		}
		if wire.Role != "system" {
			kept = append(kept, raw)
			continue
		}
		text, eErr := systemTurnText(wire.Content)
		if eErr != nil {
			return nil, eErr
		}
		if text != "" {
			folded = append(folded, text)
		}
	}
	if len(folded) > 0 {
		if eErr := foldSystem(req, folded); eErr != nil {
			return nil, eErr
		}
	}
	msgs, err := json.Marshal(kept)
	if err != nil {
		return nil, errclass.Translation("model id cannot be represented as JSON")
	}
	req["messages"] = msgs

	out, err := json.Marshal(req)
	if err != nil {
		return nil, errclass.Translation("model id cannot be represented as JSON")
	}
	return out, nil
}

// systemTurnText flattens one in-history system turn's content to text.
// String content passes through; text blocks join with blank lines;
// thinking blocks are omitted; images and unknown types are rejected
// descriptively rather than dropped.
func systemTurnText(raw json.RawMessage) (string, *errclass.Error) {
	if !hasContent(raw) {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return "", errclass.Translation("message content must be a string or an array of blocks")
	}
	var b strings.Builder
	for _, elem := range elems {
		var blk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(elem, &blk); err != nil {
			return "", errclass.Translation("malformed content block")
		}
		switch blk.Type {
		case "text":
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(blk.Text)
		case "image":
			return "", errclass.Translation("system messages cannot carry image content")
		case "thinking", "redacted_thinking":
		default:
			return "", &errclass.Error{Class: errclass.ClassUnsupported, Message: "unsupported content block type " + blk.Type + " for /v1/messages"}
		}
	}
	return b.String(), nil
}

// foldSystem merges folded in-history system texts into the envelope's
// system field: strings append with blank-line separators, block arrays
// gain one text block, absent systems become a string.
func foldSystem(req map[string]json.RawMessage, folded []string) *errclass.Error {
	joined := strings.Join(folded, "\n\n")
	rawSys, ok := req["system"]
	if !ok || !hasContent(rawSys) {
		b, _ := json.Marshal(joined) // string always marshals
		req["system"] = b
		return nil
	}
	var s string
	if err := json.Unmarshal(rawSys, &s); err == nil {
		if s != "" {
			joined = s + "\n\n" + joined
		}
		b, _ := json.Marshal(joined) // string always marshals
		req["system"] = b
		return nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(rawSys, &blocks); err != nil {
		return errclass.Translation("malformed system field")
	}
	b, _ := json.Marshal(map[string]string{"type": "text", "text": joined}) // marshallable composed type; cannot fail
	blocks = append(blocks, b)
	merged, err := json.Marshal(blocks)
	if err != nil {
		return errclass.Translation("model id cannot be represented as JSON")
	}
	req["system"] = merged
	return nil
}

// normalizeNativeRequest contains OpenCode Go compatibility rules, separate
// from the library's protocol conversion and same-protocol passthrough.
func normalizeNativeRequest(route catalog.Route, model, source string, body []byte, state *translator.RequestState) ([]byte, *errclass.Error) {
	switch {
	case route == catalog.RouteChatCompletions && source == "openai":
		return normalizeNativeChat(body)
	case route == catalog.RouteMessages && source == "claude":
		return normalizeNativeMessages(body)
	case route == catalog.RouteResponses && source == "openai-response" && !strings.HasPrefix(strings.ToLower(model), "gpt"):
		return normalizeNativeResponses(body, state)
	default:
		return body, nil
	}
}

func hasContent(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

func normalizeNativeChat(body []byte) ([]byte, *errclass.Error) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil || doc == nil {
		return nil, errclass.Translation("malformed openai request JSON")
	}
	touched := false
	if raw, ok := doc["thinking"]; ok {
		var thinking map[string]json.RawMessage
		var typ string
		if json.Unmarshal(raw, &thinking) != nil || json.Unmarshal(thinking["type"], &typ) != nil || strings.TrimSpace(typ) == "" {
			delete(doc, "thinking")
			touched = true
		}
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(doc["messages"], &messages) == nil {
		changed := false
		for _, message := range messages {
			if sessionString(message["role"]) == "developer" {
				message["role"] = json.RawMessage(`"system"`)
				changed = true
			}
		}
		if changed {
			doc["messages"], _ = json.Marshal(messages)
			touched = true
		}
	}
	if !touched {
		return body, nil
	}
	out, _ := json.Marshal(doc)
	return out, nil
}

func normalizeNativeResponses(body []byte, state *translator.RequestState) ([]byte, *errclass.Error) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil || doc == nil {
		return nil, errclass.Translation("malformed Responses request JSON")
	}
	if raw, ok := doc["tools"]; ok && hasContent(raw) {
		tools, err := filterNativeTools(raw)
		if err != nil {
			return nil, err
		}
		doc["tools"] = tools
	}
	if raw := doc["input"]; hasContent(raw) {
		var items []map[string]json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			kept := make([]map[string]json.RawMessage, 0, len(items))
			for _, item := range items {
				if item == nil {
					return nil, errclass.Translation("input must contain objects")
				}
				switch sessionString(item["type"]) {
				case "reasoning", "compaction":
					continue
				case "additional_tools":
					if !hasContent(item["tools"]) {
						return nil, errclass.Translation("additional_tools requires a tools array")
					}
					tools, err := filterNativeTools(item["tools"])
					if err != nil {
						return nil, err
					}
					item["tools"] = tools
				case "function_call":
					args := sessionString(item["arguments"])
					if strings.TrimSpace(args) == "" || strings.TrimSpace(args) == "null" {
						item["arguments"] = json.RawMessage(`"{}"`)
					}
				}
				kept = append(kept, item)
			}
			doc["input"], _ = json.Marshal(kept)
		}
	}
	prepared, _ := json.Marshal(doc)
	out, err := translator.NormalizeResponsesRequest(prepared, state)
	return out, protocol.Error(err)
}

// Only native non-GPT Responses uses the legacy unsupported-tool filter.
// Cross-protocol tools are handled by api-translator, including apply_patch.
func filterNativeTools(raw json.RawMessage) (json.RawMessage, *errclass.Error) {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || tools == nil {
		return nil, errclass.Translation("tools must be an array of objects")
	}
	kept := make([]map[string]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			return nil, errclass.Translation("tools must contain objects")
		}
		typ := sessionString(tool["type"])
		if typ == "tool_search" || typ == "image_generation" || typ == "custom" && sessionString(tool["name"]) == "apply_patch" {
			continue
		}
		if typ == "namespace" {
			children, err := filterNativeTools(tool["tools"])
			if err != nil {
				return nil, err
			}
			tool["tools"] = children
		}
		if typ == "web_search" {
			tool = map[string]json.RawMessage{"type": json.RawMessage(`"web_search"`)}
		}
		kept = append(kept, tool)
	}
	out, _ := json.Marshal(kept)
	return out, nil
}
