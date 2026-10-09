package adapters

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/dillonzq/cpa-opencode-go/internal/adapter/chatcompletions"
	"github.com/dillonzq/cpa-opencode-go/internal/adapter/messages"
	"github.com/dillonzq/cpa-opencode-go/internal/adapter/responses"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
)

func reasoningRequest(t *testing.T, target, source string, body []byte, capability *pluginapi.ThinkingSupport) (map[string]any, *errclass.Error) {
	t.Helper()
	var out []byte
	var eErr *errclass.Error
	switch target {
	case "openai":
		out, eErr = chatcompletions.BuildRequest("m", source, body, capability)
	case "openai-response":
		out, eErr = responses.BuildRequest("m", source, body, capability)
	case "claude":
		out, eErr = messages.BuildRequest("m", source, body, capability)
	}
	if eErr != nil {
		return nil, eErr
	}
	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	return req, nil
}

// Capability metadata must not affect reasoning in either streaming or
// non-streaming requests, even when the declared range/list excludes the input.
func TestReasoningConversionIgnoresCapabilities(t *testing.T) {
	capabilities := []*pluginapi.ThinkingSupport{
		nil, {},
		{Min: 2048, Max: 4096, Levels: []string{"none", "low"}},
		{ZeroAllowed: true, DynamicAllowed: true, Levels: []string{"high"}},
	}
	for _, source := range []string{"openai", "openai-response"} {
		for _, target := range []string{"openai", "openai-response", "claude"} {
			for _, effort := range []string{"none", "auto", "minimal", "xhigh", "max", "turbo"} {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/stream=%t", source, target, effort, stream), func(t *testing.T) {
						body := map[string]any{"stream": stream, "temperature": 0.5, "top_p": 0.9}
						if source == "openai" {
							body["messages"], body["max_tokens"], body["reasoning_effort"] = []any{}, 64, effort
						} else {
							body["input"], body["max_output_tokens"], body["reasoning"] = []any{}, 64, map[string]any{"effort": effort}
						}
						raw, _ := json.Marshal(body)
						var baseline map[string]any
						for _, capability := range capabilities {
							req, eErr := reasoningRequest(t, target, source, raw, capability)
							if target == "claude" && effort == "turbo" {
								if eErr == nil || eErr.Class != errclass.ClassUnsupported {
									t.Fatalf("unconvertible effort silently lost: %v", eErr)
								}
								continue
							}
							if eErr != nil {
								t.Fatal(eErr)
							}
							if baseline != nil && !reflect.DeepEqual(req, baseline) {
								t.Fatalf("capabilities changed request: %v vs %v", req, baseline)
							}
							baseline = req
							if req["temperature"] != 0.5 || req["top_p"] != 0.9 || (req["stream"] == true) != stream {
								t.Fatalf("client controls changed: %v", req)
							}
							switch target {
							case "openai":
								if effort == "auto" && source != target {
									if _, has := req["reasoning_effort"]; has {
										t.Fatalf("auto must use defaults: %v", req)
									}
									continue
								}
								if req["reasoning_effort"] != effort {
									t.Fatalf("effort lost: %v", req)
								}
							case "openai-response":
								if effort == "auto" && source != target {
									if _, has := req["reasoning"]; has {
										t.Fatalf("auto must use defaults: %v", req)
									}
									continue
								}
								if req["reasoning"].(map[string]any)["effort"] != effort {
									t.Fatalf("effort lost: %v", req)
								}
							case "claude":
								if effort == "auto" {
									if _, has := req["thinking"]; has {
										t.Fatalf("auto must use defaults: %v", req)
									}
									continue
								}
								th := req["thinking"].(map[string]any)
								wantType := "enabled"
								if effort == "none" {
									wantType = "disabled"
								}
								if th["type"] != wantType || req["max_tokens"] != float64(64) {
									t.Fatalf("thinking/limit changed: %v", req)
								}
								wantBudget := map[string]any{"minimal": float64(512), "xhigh": float64(32768), "max": float64(128000)}[effort]
								if th["budget_tokens"] != wantBudget {
									t.Fatalf("budget changed: %v", th)
								}
							}
						}
					})
				}
			}
		}
	}
}

func TestClaudeReasoningControlsAcrossFormats(t *testing.T) {
	for _, target := range []string{"openai", "openai-response"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct{ control, effort string }{
				{``, ""},
				{`,"thinking":{"type":"disabled","budget_tokens":8192}`, "none"},
				{`,"thinking":{"type":"enabled"}`, "auto"},
				{`,"thinking":{"type":"enabled","budget_tokens":0}`, "none"},
				{`,"thinking":{"type":"enabled","budget_tokens":-1}`, "auto"},
				{`,"thinking":{"type":"enabled","budget_tokens":512}`, "minimal"},
				{`,"thinking":{"type":"enabled","budget_tokens":8192}`, "medium"},
				{`,"thinking":{"type":"enabled","budget_tokens":128000}`, "xhigh"},
				{`,"thinking":{"type":"adaptive"}`, "auto"},
				{`,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}`, "max"},
				{`,"thinking":{"type":"adaptive"},"output_config":{"effort":"turbo"}`, "turbo"},
			} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", target, tc.control, stream), func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"messages":[],"max_tokens":64,"stream":%t%s}`, stream, tc.control))
					for _, capability := range []*pluginapi.ThinkingSupport{nil, {Min: 4096, Max: 8192, Levels: []string{"none", "low"}}} {
						req, eErr := reasoningRequest(t, target, "claude", body, capability)
						if eErr != nil {
							t.Fatal(eErr)
						}
						var effort any
						if target == "openai" {
							effort = req["reasoning_effort"]
						} else if r, ok := req["reasoning"].(map[string]any); ok {
							effort = r["effort"]
						}
						if tc.effort == "" || tc.effort == "auto" {
							if effort != nil {
								t.Fatalf("default thinking became explicit effort %v", effort)
							}
						} else if effort != tc.effort {
							t.Fatalf("effort = %v, want %s", effort, tc.effort)
						}
					}
				})
			}
			for _, control := range []string{`{"type":"enabled","budget_tokens":-2}`, `{"type":"future"}`} {
				_, eErr := reasoningRequest(t, target, "claude", []byte(fmt.Sprintf(`{"stream":%t,"thinking":%s}`, stream, control)), nil)
				if eErr == nil || eErr.Class != errclass.ClassUnsupported {
					t.Fatalf("invalid control silently lost: %v", eErr)
				}
			}
		}
	}
}

func TestNativeReasoningPreserved(t *testing.T) {
	for source, control := range map[string]string{
		"openai":          `"reasoning_effort":"turbo","thinking":{"type":"future","budget_tokens":-2}`,
		"openai-response": `"reasoning":{"effort":"turbo","summary":"detailed","future":true}`,
		"claude":          `"thinking":{"type":"adaptive","future":true},"output_config":{"effort":"turbo","future":true}`,
	} {
		for _, stream := range []bool{false, true} {
			raw := []byte(fmt.Sprintf(`{"model":"old","messages":[],"stream":%t,%s}`, stream, control))
			req, eErr := reasoningRequest(t, source, source, raw, &pluginapi.ThinkingSupport{Levels: []string{"low"}})
			if eErr != nil {
				t.Fatal(eErr)
			}
			var original map[string]any
			json.Unmarshal(raw, &original)
			for _, field := range []string{"reasoning_effort", "thinking", "reasoning", "output_config"} {
				if !reflect.DeepEqual(original[field], req[field]) {
					t.Fatalf("native %s %s changed: %v vs %v", source, field, req[field], original[field])
				}
			}
		}
	}
}
