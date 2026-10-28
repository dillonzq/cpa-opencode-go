package plugin

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestDeepSeekThinkingPreservesPayload(t *testing.T) {
	for _, tc := range []struct {
		name, model, body, want string
		route                   catalog.Route
	}{
		{"chat", "DeepSeek-V4-Pro", `{"reasoning_effort":"none","thinking":{"type":"enabled","budget_tokens":1024},"n":9007199254740993,"tools":[{"parameters":{"reasoning_effort":"none"}}]}`, `{"thinking":{"type":"disabled"},"n":9007199254740993,"tools":[{"parameters":{"reasoning_effort":"none"}}]}`, catalog.RouteChatCompletions},
		{"messages format preserved", "deepseek-flash", `{"output_config":{"effort":"none","format":{"type":"json_schema","schema":{"const":9007199254740993}}},"thinking":{"type":"adaptive","display":"summarized"}}`, `{"output_config":{"format":{"type":"json_schema","schema":{"const":9007199254740993}}},"thinking":{"type":"disabled"}}`, catalog.RouteMessages},
		{"messages empty config removed", "deepseek-flash", `{"output_config":{"effort":"none"}}`, `{"thinking":{"type":"disabled"}}`, catalog.RouteMessages},
		{"other chat model", "glm-5.3", `{"reasoning_effort":"none"}`, "", catalog.RouteChatCompletions},
		{"other messages model", "claude-opus", `{"output_config":{"effort":"none"}}`, "", catalog.RouteMessages},
		{"responses unchanged", "deepseek-flash", `{"reasoning":{"effort":"none"}}`, "", catalog.RouteResponses},
		{"chat high unchanged", "deepseek-flash", `{"reasoning_effort":"high"}`, "", catalog.RouteChatCompletions},
		{"messages high unchanged", "deepseek-flash", `{"output_config":{"effort":"high"}}`, "", catalog.RouteMessages},
		{"no effort", "deepseek-flash", `{"messages":[]}`, "", catalog.RouteChatCompletions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeDeepSeekThinking(tc.route, tc.model, []byte(tc.body))
			if tc.want == "" {
				if string(got) != tc.body {
					t.Fatalf("unrelated request changed: %s", got)
				}
				return
			}
			assertJSONEqual(t, []byte(tc.want), got)
		})
	}
}

// Inspect actual host HTTP payloads for native/converted requests, suffix
// precedence, and both execution paths. Pin the destination independently of
// the client format to exercise fallback routing as well as native routing.
func TestExecuteDeepSeekThinkingNone(t *testing.T) {
	for _, route := range []string{"chat-completions", "messages"} {
		for _, tc := range []struct {
			name, source, suffix, body string
			disabled                   bool
		}{
			{"chat none", "openai", "", `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`, true},
			{"messages none", "claude", "", `{"messages":[{"role":"user","content":"hi"}],"max_tokens":4096,"thinking":{"type":"adaptive"},"output_config":{"effort":"none"}}`, true},
			{"messages standalone none", "claude", "", `{"messages":[{"role":"user","content":"hi"}],"max_tokens":4096,"output_config":{"effort":"none"}}`, true},
			{"messages none overrides budget", "claude", "", `{"messages":[{"role":"user","content":"hi"}],"max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"none"}}`, true},
			{"responses none", "openai-response", "", `{"input":"hi","reasoning":{"effort":"none"}}`, true},
			{"suffix overrides high", "openai", "(none)", `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high","thinking":{"type":"enabled"}}`, true},
			{"zero suffix", "claude", "(0)", `{"messages":[{"role":"user","content":"hi"}],"max_tokens":4096,"output_config":{"effort":"high"}}`, true},
			{"high suffix overrides none", "openai", "(high)", `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`, false},
			{"high suffix overrides messages none", "claude", "(high)", `{"messages":[{"role":"user","content":"hi"}],"max_tokens":4096,"output_config":{"effort":"none"}}`, false},
		} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", route, tc.name, stream), func(t *testing.T) {
					path, response, frames := "/openai/v1/chat/completions", ccResponseBody, chatSSEFrames
					if route == "messages" {
						path, response, frames = "/anthropic/v1/messages", claudeResponseBody, messagesSSEFrames
					}
					yaml := testValidYAML + "\nmodels:\n  - name: deepseek-v4-pro\n    protocols:\n" +
						fmt.Sprintf("      chat-completions: {enabled: %t}\n      messages: {enabled: %t}\n      responses: {enabled: false}\n", route == "chat-completions", route == "messages")
					responder := upstreamRouter(t, map[string]string{path: response})
					if stream {
						responder = streamResponder(streamScript{upstreamID: "deepseek-none", frames: frames})
					}
					f := &fakeCaller{responder: wrapWithCatalog(`{"data":[{"id":"deepseek-v4-pro"}]}`, responder)}
					m := NewManager(NewHostBridge(f.call))
					t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
					registered, err := m.HandleCall("plugin.register", lifecycleRequestBody(yaml))
					if err != nil || !decodeEnv(t, registered).OK {
						t.Fatalf("register: %v %s", err, registered)
					}
					payload := strings.TrimSuffix(tc.body, "}") + fmt.Sprintf(",\"stream\":%t}", stream)
					req := executorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
						Model: "opencode-go/deepseek-v4-pro" + tc.suffix, AuthProvider: ProviderID,
						AuthAttributes: map[string]string{"api_key": testKey}, SourceFormat: tc.source, Format: tc.source,
						Payload: []byte(payload), Stream: stream,
					}, StreamID: "down-deepseek-none"}
					raw, err := m.HandleCall(pluginabi.MethodExecutorExecute, mustJSON(req))
					if err != nil || !decodeEnv(t, raw).OK {
						t.Fatalf("execute: %v %s", err, raw)
					}
					if !m.bridge.WaitForInFlight(5 * time.Second) {
						t.Fatal("execution did not drain")
					}
					method := pluginabi.MethodHostHTTPDo
					if stream {
						method = pluginabi.MethodHostHTTPDoStream
						assertCleanStreamClose(t, f)
					}
					wire := upstreamRequestBody(t, f, method)
					assertField(t, wire, "model", "deepseek-v4-pro")
					if tc.disabled {
						assertJSONEqual(t, []byte(`{"type":"disabled"}`), mustJSON(wire["thinking"]))
						assertField(t, wire, "reasoning_effort", nil)
						if got := dig(t, wire, "output_config.effort"); got != nil {
							t.Fatalf("disabled thinking retained effort: %v", got)
						}
					} else if route == "chat-completions" {
						assertField(t, wire, "reasoning_effort", "high")
						if got := dig(t, wire, "thinking.type"); got == "disabled" {
							t.Fatal("body none overrode high suffix")
						}
					} else if dig(t, wire, "thinking.type") != "adaptive" || dig(t, wire, "output_config.effort") != "high" {
						t.Fatalf("high suffix lost: %s", mustJSON(wire))
					}
				})
			}
		}
	}
}
