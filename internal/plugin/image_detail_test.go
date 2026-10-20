package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const originalImageResponses = `{"input":[
 {"role":"user","content":[{"type":"input_text","text":"old image"},{"type":"input_image","image_url":"https://example.com/history.png","detail":"original","extension":9007199254740993}]},
 {"type":"function_call","call_id":"c","name":"view_image","arguments":"{\"detail\":\"original\"}"},
 {"type":"function_call_output","call_id":"c","output":[{"type":"input_text","text":"tool result"},{"type":"input_image","image_url":"data:image/png;base64,YWJj","detail":"original"}]},
 {"role":"user","content":"continue"}
],"tools":[{"type":"function","name":"view_image","parameters":{"type":"object","properties":{"detail":{"type":"string","enum":["high","original"]}}}}]}`

func TestDeepSeekOriginalImageDetail(t *testing.T) {
	chat := `{"messages":[{"role":"user","content":[{"type":"text","text":"original"},{"type":"image_url","image_url":{"url":"https://example.com/history.png","detail":"original","extension":9007199254740993}}]},{"role":"tool","tool_call_id":"c","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,YWJj","detail":"original"}}]},{"role":"user","content":"continue"}],"future":9007199254740993}`
	for _, tc := range []struct {
		name, model, source, body string
		route                     catalog.Route
		changed                   bool
	}{
		{"chat history", "deepseek-v4.1-flash", "openai", chat, catalog.RouteChatCompletions, true},
		{"responses history", "deepseek-v4.1-flash", "openai-response", originalImageResponses, catalog.RouteResponses, true},
		{"case insensitive family", "DeepSeek-V4-Pro", "openai", chat, catalog.RouteChatCompletions, true},
		{"other chat model", "glm-5.3", "openai", chat, catalog.RouteChatCompletions, false},
		{"gpt responses", "gpt-5.6-luna", "openai-response", originalImageResponses, catalog.RouteResponses, false},
		{"other responses model", "muse-spark", "openai-response", originalImageResponses, catalog.RouteResponses, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := buildUpstreamRequest(tc.route, tc.model, tc.source, []byte(tc.body), nil)
			if err != nil {
				t.Fatal(err)
			}
			// Compare the complete native payload: only image detail and model
			// may change, including exact extension numbers and tool arguments.
			want := tc.body
			if tc.changed {
				want = strings.ReplaceAll(want, `"detail":"original"`, `"detail":"high"`)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal([]byte(want), &doc); err != nil {
				t.Fatal(err)
			}
			doc["model"], _ = json.Marshal(tc.model)
			assertJSONEqual(t, mustJSON(doc), out)
		})
	}
}

func assertJSONEqual(t *testing.T, want, got []byte) {
	t.Helper()
	decode := func(raw []byte) any {
		var v any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if !bytes.Equal(mustJSON(decode(want)), mustJSON(decode(got))) {
		t.Fatalf("want %s\ngot  %s", want, got)
	}
}

func TestExecuteDeepSeekOriginalImageDetail(t *testing.T) {
	for _, route := range []string{"chat", "responses"} {
		for _, source := range []string{"openai", "openai-response"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", route, source, stream), func(t *testing.T) {
					frames, path, response := chatSSEFrames, "/chat/completions", ccResponseBody
					yaml := testValidYAML
					if route == "responses" {
						frames, path, response = responsesSSEFrames, "/responses", responsesPassthrough
						yaml += "\nroute-overrides:\n  deepseek-v4.1-flash:\n    protocol: responses\n    endpoint: /responses\n"
					}
					responder := upstreamRouter(t, map[string]string{path: response})
					if stream {
						responder = streamResponder(streamScript{upstreamID: "up-image", frames: frames})
					}
					f := &fakeCaller{responder: wrapWithCatalog(`{"data":[{"id":"deepseek-v4.1-flash"}]}`, responder)}
					m := NewManager(NewHostBridge(f.call))
					t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
					registered, err := m.HandleCall("plugin.register", lifecycleRequestBody(yaml))
					if err != nil || !decodeEnv(t, registered).OK {
						t.Fatalf("register: %v %s", err, registered)
					}
					payload := originalImageResponses
					if source == "openai" {
						payload = `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/history.png","detail":"original"}}]},{"role":"user","content":"continue"}]}`
					}
					req := executorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
						Model: "opencode-go/deepseek-v4.1-flash(high)", AuthProvider: ProviderID,
						AuthAttributes: map[string]string{"api_key": testKey}, SourceFormat: source, Format: "openai-response",
						Payload: []byte(payload), OriginalRequest: []byte(`{"input":"before interception"}`), Stream: stream,
					}, StreamID: "down-image"}
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
					body := string(mustJSON(wire))
					if strings.Contains(body, `"detail":"original"`) || !strings.Contains(body, "https://example.com/history.png") || !strings.Contains(body, "continue") {
						t.Fatalf("incompatible or lost history: %s", body)
					}
					assertField(t, wire, "model", "deepseek-v4.1-flash")
					// Same-protocol image controls survive; cross-protocol detail
					// remains subject to the translator's media mapping.
					if route == "chat" && source == "openai" || route == "responses" && source == "openai-response" {
						if !strings.Contains(body, `"detail":"high"`) {
							t.Fatalf("missing compatible image detail: %s", body)
						}
					}
				})
			}
		}
	}
}

// Ensure the compatibility rule leaves existing detail values and non-image
// objects alone, even when they resemble image input inside a tool schema.
func TestDeepSeekImageDetailPreservesOtherFields(t *testing.T) {
	for _, detail := range []string{`"auto"`, `"low"`, `"high"`, `null`, `42`, `"future"`} {
		body := []byte(`{"model":"deepseek-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":` + detail + `}},{"type":"text","text":"original","detail":"original"}]}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"input_image","detail":"original"}}}]}`)
		out, err := buildUpstreamRequest(catalog.RouteChatCompletions, "deepseek-flash", "openai", body, nil)
		if err != nil {
			t.Fatal(err)
		}
		assertJSONEqual(t, body, out)
	}
}
