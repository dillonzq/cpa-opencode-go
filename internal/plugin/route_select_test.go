package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
)

const (
	geminiRequestBody  = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	geminiResponseBody = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3},"modelVersion":"gemini-4-argon-contributor"}`
	geminiURL          = "https://opencode.ai/inference/go/google/v1beta/models/gemini-4-argon-contributor"
)

var geminiSSEFrames = []string{
	`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]},"index":0}],"modelVersion":"gemini-4-argon-contributor"}` + "\n\n",
	`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3},"modelVersion":"gemini-4-argon-contributor"}` + "\n\n",
}

func TestSelectRoutePrefersNativeThenLeastLoss(t *testing.T) {
	record := func(routes ...catalog.Route) catalog.ModelRecord {
		rec := catalog.ModelRecord{}
		for _, r := range routes {
			rec.Routes = append(rec.Routes, catalog.Target{Protocol: r, EndpointPath: r.EndpointPath()})
		}
		return rec
	}
	chat, messages, responses, gemini := catalog.RouteChatCompletions, catalog.RouteMessages, catalog.RouteResponses, catalog.RouteGemini
	deepseek := record(chat, responses, messages)
	cases := []struct {
		rec    catalog.ModelRecord
		format string
		want   catalog.Route
	}{
		// Same protocol first.
		{deepseek, "openai", chat},
		{deepseek, "openai-response", responses},
		{deepseek, "claude", messages},
		{record(gemini), "gemini", gemini},
		// Otherwise the static least-loss order for the client format.
		{deepseek, "gemini", responses},
		{record(chat, messages), "gemini", chat},
		{record(messages, gemini), "openai", messages},
		{record(messages, chat), "openai-response", chat},
		{record(chat, responses, gemini), "claude", responses},
		{record(chat, gemini), "claude", chat},
		{record(gemini, messages), "openai-response", messages},
		{record(gemini), "claude", gemini},
		// Unknown formats keep the primary route; conversion reports them.
		{record(messages, chat), "codex", messages},
	}
	for _, c := range cases {
		if got := selectRoute(c.rec, c.format).Protocol; got != c.want {
			t.Errorf("selectRoute(%v, %s) = %s, want %s", c.rec.Routes, c.format, got, c.want)
		}
	}
	for format, order := range routePreference {
		if len(order) != 4 || order[0] != map[string]catalog.Route{"openai": chat, "openai-response": responses, "claude": messages, "gemini": gemini}[format] {
			t.Errorf("%s preference %v must list every protocol, its own first", format, order)
		}
	}
}

// DeepSeek serves Chat, Responses, and Messages, so every client format
// reaches its own upstream protocol without conversion.
func TestExecuteDeepSeekServesEachSourceNatively(t *testing.T) {
	cases := []struct {
		format, body, path, response, marker string
	}{
		{"openai", ccRequestBody, "/v1/chat/completions", ccResponseBody, `"finish_reason":"stop"`},
		{"claude", claudeRequestBody, "/v1/messages", claudeResponseBody, `"stop_reason":"end_turn"`},
		{"openai-response", `{"model":"x","input":"hi"}`, "/v1/responses", responsesPassthrough, `"object":"response"`},
	}
	for _, c := range cases {
		t.Run(c.format, func(t *testing.T) {
			f := &fakeCaller{}
			m := NewManager(NewHostBridge(f.call))
			t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
			f.responder = wrapWithCatalog(`{"data":[{"id":"deepseek-v4-pro"}]}`, upstreamRouter(t, map[string]string{c.path: c.response}))
			if _, err := m.HandleCall("plugin.register", lifecycleRequestBody(testValidYAML)); err != nil {
				t.Fatalf("register: %v", err)
			}
			env := mustExecute(t, m, "opencode-go/deepseek-v4-pro", c.format, []byte(c.body))
			if !env.OK {
				t.Fatalf("execute: %+v", env.Error)
			}
			var out pluginapi.ExecutorResponse
			if err := json.Unmarshal(env.Result, &out); err != nil || !strings.Contains(string(out.Payload), c.marker) {
				t.Fatalf("payload = %s (%v), want native %s", out.Payload, err, c.marker)
			}
			wire := lastWire(t, f, pluginabi.MethodHostHTTPDo)
			if wire["url"] != "https://opencode.ai/inference/go"+c.path {
				t.Fatalf("url = %v, want native %s", wire["url"], c.path)
			}
			assertField(t, upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo), "model", "deepseek-v4-pro")
		})
	}
}

func newGeminiManager(t *testing.T, catalogBody string, next func(string, []byte) ([]byte, error)) (*Manager, *fakeCaller) {
	t.Helper()
	f := &fakeCaller{responder: wrapWithCatalog(catalogBody, next)}
	m := NewManager(NewHostBridge(f.call))
	t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
	if _, err := m.HandleCall("plugin.register", lifecycleRequestBody(testValidYAML)); err != nil {
		t.Fatalf("register: %v", err)
	}
	return m, f
}

func assertGeminiHeaders(t *testing.T, wire map[string]any) {
	t.Helper()
	headers := wire["headers"].(map[string]any)
	if got := headers["X-Goog-Api-Key"]; got == nil || got.([]any)[0] != testKey {
		t.Fatalf("x-goog-api-key = %v; headers = %v", got, headers)
	}
	if headers["Authorization"] != nil || headers["X-Api-Key"] != nil {
		t.Fatalf("Gemini requests carry only the Gemini key header: %v", headers)
	}
	if headers["X-Opencode-Session"] == nil {
		t.Fatalf("session header missing: %v", headers)
	}
}

func TestExecuteChatClientOnGeminiModel(t *testing.T) {
	const catalogBody = `{"data":[{"id":"gemini-4-argon-contributor"}]}`
	t.Run("non-stream", func(t *testing.T) {
		m, f := newGeminiManager(t, catalogBody, upstreamRouter(t, map[string]string{":generateContent": geminiResponseBody}))
		env := mustExecute(t, m, "opencode-go/gemini-4-argon-contributor", "openai", []byte(ccRequestBody))
		if !env.OK {
			t.Fatalf("execute: %+v", env.Error)
		}
		var out pluginapi.ExecutorResponse
		if err := json.Unmarshal(env.Result, &out); err != nil || !strings.Contains(string(out.Payload), `"finish_reason":"stop"`) || !strings.Contains(string(out.Payload), "hello") {
			t.Fatalf("payload not converted to Chat: %s (%v)", out.Payload, err)
		}
		wire := lastWire(t, f, pluginabi.MethodHostHTTPDo)
		if wire["url"] != geminiURL+":generateContent" {
			t.Fatalf("url = %v", wire["url"])
		}
		assertGeminiHeaders(t, wire)
		body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
		if _, ok := body["contents"]; !ok || body["messages"] != nil {
			t.Fatalf("upstream body is not GenerateContent: %v", body)
		}
	})
	t.Run("thinking suffix", func(t *testing.T) {
		m, f := newGeminiManager(t, catalogBody, upstreamRouter(t, map[string]string{":generateContent": geminiResponseBody}))
		env := mustExecute(t, m, "opencode-go/gemini-4-argon-contributor(high)", "openai", []byte(ccRequestBody))
		if !env.OK {
			t.Fatalf("execute: %+v", env.Error)
		}
		if wire := lastWire(t, f, pluginabi.MethodHostHTTPDo); wire["url"] != geminiURL+":generateContent" {
			t.Fatalf("suffix must not reach the URL: %v", wire["url"])
		}
		body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
		config, _ := body["generationConfig"].(map[string]any)
		if config == nil || config["thinkingConfig"] == nil {
			t.Fatalf("suffix not rendered as Gemini thinking: %v", body)
		}
	})
	t.Run("stream", func(t *testing.T) {
		m, f := newGeminiManager(t, catalogBody, streamResponder(streamScript{upstreamID: "up-gemini", frames: geminiSSEFrames}))
		raw, err := m.HandleCall(pluginabi.MethodExecutorExecuteStream, execStreamReqBody("opencode-go/gemini-4-argon-contributor", "openai", []byte(`{"model":"x","stream":true,"messages":[{"role":"user","content":"hi"}]}`), "down-gemini"))
		if err != nil || !decodeEnv(t, raw).OK {
			t.Fatalf("execute_stream: %v %s", err, raw)
		}
		if !m.bridge.WaitForInFlight(5 * time.Second) {
			t.Fatal("stream did not drain")
		}
		assertCleanStreamClose(t, f)
		wire := lastWire(t, f, pluginabi.MethodHostHTTPDoStream)
		if wire["url"] != geminiURL+":streamGenerateContent?alt=sse" {
			t.Fatalf("url = %v", wire["url"])
		}
		assertGeminiHeaders(t, wire)
		events := strings.Join(emittedEvents(t, f), "\n")
		if !strings.Contains(events, `"content":"Hel"`) || !strings.Contains(events, `"finish_reason":"stop"`) || strings.Contains(events, "data:") {
			t.Fatalf("Chat events = %s", events)
		}
	})
}

func TestExecuteGeminiClient(t *testing.T) {
	t.Run("native passthrough", func(t *testing.T) {
		m, f := newGeminiManager(t, `{"data":[{"id":"gemini-4-argon-contributor"}]}`, upstreamRouter(t, map[string]string{":generateContent": geminiResponseBody}))
		env := mustExecute(t, m, "opencode-go/gemini-4-argon-contributor", "gemini", []byte(geminiRequestBody))
		if !env.OK {
			t.Fatalf("execute: %+v", env.Error)
		}
		var out pluginapi.ExecutorResponse
		if err := json.Unmarshal(env.Result, &out); err != nil || !strings.Contains(string(out.Payload), `"finishReason":"STOP"`) {
			t.Fatalf("payload = %s (%v), want native Gemini", out.Payload, err)
		}
		wire := lastWire(t, f, pluginabi.MethodHostHTTPDo)
		if wire["url"] != geminiURL+":generateContent" {
			t.Fatalf("url = %v", wire["url"])
		}
		assertGeminiHeaders(t, wire)
		body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
		if fmt.Sprint(body["contents"]) != "[map[parts:[map[text:hi]] role:user]]" {
			t.Fatalf("native body changed: %v", body)
		}
	})
	t.Run("stream to a Chat model", func(t *testing.T) {
		m, f := newGeminiManager(t, `{"data":[{"id":"glm-5.3"}]}`, streamResponder(streamScript{upstreamID: "up-chat", frames: chatSSEFrames}))
		raw, err := m.HandleCall(pluginabi.MethodExecutorExecuteStream, execStreamReqBody("opencode-go/glm-5.3", "gemini", []byte(geminiRequestBody), "down-chat"))
		if err != nil || !decodeEnv(t, raw).OK {
			t.Fatalf("execute_stream: %v %s", err, raw)
		}
		if !m.bridge.WaitForInFlight(5 * time.Second) {
			t.Fatal("stream did not drain")
		}
		assertCleanStreamClose(t, f)
		wire := lastWire(t, f, pluginabi.MethodHostHTTPDoStream)
		if wire["url"] != "https://opencode.ai/inference/go/v1/chat/completions" {
			t.Fatalf("url = %v", wire["url"])
		}
		body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDoStream)
		assertField(t, body, "stream", true)
		assertField(t, body, "model", "glm-5.3")
		events := emittedEvents(t, f)
		if len(events) == 0 {
			t.Fatal("no Gemini events emitted")
		}
		for _, event := range events {
			// CPA frames Gemini SSE itself: each event is one JSON object.
			var chunk map[string]any
			if err := json.Unmarshal([]byte(event), &chunk); err != nil || chunk["candidates"] == nil {
				t.Fatalf("event %q is not a bare Gemini chunk: %v", event, err)
			}
		}
		if joined := strings.Join(events, "\n"); !strings.Contains(joined, `"text":"Hel"`) || !strings.Contains(joined, `"finishReason":"STOP"`) {
			t.Fatalf("Gemini events = %s", joined)
		}
	})
	t.Run("non-stream to a Chat model", func(t *testing.T) {
		m, f := newGeminiManager(t, `{"data":[{"id":"glm-5.3"}]}`, upstreamRouter(t, map[string]string{"/v1/chat/completions": ccResponseBody}))
		env := mustExecute(t, m, "opencode-go/glm-5.3", "gemini", []byte(geminiRequestBody))
		if !env.OK {
			t.Fatalf("execute: %+v", env.Error)
		}
		var out pluginapi.ExecutorResponse
		if err := json.Unmarshal(env.Result, &out); err != nil || !strings.Contains(string(out.Payload), `"candidates"`) || !strings.Contains(string(out.Payload), "hello") {
			t.Fatalf("payload not converted to Gemini: %s (%v)", out.Payload, err)
		}
		if stream, ok := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)["stream"]; ok && stream != false {
			t.Fatalf("non-stream request sent stream = %v", stream)
		}
	})
}

func TestUpstreamAuthHeadersPerProtocol(t *testing.T) {
	cases := map[catalog.Route]map[string]string{
		catalog.RouteChatCompletions: {"Authorization": "Bearer k"},
		catalog.RouteResponses:       {"Authorization": "Bearer k"},
		catalog.RouteMessages:        {"X-Api-Key": "k", "Anthropic-Version": "2023-06-01"},
		catalog.RouteGemini:          {"X-Goog-Api-Key": "k"},
	}
	for route, want := range cases {
		got := upstreamAuthHeaders(route, "k", "s")
		if len(got) != len(want)+1 || got.Get("X-Opencode-Session") != "s" {
			t.Errorf("%s headers = %v", route, got)
		}
		for name, value := range want {
			if got.Get(name) != value {
				t.Errorf("%s %s = %q, want %q", route, name, got.Get(name), value)
			}
		}
	}
}
