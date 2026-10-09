package plugin

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
	"github.com/dillonzq/cpa-opencode-go/internal/thinking"
)

// upstreamRequestBody decodes the JSON body of the last bridged upstream call.
func upstreamRequestBody(t *testing.T, f *fakeCaller, method string) map[string]any {
	t.Helper()
	calls := f.callsOf(method)
	if len(calls) == 0 {
		t.Fatalf("no %s calls recorded", method)
	}
	wire := decodePayload(t, calls[len(calls)-1])
	var body map[string]any
	if err := json.Unmarshal(wireBody(t, wire, "body"), &body); err != nil {
		t.Fatalf("upstream body decode: %v", err)
	}
	return body
}

func assertField(t *testing.T, body map[string]any, key string, want any) {
	t.Helper()
	got, ok := body[key]
	if want == nil {
		if ok {
			t.Fatalf("%s = %v, want absent; body=%v", key, got, body)
		}
		return
	}
	if !ok || got != want {
		t.Fatalf("%s = %v (present=%t), want %v; body=%v", key, got, ok, want, body)
	}
}

// testSuffix parses a model name the way a client would write it, so the cases
// below exercise the same parser the executor uses.
func testSuffix(model string) thinking.Suffix {
	return thinking.ParseSuffix(model)
}

func TestExecuteChatRouteThinkingSuffix(t *testing.T) {
	// CPA parses model(value) for its own executors only, so the plugin must
	// both strip the suffix for routing AND apply the reasoning control
	// itself, with the suffix winning over the body's own control.
	for _, tc := range []struct {
		name  string
		model string
		body  string
		want  any // nil means no reasoning_effort in the upstream body
	}{
		{"level overrides body control", "opencode-go/glm-5.3(high)",
			`{"model":"glm-5.3(high)","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`, "high"},
		{"level is case-insensitive", "opencode-go/glm-5.3(HIGH)",
			`{"model":"glm-5.3(HIGH)","messages":[{"role":"user","content":"hi"}]}`, "high"},
		{"bare upstream id", "glm-5.3(xhigh)",
			`{"model":"glm-5.3(xhigh)","messages":[{"role":"user","content":"hi"}]}`, "xhigh"},
		{"numeric budget uses the fixed thresholds", "opencode-go/glm-5.3(1024)",
			`{"model":"glm-5.3(1024)","messages":[{"role":"user","content":"hi"}]}`, "low"},
		{"none disables reasoning", "opencode-go/glm-5.3(none)",
			`{"model":"glm-5.3(none)","messages":[{"role":"user","content":"hi"}]}`, "none"},
		{"zero budget disables reasoning", "opencode-go/glm-5.3(0)",
			`{"model":"glm-5.3(0)","messages":[{"role":"user","content":"hi"}]}`, "none"},
		{"auto removes the body control", "opencode-go/glm-5.3(auto)",
			`{"model":"glm-5.3(auto)","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`, nil},
		{"minus one is auto", "opencode-go/glm-5.3(-1)",
			`{"model":"glm-5.3(-1)","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`, nil},
		{"unrecognized value strips and keeps the body control", "opencode-go/glm-5.3(turbo)",
			`{"model":"glm-5.3(turbo)","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`, "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := newExecManager(t)
			env := mustExecute(t, m, tc.model, "openai", []byte(tc.body))
			if !env.OK {
				t.Fatalf("envelope = %+v", env.Error)
			}
			body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
			assertField(t, body, "model", "glm-5.3")
			assertField(t, body, "reasoning_effort", tc.want)
		})
	}
}

