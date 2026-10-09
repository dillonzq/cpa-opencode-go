package plugin

import (
	"encoding/json"

	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
	"github.com/dillonzq/cpa-opencode-go/internal/thinking"
)

// buildUpstreamBody translates the effective payload for the resolved route and
// then applies the requested model-name thinking suffix. CPA consumes a suffix
// only on its own executor paths, so a plugin executor receives the raw ID: the
// plugin strips the suffix for routing and applies the equivalent control
// itself, with suffix priority over the body — in CPA the suffix selects the
// configuration and the body's own control is never consumed.
func buildUpstreamBody(res *resolvedExecution, req executorRequest) ([]byte, *errclass.Error) {
	source := req.effectivePayload()
	if res.suffix.Effort != "" {
		// A recognized suffix replaced the client's control, so that control
		// must not be translated: a value the target cannot represent would
		// fail the conversion before the override below runs.
		source = stripSupersededReasoning(res.rec.Protocol, req.inputFormat(), source)
	}
	body, eErr := buildUpstreamRequest(res.rec.Protocol, res.rec.UpstreamID, req.inputFormat(), source, res.rec.Thinking, &res.tools)
	if eErr != nil {
		return nil, eErr
	}
	return applyReasoningSuffix(res.rec.Protocol, body, res.suffix)
}

// stripSupersededReasoning removes the client's own reasoning control from the
// source payload when a recognized suffix supersedes it. Only the (route,
// source format) pairs whose conversion derives the target control from that
// field are touched, because only those can reject a value the suffix already
// replaced. Everything else keeps its body untouched: a native Messages
// request passes the control through without validating it, and its siblings
// (display) are preserved by the override.
func stripSupersededReasoning(route catalog.Route, sourceFormat string, body []byte) []byte {
	dropThinking := sourceFormat == "claude" &&
		(route == catalog.RouteChatCompletions || route == catalog.RouteResponses)
	dropEffort := route == catalog.RouteMessages && sourceFormat == "openai"
	dropNestedEffort := route == catalog.RouteMessages && sourceFormat == "openai-response"
	if !dropThinking && !dropEffort && !dropNestedEffort {
		return body
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil || req == nil {
		// Malformed input stays the adapter's to classify and report.
		return body
	}
	switch {
	case dropThinking:
		// Chat Completions and Responses derive their control from the
		// Messages thinking object and reject an unknown type.
		delete(req, "thinking")
	case dropEffort:
		delete(req, "reasoning_effort")
	default:
		// Messages converts the effort through the fixed budget table and
		// reports a level with no equivalent; the other reasoning fields
		// (summary, and any future sibling) stay untouched.
		deleteNested(req, "reasoning", "effort")
	}
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// applyReasoningSuffix rewrites the reasoning control of an upstream body the
// route adapter already built, so the requested model's suffix takes priority
// over the control the client sent. It runs only for a recognized suffix value:
// without one the body stays byte-identical — no suffix, or a value CPA would
// not interpret, which CPA strips while applying no configuration.
func applyReasoningSuffix(route catalog.Route, body []byte, suffix thinking.Suffix) ([]byte, *errclass.Error) {
	if suffix.Effort == "" {
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
		applyChatEffort(req, suffix.Effort)
	case catalog.RouteMessages:
		applyMessagesEffort(req, suffix)
	case catalog.RouteResponses:
		applyResponsesEffort(req, suffix.Effort)
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
// upstream applies its own default, which is how every adapter renders auto for
// a protocol without an auto wire value.
func applyChatEffort(req map[string]json.RawMessage, effort string) {
	if effort == "auto" {
		delete(req, "reasoning_effort")
		return
	}
	req["reasoning_effort"] = jsonRaw(effort)
}

// applyMessagesEffort overrides the Anthropic thinking control: none disables
// thinking, a level becomes an enabled budget from the fixed table, a numeric
// suffix keeps its exact budget (CPA's ModeBudget), and auto removes the
// control so the upstream picks its default. Sibling fields the client sent
// (display) survive the enabled override; CPA drops display when thinking is
// disabled, because display only applies to an active block. An adaptive
// output_config.effort is always superseded.
func applyMessagesEffort(req map[string]json.RawMessage, suffix thinking.Suffix) {
	deleteNested(req, "output_config", "effort")
	if suffix.Effort == "auto" {
		delete(req, "thinking")
		return
	}
	budget, ok := messagesBudget(suffix)
	if !ok {
		return
	}
	thinkingObj := nestedObject(req, "thinking")
	if budget == 0 {
		delete(thinkingObj, "budget_tokens")
		delete(thinkingObj, "display")
		thinkingObj["type"] = jsonRaw("disabled")
	} else {
		thinkingObj["type"] = jsonRaw("enabled")
		thinkingObj["budget_tokens"] = jsonRaw(budget)
	}
	req["thinking"] = jsonObject(thinkingObj)
}

// messagesBudget resolves the Messages budget for a recognized suffix: an
// exact numeric value keeps its number, a level uses the fixed table.
func messagesBudget(suffix thinking.Suffix) (int64, bool) {
	if suffix.Budget != nil {
		return *suffix.Budget, true
	}
	return thinking.BudgetFromEffort(suffix.Effort)
}

// applyResponsesEffort overrides reasoning.effort inside an existing reasoning
// object so sibling fields (summary, encrypted_content) survive. Auto removes
// the effort, and an emptied reasoning object is dropped.
func applyResponsesEffort(req map[string]json.RawMessage, effort string) {
	if effort == "auto" {
		deleteNested(req, "reasoning", "effort")
		return
	}
	reasoning := nestedObject(req, "reasoning")
	reasoning["effort"] = jsonRaw(effort)
	req["reasoning"] = jsonObject(reasoning)
}

// deleteNested removes parent.child, dropping the parent object when it holds
// nothing else. A parent that is absent or not a JSON object is left alone.
func deleteNested(req map[string]json.RawMessage, parent, child string) {
	if _, ok := req[parent]; !ok {
		return
	}
	obj := nestedObject(req, parent)
	if _, ok := obj[child]; !ok {
		return
	}
	delete(obj, child)
	if len(obj) == 0 {
		delete(req, parent)
		return
	}
	req[parent] = jsonObject(obj)
}

// nestedObject reads a parent field as a mutable object; an absent parent, a
// non-object parent, and a JSON null all start empty.
func nestedObject(req map[string]json.RawMessage, parent string) map[string]json.RawMessage {
	obj := map[string]json.RawMessage{}
	if raw, ok := req[parent]; ok {
		if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
			obj = map[string]json.RawMessage{}
		}
	}
	return obj
}

// jsonObject re-encodes a composed object. Its values came from a successful
// unmarshal and jsonRaw encodes Go builtins, so marshaling cannot fail.
func jsonObject(obj map[string]json.RawMessage) json.RawMessage {
	b, err := json.Marshal(obj)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// jsonRaw encodes a JSON scalar (a canonical effort string or an int64 budget).
func jsonRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
