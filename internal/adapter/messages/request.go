package messages

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/dillonzq/cpa-opencode-go/internal/adapter/shared"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
	"github.com/dillonzq/cpa-opencode-go/internal/thinking"
)

type anthropicBlock = map[string]any

func textBlock(text string) anthropicBlock {
	return anthropicBlock{"type": "text", "text": text}
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthropicBlock
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// messagesRequest is the outbound Anthropic encode shape — intentionally
// divergent from shared.ClaudeTool decode type: typed stop sequences +
// named items vs raw-ignore + tool_choice.
type messagesRequest struct {
	Model         string                 `json:"model"`
	MaxTokens     int64                  `json:"max_tokens"`
	System        any                    `json:"system,omitempty"` // string or []anthropicBlock
	Messages      []anthropicMessage     `json:"messages"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Tools         []anthropicTool        `json:"tools,omitempty"`
	ToolChoice    any                    `json:"tool_choice,omitempty"` // {type,name?,disable_parallel_tool_use?}
	Thinking      *shared.ClaudeThinking `json:"thinking,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
}

// BuildRequest translates an inbound request body from sourceFormat
// ("openai" Chat Completions, "openai-response" Responses, "claude"
// passthrough) into an Anthropic Messages body for upstreamModel
// (FR-005). ts carries the target model's thinking capability so
// reasoning controls resolve against what the model actually supports.
// Unknown formats are ClassUnsupported; malformed input is
// ClassTranslation. Errors are descriptive and redacted — no silent loss.
func BuildRequest(upstreamModel string, sourceFormat string, sourceBody []byte, ts *pluginapi.ThinkingSupport, tools ...*shared.ResponseTools) ([]byte, *errclass.Error) {
	switch sourceFormat {
	case "claude":
		return passthroughClaude(upstreamModel, sourceBody)
	case "openai":
		return fromChatCompletions(upstreamModel, sourceBody, ts)
	case "openai-response":
		return fromResponses(upstreamModel, sourceBody, ts, shared.ResponseToolContext(tools))
	default:
		return nil, shared.UnsupportedFormat(sourceFormat, EndpointPath)
	}
}

// passthroughClaude rewrites the model ID on a native Claude request,
// folding any in-history role:"system" turns into the top-level system
// field (Anthropic rejects system roles inside messages). Text joins with
// blank lines; a string system stays a string, a block-array system gains
// a text block, an absent system becomes a string. Non-text system content
// is rejected descriptively, never dropped. Without system turns it is a
// pure model rewrite with no other mutation.
func passthroughClaude(upstreamModel string, sourceBody []byte) ([]byte, *errclass.Error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(sourceBody, &req); err != nil {
		return nil, errclass.Translation("malformed claude request JSON: " + err.Error())
	}
	if req == nil {
		return nil, errclass.Translation("malformed request body: JSON null is not a valid request")
	}
	rawMsgs, ok := req["messages"]
	if !ok || !shared.HasContent(rawMsgs) {
		return shared.RewriteModelID(upstreamModel, sourceBody, "claude")
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(rawMsgs, &arr); err != nil {
		return nil, errclass.Translation("malformed claude request JSON: " + err.Error())
	}
	var wire struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
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
		return shared.RewriteModelID(upstreamModel, sourceBody, "claude")
	}
	var folded []string
	kept := make([]json.RawMessage, 0, len(arr))
	for _, raw := range arr {
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
	req["model"] = json.RawMessage(`"` + upstreamModel + `"`)
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
	if !shared.HasContent(raw) {
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
			return "", shared.SystemImageRejected()
		case "thinking", "redacted_thinking":
		default:
			return "", shared.UnsupportedPartType(blk.Type, EndpointPath)
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
	if !ok || !shared.HasContent(rawSys) {
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
	b, _ := json.Marshal(textBlock(joined)) // marshallable composed type; cannot fail
	blocks = append(blocks, b)
	merged, err := json.Marshal(blocks)
	if err != nil {
		return errclass.Translation("model id cannot be represented as JSON")
	}
	req["system"] = merged
	return nil
}

// contentParts normalizes an OpenAI-style content field into Anthropic
// blocks via the shared kernel, which owns per-part ordering, validation,
// and the empty-input policy (FR-005 multimodal preservation, F5 parity).
func contentParts(raw json.RawMessage) ([]anthropicBlock, *errclass.Error) {
	return shared.ClaudeBlocksFromOpenAI(raw, EndpointPath)
}

// hasImage reports whether any normalized content block carries image
// content (FR-005).
func hasImage(blocks []anthropicBlock) bool {
	for _, blk := range blocks {
		if blk["type"] == "image" {
			return true
		}
	}
	return false
}

// decodeStop accepts the OpenAI stop field as a string or array of strings.
func decodeStop(raw json.RawMessage) ([]string, *errclass.Error) {
	if !shared.HasContent(raw) {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, errclass.Translation("stop must be a string or an array of strings")
	}
	return many, nil
}

// systemField renders collected system text as a single string when one
// piece, or text blocks when several (AC §C: system represented correctly).
func systemField(parts []string) any {
	switch len(parts) {
	case 0:
		return nil
	case 1:
		return parts[0]
	default:
		blocks := make([]anthropicBlock, len(parts))
		for i, p := range parts {
			blocks[i] = textBlock(p)
		}
		return blocks
	}
}

// msgBuilder groups consecutive blocks into alternating-role Anthropic
// messages. Buckets with distinct keys merge only when both canonical
// roles are "user" (tool_result + later user text in one turn) — the
// Messages contract rejects consecutive same-role messages.
type msgBuilder struct {
	curKey string
	cur    *anthropicMessage
	msgs   []anthropicMessage
}

func (b *msgBuilder) add(key, role string, block anthropicBlock) {
	if b.cur == nil || (b.curKey != key && !(role == "user" && b.cur.Role == "user")) {
		b.flush()
		b.cur = &anthropicMessage{Role: role, Content: []anthropicBlock{}}
		b.curKey = key
	}
	b.cur.Content = append(b.cur.Content.([]anthropicBlock), block)
}

func (b *msgBuilder) flush() {
	if b.cur == nil {
		return
	}
	blocks := b.cur.Content.([]anthropicBlock)
	if len(blocks) == 1 && blocks[0]["type"] == "text" {
		b.cur.Content = blocks[0]["text"]
	}
	b.msgs = append(b.msgs, *b.cur)
	b.cur = nil
	b.curKey = ""
}

// finalize assembles the shared envelope fields, resolves the client
// reasoning effort to a thinking budget via the model's declared
// capability (FR-005: unsupported levels are rejected explicitly, never
// dropped), and encodes the request.
func finalize(req *messagesRequest, system []string, effort string, ts *pluginapi.ThinkingSupport) ([]byte, *errclass.Error) {
	req.System = systemField(system)
	if req.Messages == nil {
		req.Messages = []anthropicMessage{}
	}
	if effort != "" {
		budget, _ := thinking.BudgetFromEffort(effort, ts)
		applyThinking(req, budget)
	}
	b, _ := json.Marshal(req) // only marshallable composed types; cannot fail
	return b, nil
}

// applyThinking maps a resolved budget onto the Messages thinking field.
// budget == 0 means reasoning off (ZeroAllowed): no thinking block, and
// sampling controls stay intact. budget < 0 is the dynamic "auto"
// sentinel; Anthropic has no dynamic budget field — an adaptive mode
// would need a catalog-declared capability signal, which does not exist
// today — so it is omitted rather than fabricated (FR-005 explicit
// omission policy). budget > 0 enables thinking and drops sampling
// controls, which Anthropic rejects alongside thinking.
func applyThinking(req *messagesRequest, budget int64) {
	if budget <= 0 {
		return
	}
	req.Thinking = &shared.ClaudeThinking{Type: "enabled", BudgetTokens: budget}
	req.Temperature, req.TopP = nil, nil
	if req.MaxTokens <= budget {
		req.MaxTokens = budget + 1024 // budget_tokens must be < max_tokens
	}
}

// fromChatCompletions translates a Chat Completions request into a
// Messages request (FR-005, AC §C): leading system/developer messages
// become the top-level system field, assistant tool_calls become tool_use
// blocks, tool results become user tool_result blocks, stop becomes
// stop_sequences, tools become input_schema definitions, reasoning_effort
// becomes a best-effort thinking budget.
//
// Explicit omission policy (FR-005): fields with no Messages equivalent
// (logprobs, frequency_penalty, n, ...) are omitted by struct selection;
// everything representable is mapped or rejected descriptively.
func fromChatCompletions(upstreamModel string, body []byte, ts *pluginapi.ThinkingSupport) ([]byte, *errclass.Error) {
	var src shared.ChatCompletionsRequest
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, errclass.Translation("malformed openai request JSON: " + err.Error())
	}
	// The shared kernel owns the FR-005 defaulting policy: a positive
	// max_tokens (or max_completion_tokens fallback) passes through;
	// absent, zero, or negative defaults to 4096.
	maxTokens := int64(0)
	if src.MaxTokens != nil {
		maxTokens = *src.MaxTokens
	} else if src.MaxCompletionTokens != nil {
		maxTokens = *src.MaxCompletionTokens
	}
	req := &messagesRequest{
		Model:       upstreamModel,
		MaxTokens:   shared.ClaudeMaxTokens(maxTokens),
		Stream:      src.Stream,
		Temperature: src.Temperature,
		TopP:        src.TopP,
	}
	stop, eErr := decodeStop(src.Stop)
	if eErr != nil {
		return nil, eErr
	}
	req.StopSequences = stop

	var system []string
	var b msgBuilder
	for i := range src.Messages {
		m := &src.Messages[i]
		switch m.Role {
		case "system", "developer":
			blocks, eErr := contentParts(m.Content)
			if eErr != nil {
				return nil, eErr
			}
			if hasImage(blocks) {
				return nil, shared.SystemImageRejected()
			}
			system = append(system, shared.JoinTexts(blocks))
		case "assistant":
			blocks, eErr := contentParts(m.Content)
			if eErr != nil {
				return nil, eErr
			}
			if hasImage(blocks) {
				return nil, errclass.Translation("assistant messages cannot carry image content")
			}
			if text := shared.JoinTexts(blocks); text != "" {
				b.add("assistant", "assistant", textBlock(text))
			}
			for _, tc := range m.ToolCalls {
				input, eErr := shared.DecodeArgs(tc.Function.Arguments)
				if eErr != nil {
					return nil, eErr
				}
				b.add("assistant", "assistant", anthropicBlock{
					"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input,
				})
			}
		case "tool":
			blocks, eErr := contentParts(m.Content)
			if eErr != nil {
				return nil, eErr
			}
			content := any(shared.JoinTexts(blocks))
			if hasImage(blocks) {
				content = blocks // mixed parts keep their per-part order
			}
			b.add("user:tool", "user", anthropicBlock{
				"type": "tool_result", "tool_use_id": m.ToolCallID, "content": content,
			})
		case "user":
			blocks, eErr := contentParts(m.Content)
			if eErr != nil {
				return nil, eErr
			}
			for _, blk := range blocks { // part order is preserved (FR-005)
				b.add("user", "user", blk)
			}
		default:
			return nil, shared.ValidateRole(m.Role, EndpointPath)
		}
	}
	b.flush()
	req.Messages = b.msgs

	for _, t := range src.Tools {
		if t.Type == "namespace" {
			continue
		}
		if eErr := shared.FunctionTool(t.Type, EndpointPath); eErr != nil {
			return nil, eErr
		}
		req.Tools = append(req.Tools, anthropicTool{
			Name: t.Function.Name, Description: t.Function.Description,
			InputSchema: shared.ObjectSchema(t.Function.Parameters),
		})
	}
	kind, tcName, eErr := shared.DecodeToolChoice(src.ToolChoice)
	if eErr != nil {
		return nil, eErr
	}
	if eErr := applyToolChoiceMessages(req, kind, tcName, src.ParallelToolCalls); eErr != nil {
		return nil, eErr
	}
	return finalize(req, system, src.ReasoningEffort, ts)
}

// fromResponses translates an OpenAI Responses request into a Messages
// request (FR-005): instructions become the system field, message items
// map like Chat Completions messages, function_call/function_call_output
// items map to tool_use/tool_result blocks, reasoning summaries are kept
// as best-effort thinking blocks (signatures unavailable upstream).
func fromResponses(upstreamModel string, body []byte, ts *pluginapi.ThinkingSupport, tools *shared.ResponseTools) ([]byte, *errclass.Error) {
	var src shared.ResponsesRequest
	if err := json.Unmarshal(body, &src); err != nil {
		return nil, errclass.Translation("malformed openai-response request JSON: " + err.Error())
	}
	// Same shared kernel policy as the Chat Completions leg (FR-005).
	maxTokens := int64(0)
	if src.MaxOutputTokens != nil {
		maxTokens = *src.MaxOutputTokens
	}
	req := &messagesRequest{
		Model:       upstreamModel,
		MaxTokens:   shared.ClaudeMaxTokens(maxTokens),
		Stream:      src.Stream,
		Temperature: src.Temperature,
		TopP:        src.TopP,
	}

	var system []string
	instr, eErr := src.DecodeInstructions()
	if eErr != nil {
		return nil, eErr
	}
	if instr != "" {
		system = append(system, instr)
	}

	var b msgBuilder
	items, eErr := tools.Normalize(&src, EndpointPath)
	if eErr != nil {
		return nil, eErr
	}
	for _, item := range items {
		switch item.Type {
		case "message":
			blocks, eErr := contentParts(item.Content)
			if eErr != nil {
				return nil, eErr
			}
			switch item.Role {
			case "user":
				for _, blk := range blocks { // part order is preserved (FR-005)
					b.add("user", "user", blk)
				}
			case "assistant":
				if hasImage(blocks) {
					return nil, errclass.Translation("assistant messages cannot carry image content")
				}
				if text := shared.JoinTexts(blocks); text != "" {
					b.add("assistant", "assistant", textBlock(text))
				}
			case "system", "developer":
				if hasImage(blocks) {
					return nil, shared.SystemImageRejected()
				}
				system = append(system, shared.JoinTexts(blocks))
			default:
				return nil, shared.ValidateRole(item.Role, EndpointPath)
			}
		case "function_call":
			input, eErr := shared.DecodeArgs(item.Arguments)
			if eErr != nil {
				return nil, eErr
			}
			b.add("assistant", "assistant", anthropicBlock{
				"type": "tool_use", "id": item.CallID, "name": item.Name, "input": input,
			})
		case "custom_tool_call":
			args := item.Arguments
			if args == "" && item.Input != "" {
				args = item.Input
			}
			input, eErr := shared.DecodeArgs(args)
			if eErr != nil {
				// Freeform custom tool input is not JSON: wrap verbatim.
				input = map[string]any{"input": args}
			}
			b.add("assistant", "assistant", anthropicBlock{
				"type": "tool_use", "id": item.CallID, "name": item.Name, "input": input,
			})
		case "function_call_output", "custom_tool_call_output":
			text, eErr := shared.RespOutputText(item.Output, "tool messages carry text only")
			if eErr != nil {
				return nil, eErr
			}
			b.add("user:tool", "user", anthropicBlock{
				"type": "tool_result", "tool_use_id": item.CallID, "content": text,
			})
		case "reasoning":
			// Best-effort: keep summary text as a thinking block;
			// encrypted_content has no Anthropic equivalent, so the
			// signature is omitted rather than fabricated (FR-005).
			var sb strings.Builder
			for i, s := range item.Summary {
				if i > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(s.Text)
			}
			if sb.Len() > 0 {
				b.add("assistant", "assistant", anthropicBlock{
					"type": "thinking", "thinking": sb.String(),
				})
			}
		default:
			return nil, shared.UnsupportedInputItemType(item.Type)
		}
	}
	b.flush()
	req.Messages = b.msgs

	for _, t := range src.Tools {
		if eErr := shared.FunctionTool(t.Type, EndpointPath); eErr != nil {
			return nil, eErr
		}
		schema := shared.ObjectSchema(t.Parameters)
		req.Tools = append(req.Tools, anthropicTool{
			Name: t.Name, Description: t.Description, InputSchema: schema,
		})
	}
	effort := ""
	if src.Reasoning != nil {
		effort = src.Reasoning.Effort
	}
	kind, tcName, eErr := shared.DecodeToolChoice(src.ToolChoice)
	if eErr != nil {
		return nil, eErr
	}
	if eErr := applyToolChoiceMessages(req, kind, tcName, src.ParallelToolCalls); eErr != nil {
		return nil, eErr
	}
	return finalize(req, system, effort, ts)
}

// applyToolChoiceMessages maps a normalized tool choice onto the outbound
// Messages request. "none" has no Anthropic equivalent and fails
// descriptively rather than silently unforcing the client constraint
// (FR-005 explicit policy). An explicit parallel_tool_calls=false is a
// client constraint too (FR-005): it becomes disable_parallel_tool_use on
// the synthesized or decoded tool_choice — synthesized as {"type":"auto"}
// when no tool_choice was sent, so the flag never rides alone.
func applyToolChoiceMessages(req *messagesRequest, kind, name string, parallel *bool) *errclass.Error {
	noParallel := parallel != nil && !*parallel
	if kind == shared.ToolChoiceAbsent && noParallel {
		kind = shared.ToolChoiceAuto
	}
	switch kind {
	case shared.ToolChoiceAbsent:
		return nil
	case shared.ToolChoiceAuto:
		req.ToolChoice = toolChoiceBlock("auto", "", noParallel)
	case shared.ToolChoiceAny:
		req.ToolChoice = toolChoiceBlock("any", "", noParallel)
	case shared.ToolChoiceNamed:
		req.ToolChoice = toolChoiceBlock("tool", name, noParallel)
	default:
		return &errclass.Error{
			Class:   errclass.ClassUnsupported,
			Message: fmt.Sprintf("tool_choice %q cannot be represented for %s", kind, EndpointPath),
		}
	}
	return nil
}

// toolChoiceBlock renders one outbound tool_choice object; noParallel is
// Anthropic's disable_parallel_tool_use flag.
func toolChoiceBlock(typ, name string, noParallel bool) map[string]any {
	tc := map[string]any{"type": typ}
	if name != "" {
		tc["name"] = name
	}
	if noParallel {
		tc["disable_parallel_tool_use"] = true
	}
	return tc
}
