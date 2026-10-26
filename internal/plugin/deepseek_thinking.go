package plugin

import (
	"encoding/json"
	"strings"

	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
)

// normalizeDeepSeekThinking renders none using the gateway's explicit switch.
// Run after translation and suffix application so the effective control wins.
// Responses keeps its native reasoning.effort representation.
func normalizeDeepSeekThinking(route catalog.Route, model string, body []byte) []byte {
	if !strings.HasPrefix(strings.ToLower(model), "deepseek") ||
		(route != catalog.RouteChatCompletions && route != catalog.RouteMessages) {
		return body
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil || doc == nil {
		return body // Validation belongs to the protocol layer.
	}
	effort := doc["reasoning_effort"]
	var output map[string]json.RawMessage
	if route == catalog.RouteMessages {
		if json.Unmarshal(doc["output_config"], &output) != nil {
			return body
		}
		effort = output["effort"]
	}
	if !strings.EqualFold(strings.TrimSpace(sessionString(effort)), "none") {
		return body
	}
	doc["thinking"] = json.RawMessage(`{"type":"disabled"}`)
	if route == catalog.RouteChatCompletions {
		delete(doc, "reasoning_effort")
	} else {
		delete(output, "effort")
		if len(output) == 0 {
			delete(doc, "output_config")
		} else {
			doc["output_config"], _ = json.Marshal(output)
		}
	}
	out, _ := json.Marshal(doc)
	return out
}