func TestExecuteMessagesRouteThinkingSuffix(t *testing.T) {
	for _, tc := range []struct {
		name         string
		model        string
		format       string
		body         string
		wantThinking map[string]any // nil means absent
		checkEffort  bool           // assert output_config.effort is gone
	}{
		{
			// The claude source carries both an explicit budget and an
			// adaptive effort: the suffix must replace both, and the
			// display preference survives the enabled override.
			name: "level replaces budget and adaptive effort", model: "opencode-go/minimax-m3(high)", format: "claude",
			body: `{"model":"minimax-m3(high)","max_tokens":65536,"messages":[{"role":"user","content":"hi"}],` +
				`"thinking":{"type":"enabled","budget_tokens":3000,"display":"omitted"},"output_config":{"effort":"low"}}`,
			wantThinking: map[string]any{"type": "enabled", "budget_tokens": float64(24576), "display": "omitted"},
			checkEffort:  true,
		},
		{
			// A numeric suffix keeps its exact budget (CPA ModeBudget)
			// instead of re-deriving one from the level thresholds.
			name: "numeric suffix keeps its exact budget", model: "opencode-go/minimax-m3(16384)", format: "claude",
			body:         `{"model":"minimax-m3(16384)","max_tokens":65536,"messages":[{"role":"user","content":"hi"}]}`,
			wantThinking: map[string]any{"type": "enabled", "budget_tokens": float64(16384)},
		},
		{
			// CPA drops display when thinking is disabled: display only
			// applies to an active block.
			name: "none disables thinking and drops display", model: "opencode-go/minimax-m3(none)", format: "claude",
			body: `{"model":"minimax-m3(none)","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],` +
				`"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"high"}}`,
			wantThinking: map[string]any{"type": "disabled"},
			checkEffort:  true,
		},
		{
			name: "auto removes the control", model: "opencode-go/minimax-m3(auto)", format: "claude",
			body: `{"model":"minimax-m3(auto)","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],` +
				`"thinking":{"type":"enabled","budget_tokens":3000}}`,
			checkEffort: true,
		},
		{
			name: "unrecognized value leaves the control", model: "opencode-go/minimax-m3(turbo)", format: "claude",
			body: `{"model":"minimax-m3(turbo)","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],` +
				`"thinking":{"type":"enabled","budget_tokens":3000}}`,
			wantThinking: map[string]any{"type": "enabled", "budget_tokens": float64(3000)},
		},
		{
			name: "cross-protocol level reaches the budget table", model: "opencode-go/minimax-m3(high)", format: "openai",
			body:         `{"model":"minimax-m3(high)","max_tokens":65536,"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`,
			wantThinking: map[string]any{"type": "enabled", "budget_tokens": float64(24576)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := newExecManager(t)
			env := mustExecute(t, m, tc.model, tc.format, []byte(tc.body))
			if !env.OK {
				t.Fatalf("envelope = %+v", env.Error)
			}
			body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
			assertField(t, body, "model", "minimax-m3")
			got, _ := body["thinking"].(map[string]any)
			if tc.wantThinking == nil {
				assertField(t, body, "thinking", nil)
			} else if !reflect.DeepEqual(got, tc.wantThinking) {
				t.Fatalf("thinking = %v, want %v", got, tc.wantThinking)
			}
			if tc.checkEffort {
				if oc, ok := body["output_config"].(map[string]any); ok {
					if effort, has := oc["effort"]; has {
						t.Fatalf("output_config.effort survived the suffix override: %v", effort)
					}
				}
			}
		})
	}
}

