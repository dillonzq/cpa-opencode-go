package plugin

import (
	"encoding/json"

	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
	"github.com/dillonzq/cpa-opencode-go/internal/thinking"
)

// applyReasoningSuffix rewrites the reasoning control of an upstream body the
// route adapter already built. CPA model-name thinking suffixes (model(high))
// reach a plugin executor as part of the requested model ID, but CPA consumes
// the suffix only on its own executor paths, so the plugin strips it for
// catalog lookup and applies the equivalent control itself, with the suffix
// winning over the body like CPA's suffix priority. effort is the canonical
// suffix effort; "" leaves the body byte-identical (no suffix, or a value CPA
// itself would not interpret).
func applyReasoningSuffix(route catalog.Route, body []byte, effort string) ([]byte, *errclass.Error) {
	if effort == "" {
		return body, nil
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errclass.Translation("malformed upstream request JSON: " + err.Error())
	}
	if req == nil {
		return nil, errclass.Translation("malformed request body: JSON null is not a valid request")
	}
	switch route {
	case catalog.RouteChatCompletions:
		applyChatEffort(req, effort)
	case catalog.RouteMessages:
		applyMessagesEffort(req, effort)
	case catalog.RouteResponses:
		applyResponsesEffort(req, effort)
	default:
		return body, nil
	}
	out, err := json.Marshal(req)
	if err != nil {
		return nil, errclass.Translation("reasoning control cannot be represented as JSON")
	}
	return out, nil
}

// applyChatEffort overrides reasoning_effort. Auto removes the control so the
// upstream applies its own default, which is how every adapter renders auto
// for a protocol without an auto wire value.
func applyChatEffort(req map[string]json.RawMessage, effort string) {
	if effort == "auto" {
		delete(req, "reasoning_effort")
		return
	}
	req["reasoning_effort"] = jsonValue(effort)
}

// applyMessagesEffort overrides the Anthropic thinking control using the same
// fixed budget table as cross-format conversion: none disables thinking, a
// level becomes an enabled budget, and auto removes the control so the
// upstream picks its default. Adaptive output_config.effort is dropped so it
// cannot outlive the suffix.
func applyMessagesEffort(req map[string]json.RawMessage, effort string) {
	deleteNested(req, "output_config", "effort")
	if effort == "auto" {
		delete(req, "thinking")
		return
	}
	budget, ok := thinking.BudgetFromEffort(effort)
	if !ok {
		return
	}
	if budget == 0 {
		req["thinking"] = json.RawMessage(`{"type":"disabled"}`)
		return
	}
	b, err := json.Marshal(map[string]any{"type": "enabled", "budget_tokens": budget})
	if err != nil {
		return
	}
	req["thinking"] = b
}

// applyResponsesEffort overrides reasoning.effort inside an existing
// reasoning object so sibling fields (summary, encrypted_content) survive.
// Auto removes the effort; an emptied reasoning object is dropped.
func applyResponsesEffort(req map[string]json.RawMessage, effort string) {
	if effort == "auto" {
		deleteNested(req, "reasoning", "effort")
		return
	}
	reasoning := map[string]json.RawMessage{}
	if raw, ok := req["reasoning"]; ok {
		if err := json.Unmarshal(raw, &reasoning); err != nil || reasoning == nil {
			reasoning = map[string]json.RawMessage{}
		}
	}
	reasoning["effort"] = jsonValue(effort)
	b, err := json.Marshal(reasoning)
	if err != nil {
		return
	}
	req["reasoning"] = b
}

// deleteNested removes parent.child, dropping the parent object when it holds
// nothing else. A parent that is absent or not a JSON object is left alone.
func deleteNested(req map[string]json.RawMessage, parent, child string) {
	raw, ok := req[parent]
	if !ok {
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return
	}
	if _, ok := obj[child]; !ok {
		return
	}
	delete(obj, child)
	if len(obj) == 0 {
		delete(req, parent)
		return
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return
	}
	req[parent] = b
}

// jsonValue encodes a suffix effort (a closed lowercase ASCII set) as a JSON
// string value; marshaling cannot fail on it.
func jsonValue(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
