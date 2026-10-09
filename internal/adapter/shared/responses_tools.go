package shared

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
)

// responseToolIdentity is the original (name, namespace) pair behind one
// flattened wire name. A plain function tool carries an empty namespace.
// isCustom marks tools originally declared as type "custom" so the
// outbound path restores custom_tool_call items.
type responseToolIdentity struct {
	name      string
	namespace string
	isCustom  bool
}

// ResponseTools tracks flattened namespace wire names to their original
// identity so outbound function calls restore on the return path.
type ResponseTools struct {
	names map[string]responseToolIdentity
}

// NewResponseTools prepares an empty namespace registry.
func NewResponseTools() *ResponseTools {
	return &ResponseTools{names: make(map[string]responseToolIdentity)}
}

// ResponseToolContext resolves the registry for a request conversion:
// the first non-nil entry wins, otherwise a fresh registry serves
// request-local normalization.
func ResponseToolContext(tools []*ResponseTools) *ResponseTools {
	for _, t := range tools {
		if t != nil {
			return t
		}
	}
	return NewResponseTools()
}

// Identity resolves a wire tool name to its original identity. Unknown
// names (and a nil registry) pass through as a plain name.
func (t *ResponseTools) Identity(name string) responseToolIdentity {
	if t == nil {
		return responseToolIdentity{name: name}
	}
	if id, ok := t.names[name]; ok {
		return id
	}
	return responseToolIdentity{name: name}
}

// IsCustom reports whether a wire tool name was originally declared as
// type "custom". Unknown names (and a nil registry) report false.
func (t *ResponseTools) IsCustom(name string) bool {
	if t != nil && t.names != nil {
		if id, ok := t.names[name]; ok {
			return id.isCustom
		}
	}
	return false
}

// UnwrapCustomToolInput extracts the input string from custom tool call
// arguments: a JSON object with properties like "input", "code", or "arguments"
// unwraps to that raw string value, otherwise the arguments pass through verbatim.
func UnwrapCustomToolInput(args string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &m) == nil {
		for _, key := range []string{"input", "code", "arguments", "cmd", "command"} {
			if raw, ok := m[key]; ok {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					return s
				}
				return string(raw)
			}
		}
		if len(m) == 1 {
			for _, raw := range m {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					return s
				}
			}
		}
	}
	return args
}

// qualifiedToolName flattens one namespaced child to its wire name. Names
// longer than 64 characters truncate to 50 chars plus "__" plus the first
// 6 bytes of the SHA-256 digest hex-encoded (50+2+12 = 64).
func qualifiedToolName(name, namespace string) string {
	qualified := namespace + "__" + name
	if len(qualified) > 64 {
		sum := sha256.Sum256([]byte(qualified))
		qualified = qualified[:50] + "__" + hex.EncodeToString(sum[:6])
	}
	return qualified
}

// normalizeCustomTool drops client-only tools (apply_patch, tool_search,
// image_generation) and, on translated routes, hosted web search tools
// (web_search, web_search_preview) with no function-calling equivalent. It
// rewrites generic custom tools to functions with a default empty-object
// parameters schema. It reports false when the tool must be dropped.
func normalizeCustomTool(tool *RespTool, target string) bool {
	if tool.Type == "custom" && tool.Name == "apply_patch" {
		return false
	}
	if tool.Type == "tool_search" || tool.Type == "image_generation" {
		return false
	}
	if (target == "/v1/chat/completions" || target == "/v1/messages") &&
		(tool.Type == "web_search" || tool.Type == "web_search_preview") {
		return false
	}
	if tool.Type == "custom" {
		tool.Type = "function"
		if len(tool.Parameters) == 0 {
			tool.Parameters = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":true}`)
		}
	}
	return true
}

// Normalize merges additional_tools input items into r.Tools, unrolls
// namespace tools to flattened function tools, and rewrites historical
// function_call items plus a namespaced tool_choice to wire names. It
// returns the conversation items with additional_tools stripped. r.Tools
// and r.ToolChoice are rewritten in place.
func (t *ResponseTools) Normalize(r *ResponsesRequest, target string) ([]RespItem, *errclass.Error) {
	if t.names == nil {
		t.names = make(map[string]responseToolIdentity)
	}
	items, eErr := r.DecodeInputItems()
	if eErr != nil {
		return nil, eErr
	}
	var conv []RespItem
	for _, item := range items {
		if item.Type == "additional_tools" {
			if item.Tools == nil {
				return nil, errclass.Translation("additional_tools requires a tools array")
			}
			r.Tools = append(r.Tools, item.Tools...)
			continue
		}
		conv = append(conv, item)
	}
	var flat []RespTool
	for _, tool := range r.Tools {
		if tool.Type != "namespace" {
			wasCustom := tool.Type == "custom"
			if !normalizeCustomTool(&tool, target) {
				continue
			}
			if wasCustom {
				if _, ok := t.names[tool.Name]; !ok {
					t.names[tool.Name] = responseToolIdentity{name: tool.Name, isCustom: true}
				}
			}
			flat = append(flat, tool)
			continue
		}
		if strings.TrimSpace(tool.Name) == "" || tool.Tools == nil {
			return nil, errclass.Translation("namespace tool requires a name and tools array")
		}
		for _, child := range tool.Tools {
			wasCustom := child.Type == "custom"
			if !normalizeCustomTool(&child, target) {
				continue
			}
			if eErr := FunctionTool(child.Type, target); eErr != nil {
				return nil, eErr
			}
			qualified := qualifiedToolName(child.Name, tool.Name)
			id := responseToolIdentity{name: child.Name, namespace: tool.Name, isCustom: wasCustom}
			if prev, ok := t.names[qualified]; ok {
				if prev != id {
					return nil, errclass.Translation("Responses tool names collide after namespace conversion")
				}
				continue // duplicate identical declaration: first wins
			}
			t.names[qualified] = id
			child.Name = qualified
			flat = append(flat, child)
		}
	}
	r.Tools = flat
	for i := range conv {
		if (conv[i].Type == "function_call" || conv[i].Type == "custom_tool_call") && conv[i].Namespace != "" {
			id := responseToolIdentity{name: conv[i].Name, namespace: conv[i].Namespace}
			qualified := qualifiedToolName(conv[i].Name, conv[i].Namespace)
			if _, ok := t.names[qualified]; !ok {
				t.names[qualified] = id
			}
			conv[i].Name = qualified
			conv[i].Namespace = ""
		}
	}
	if len(r.ToolChoice) > 0 {
		var tc struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		}
		if err := json.Unmarshal(r.ToolChoice, &tc); err == nil && tc.Namespace != "" {
			qualified := qualifiedToolName(tc.Name, tc.Namespace)
			if _, ok := t.names[qualified]; !ok {
				t.names[qualified] = responseToolIdentity{name: tc.Name, namespace: tc.Namespace}
			}
			b, _ := json.Marshal(map[string]any{"type": tc.Type, "name": qualified})
			r.ToolChoice = b
		}
	}
	return conv, nil
}
