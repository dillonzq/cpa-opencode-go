package plugin

import (
	"encoding/json"
	"strings"

	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
)

// OpenCode's DeepSeek gateway can reject original even though DeepSeek's
// direct API supports it. DeepSeek documents high as equivalent to original.
// Apply this after translation so both native and converted requests use the
// upstream wire shape. Inspect only content parts, never tool schemas or args.
func normalizeUpstreamImageDetail(route catalog.Route, model string, body []byte) []byte {
	if !strings.HasPrefix(strings.ToLower(model), "deepseek") {
		return body
	}
	field := "messages"
	switch route {
	case catalog.RouteChatCompletions:
	case catalog.RouteResponses:
		field = "input"
	default:
		return body
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return body // Validation belongs to the protocol layer.
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(doc[field], &items) != nil {
		return body // Includes Responses string input.
	}
	changed := false
	for _, item := range items {
		content := "content"
		if route == catalog.RouteResponses {
			switch sessionString(item["type"]) {
			case "", "message":
			case "function_call_output", "custom_tool_call_output":
				content = "output"
			default:
				continue
			}
		}
		if raw, ok := normalizeImageParts(item[content], route); ok {
			item[content] = raw
			changed = true
		}
	}
	if !changed {
		return body
	}
	doc[field], _ = json.Marshal(items)
	out, _ := json.Marshal(doc)
	return out
}

func normalizeImageParts(raw json.RawMessage, route catalog.Route) (json.RawMessage, bool) {
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return raw, false // Text and malformed native content stay untouched.
	}
	changed := false
	for _, part := range parts {
		switch {
		case route == catalog.RouteResponses && sessionString(part["type"]) == "input_image":
			if sessionString(part["detail"]) == "original" {
				part["detail"] = json.RawMessage(`"high"`)
				changed = true
			}
		case route == catalog.RouteChatCompletions && sessionString(part["type"]) == "image_url":
			var image map[string]json.RawMessage
			if json.Unmarshal(part["image_url"], &image) == nil && sessionString(image["detail"]) == "original" {
				image["detail"] = json.RawMessage(`"high"`)
				part["image_url"], _ = json.Marshal(image)
				changed = true
			}
		}
	}
	if !changed {
		return raw, false
	}
	out, _ := json.Marshal(parts)
	return out, true
}