func TestExecuteResponsesRouteThinkingSuffix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		model       string
		format      string
		body        string
		want        any // nil means no reasoning.effort in the upstream body
		wantSummary any
	}{
		{
			name: "native responses keeps sibling reasoning fields", model: "opencode-go/gpt-5.6-luna(medium)", format: "openai-response",
			body: `{"model":"gpt-5.6-luna(medium)","input":"hi","reasoning":{"summary":"auto","effort":"low"}}`,
			want: "medium", wantSummary: "auto",
		},
		{
			name: "auto removes only the effort", model: "opencode-go/gpt-5.6-luna(auto)", format: "openai-response",
			body: `{"model":"gpt-5.6-luna(auto)","input":"hi","reasoning":{"effort":"low"}}`,
		},
		{
			name: "cross-protocol none overrides the body", model: "opencode-go/gpt-5.6-luna(none)", format: "openai",
			body: `{"model":"gpt-5.6-luna(none)","input":"hi","reasoning_effort":"low"}`,
			want: "none",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := newExecManager(t)
			env := mustExecute(t, m, tc.model, tc.format, []byte(tc.body))
			if !env.OK {
				t.Fatalf("envelope = %+v", env.Error)
			}
			body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
			assertField(t, body, "model", "gpt-5.6-luna")
			reasoning, _ := body["reasoning"].(map[string]any)
			if tc.want == nil {
				if effort, has := reasoning["effort"]; has {
					t.Fatalf("reasoning.effort survived the suffix override: %v", effort)
				}
				return
			}
			if reasoning["effort"] != tc.want {
				t.Fatalf("reasoning.effort = %v, want %v; body=%v", reasoning["effort"], tc.want, body)
			}
			if tc.wantSummary != nil && reasoning["summary"] != tc.wantSummary {
				t.Fatalf("reasoning.summary = %v, want %v", reasoning["summary"], tc.wantSummary)
			}
		})
	}
}

// TestExecuteThinkingSuffixSupersedesBodyControl covers the conversion that
// reads the client's reasoning control before the override runs: an
// unrepresentable value must not fail a request whose suffix already replaced
// it, and the same request without a suffix must still fail.
func TestExecuteThinkingSuffixSupersedesBodyControl(t *testing.T) {
	const messagesRoute = "opencode-go/minimax-m3"
	const chatRoute = "opencode-go/glm-5.3"
	const responsesRoute = "opencode-go/gpt-5.6-luna"
	for _, tc := range []struct {
		name      string
		model     string
		upstream  string
		format    string
		body      string
		errorOnly bool   // without the suffix the request must fail
		wantPath  string // dotted path of the overridden control
		want      any
	}{
		{
			name: "openai effort with no budget equivalent", model: messagesRoute, upstream: "minimax-m3", format: "openai",
			body:      `{"model":"m","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"turbo"}`,
			errorOnly: true, wantPath: "thinking.budget_tokens", want: float64(24576),
		},
		{
			name: "responses effort with no budget equivalent", model: messagesRoute, upstream: "minimax-m3", format: "openai-response",
			body:      `{"model":"m","input":"hi","reasoning":{"effort":"turbo","summary":"auto"}}`,
			errorOnly: true, wantPath: "thinking.budget_tokens", want: float64(8192),
		},
		{
			name: "claude thinking type with no chat equivalent", model: chatRoute, upstream: "glm-5.3", format: "claude",
			body:      `{"model":"m","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"bogus"}}`,
			errorOnly: true, wantPath: "reasoning_effort", want: "low",
		},
		{
			name: "claude thinking type with no responses equivalent", model: responsesRoute, upstream: "gpt-5.6-luna", format: "claude",
			body:      `{"model":"m","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"bogus"}}`,
			errorOnly: true, wantPath: "reasoning.effort", want: "low",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := newExecManager(t)
			suffixed := tc.model + "(high)"
			if tc.format == "openai-response" {
				suffixed = tc.model + "(medium)"
			}
			if tc.format == "claude" {
				suffixed = tc.model + "(low)"
			}
			env := mustExecute(t, m, suffixed, tc.format, []byte(tc.body))
			if !env.OK {
				t.Fatalf("suffix must supersede the body control, envelope = %+v", env.Error)
			}
			body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
			assertField(t, body, "model", tc.upstream)
			if got := dig(t, body, tc.wantPath); got != tc.want {
				t.Fatalf("%s = %v, want %v; body=%v", tc.wantPath, got, tc.want, body)
			}
			if tc.errorOnly {
				// Baseline: the same body without a suffix keeps failing
				// instead of silently dropping the client's control.
				base, _ := newExecManager(t)
				failed := mustExecute(t, base, tc.model, tc.format, []byte(tc.body))
				if failed.OK || failed.Error == nil || failed.Error.Code != string(errclass.ClassUnsupported) {
					t.Fatalf("baseline without suffix = %+v", failed.Error)
				}
			}
		})
	}
}

// dig resolves a dotted JSON path in a decoded object.
func dig(t *testing.T, body map[string]any, path string) any {
	t.Helper()
	node := any(body)
	for _, key := range strings.Split(path, ".") {
		obj, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = obj[key]
	}
	return node
}

func TestExecuteThinkingSuffixWithPrefixDisabled(t *testing.T) {
	// With the provider prefix off the client sends the bare upstream ID plus
	// the suffix; routing and the override must work identically.
	f := &fakeCaller{responder: wrapWithCatalog(multiRouteCatalog, upstreamRouter(t, map[string]string{
		"/v1/chat/completions": ccResponseBody,
	}))}
	m := NewManager(NewHostBridge(f.call))
	t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
	if _, err := m.HandleCall("plugin.register", lifecycleRequestBody(testValidYAML)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if resp, err := m.HandleCall("plugin.reconfigure",
		lifecycleRequestBody(testValidYAML+"model-prefix:\n  enabled: false\n")); err != nil {
		t.Fatalf("reconfigure: %v", err)
	} else if env := decodeEnv(t, resp); !env.OK {
		t.Fatalf("reconfigure envelope = %+v", env.Error)
	}
	env := mustExecute(t, m, "glm-5.3(high)", "openai", []byte(`{"model":"glm-5.3(high)","messages":[{"role":"user","content":"hi"}]}`))
	if !env.OK {
		t.Fatalf("envelope = %+v", env.Error)
	}
	body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
	assertField(t, body, "model", "glm-5.3")
	assertField(t, body, "reasoning_effort", "high")
}

func TestExecuteThinkingSuffixLiteralIDLosesToBaseModel(t *testing.T) {
	// Suffix priority: when both the base model and a literal parenthesized ID
	// are routable, the base model wins and the suffix is applied.
	const catalogBody = `{"data":[{"id":"glm-5.3"},{"id":"glm-weird(kid)"}]}`
	f := &fakeCaller{responder: wrapWithCatalog(catalogBody, upstreamRouter(t, map[string]string{
		"/v1/chat/completions": ccResponseBody,
	}))}
	m := NewManager(NewHostBridge(f.call))
	t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
	if _, err := m.HandleCall("plugin.register", lifecycleRequestBody(testValidYAML)); err != nil {
		t.Fatalf("register: %v", err)
	}
	env := mustExecute(t, m, "opencode-go/glm-weird(kid)", "openai", []byte(ccRequestBody))
	if !env.OK {
		t.Fatalf("envelope = %+v", env.Error)
	}
	body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
	// The literal ID is routable on its own, but a recognized suffix value
	// takes priority: "kid" is not a suffix value, so the literal ID wins.
	assertField(t, body, "model", "glm-weird(kid)")
	assertField(t, body, "reasoning_effort", nil)

	// A recognized value on the same parenthesized family resolves the base.
	env = mustExecute(t, m, "opencode-go/glm-5.3(low)", "openai", []byte(ccRequestBody))
	if !env.OK {
		t.Fatalf("envelope = %+v", env.Error)
	}
	body = upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
	assertField(t, body, "model", "glm-5.3")
	assertField(t, body, "reasoning_effort", "low")
}

func TestExecuteThinkingSuffixKeepsLiteralCatalogID(t *testing.T) {
	// A catalog ID that literally contains parentheses stays reachable when the
	// stripped name does not resolve; it keeps its identity and no override.
	const catalogBody = `{"data":[{"id":"glm-weird(kid)"}]}`
	f := &fakeCaller{responder: wrapWithCatalog(catalogBody, upstreamRouter(t, map[string]string{
		"/v1/chat/completions": ccResponseBody,
	}))}
	m := NewManager(NewHostBridge(f.call))
	t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
	if _, err := m.HandleCall("plugin.register", lifecycleRequestBody(testValidYAML)); err != nil {
		t.Fatalf("register: %v", err)
	}
	env := mustExecute(t, m, "opencode-go/glm-weird(kid)", "openai", []byte(ccRequestBody))
	if !env.OK {
		t.Fatalf("envelope = %+v", env.Error)
	}
	body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
	assertField(t, body, "model", "glm-weird(kid)")
	assertField(t, body, "reasoning_effort", nil)
}

func TestExecuteThinkingSuffixUsesInterceptorPayload(t *testing.T) {
	// The suffix is read from the requested model while the body comes from the
	// host's effective Payload, so an interceptor rewrite of the body's model
	// field does not change the routing or the override.
	m, f := newExecManager(t)
	req, _ := json.Marshal(executorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		AuthProvider: ProviderID, AuthAttributes: map[string]string{"api_key": testKey},
		Model: "opencode-go/glm-5.3(high)", SourceFormat: "openai", Format: "openai",
		OriginalRequest: []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`),
		Payload:         []byte(`{"model":"glm-5.3(high)","messages":[{"role":"user","content":"rewritten"}],"reasoning_effort":"low"}`),
	}})
	resp, err := m.HandleCall("executor.execute", req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if env := decodeEnv(t, resp); !env.OK {
		t.Fatalf("envelope = %+v", env.Error)
	}
	body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDo)
	assertField(t, body, "model", "glm-5.3")
	assertField(t, body, "reasoning_effort", "high")
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("payload messages = %v", body["messages"])
	}
	if content := msgs[0].(map[string]any)["content"]; content != "rewritten" {
		t.Fatalf("effective payload not used: %v", content)
	}
}

func TestExecuteStreamThinkingSuffix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  string
		format string
		body   string
		path   string
		want   any
	}{
		{
			name: "chat route", model: "opencode-go/glm-5.3(high)", format: "openai",
			body: `{"model":"glm-5.3(high)","stream":true,"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`,
			path: "reasoning_effort", want: "high",
		},
		{
			name: "messages route", model: "opencode-go/minimax-m3(high)", format: "claude",
			body: `{"model":"minimax-m3(high)","stream":true,"max_tokens":65536,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":1000}}`,
			path: "thinking.budget_tokens", want: float64(24576),
		},
		{
			name: "responses route", model: "opencode-go/gpt-5.6-luna(none)", format: "openai-response",
			body: `{"model":"gpt-5.6-luna(none)","stream":true,"input":"hi","reasoning":{"effort":"low"}}`,
			path: "reasoning.effort", want: "none",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := newStreamManager(t, streamScript{
				upstreamID: "up-suffix",
				frames: []string{
					`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}` + "\n\n",
					"data: [DONE]\n\n",
				},
			})
			resp, err := m.HandleCall("executor.execute_stream",
				execStreamReqBody(tc.model, tc.format, []byte(tc.body), "down-suffix"))
			if err != nil {
				t.Fatalf("execute_stream: %v", err)
			}
			env := decodeEnv(t, resp)
			if !env.OK {
				t.Fatalf("envelope = %+v", env.Error)
			}
			m.bridge.WaitForInFlight(5 * time.Second)
			body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDoStream)
			if got := dig(t, body, tc.path); got != tc.want {
				t.Fatalf("%s = %v, want %v; body=%v", tc.path, got, tc.want, body)
			}
		})
	}
}

func TestApplyReasoningSuffixSeamBranches(t *testing.T) {
	// Without a recognized suffix the body stays byte-identical: no suffix must
	// not rewrite a request at all.
	body := []byte(`{"model":"m","reasoning_effort":"low"}`)
	if got, eErr := applyReasoningSuffix(catalog.RouteChatCompletions, body, testSuffix("m")); eErr != nil || string(got) != string(body) {
		t.Fatalf("no suffix = %q, %v", got, eErr)
	}
	if got, eErr := applyReasoningSuffix(catalog.RouteChatCompletions, body, testSuffix("m(turbo)")); eErr != nil || string(got) != string(body) {
		t.Fatalf("unrecognized suffix = %q, %v", got, eErr)
	}
	// An unknown route has no reasoning control to override.
	if got, eErr := applyReasoningSuffix(catalog.Route("weird"), body, testSuffix("m(high)")); eErr != nil || string(got) != string(body) {
		t.Fatalf("unknown route = %q, %v", got, eErr)
	}
	// Defensive parse failures are classified translation errors, never a send.
	for _, in := range [][]byte{[]byte("{bad"), []byte("null")} {
		if _, eErr := applyReasoningSuffix(catalog.RouteChatCompletions, in, testSuffix("m(high)")); eErr == nil || eErr.Class != errclass.ClassTranslation {
			t.Fatalf("malformed body %q = %v", in, eErr)
		}
	}
	// Containers with the wrong JSON shape are replaced or left alone, not fatal.
	if got, eErr := applyReasoningSuffix(catalog.RouteResponses, []byte(`{"reasoning":"auto"}`), testSuffix("m(high)")); eErr != nil || !strings.Contains(string(got), `"effort":"high"`) {
		t.Fatalf("non-object reasoning = %s, %v", got, eErr)
	}
	if got, eErr := applyReasoningSuffix(catalog.RouteMessages, []byte(`{"output_config":"auto"}`), testSuffix("m(none)")); eErr != nil || !strings.Contains(string(got), `"type":"disabled"`) {
		t.Fatalf("non-object output_config = %s, %v", got, eErr)
	}
	// Auto drops the control, including an emptied parent object.
	if got, eErr := applyReasoningSuffix(catalog.RouteResponses, []byte(`{"reasoning":{"effort":"low"}}`), testSuffix("m(auto)")); eErr != nil || strings.Contains(string(got), "reasoning") {
		t.Fatalf("auto reasoning = %s, %v", got, eErr)
	}
	// Dropping the adaptive effort keeps every sibling field.
	got, eErr := applyReasoningSuffix(catalog.RouteMessages,
		[]byte(`{"thinking":{"type":"enabled","budget_tokens":1},"output_config":{"effort":"low","other":1}}`), testSuffix("m(high)"))
	if eErr != nil || strings.Contains(string(got), `"effort"`) || !strings.Contains(string(got), `"other"`) {
		t.Fatalf("messages override = %s, %v", got, eErr)
	}
	// A superseded control is dropped only for the conversions that read it.
	if got := stripSupersededReasoning(catalog.RouteMessages, "claude", body); string(got) != string(body) {
		t.Fatalf("native messages body rewritten: %s", got)
	}
	if got := stripSupersededReasoning(catalog.RouteChatCompletions, "openai", body); string(got) != string(body) {
		t.Fatalf("native chat body rewritten: %s", got)
	}
	if got := stripSupersededReasoning(catalog.RouteMessages, "openai", body); strings.Contains(string(got), "reasoning_effort") {
		t.Fatalf("superseded chat effort kept: %s", got)
	}
	if got := stripSupersededReasoning(catalog.RouteMessages, "openai", []byte("{bad")); string(got) != "{bad" {
		t.Fatalf("malformed body rewritten: %s", got)
	}
}

func TestExecuteThinkingSuffixUnknownModelIs404(t *testing.T) {
	m, _ := newExecManager(t)
	env := mustExecute(t, m, "opencode-go/nope(high)", "openai", []byte(ccRequestBody))
	if env.OK || env.Error == nil || env.Error.Code != string(errclass.ClassInvalidModel) || env.Error.HTTPStatus != http.StatusNotFound {
		t.Fatalf("envelope = %+v", env.Error)
	}
}
