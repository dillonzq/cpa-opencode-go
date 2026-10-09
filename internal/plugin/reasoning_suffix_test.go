package plugin

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"

	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
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
			// adaptive effort: the suffix must replace both.
			name: "level replaces budget and adaptive effort", model: "opencode-go/minimax-m3(high)", format: "claude",
			body: `{"model":"minimax-m3(high)","max_tokens":65536,"messages":[{"role":"user","content":"hi"}],` +
				`"thinking":{"type":"enabled","budget_tokens":3000},"output_config":{"effort":"low"}}`,
			wantThinking: map[string]any{"type": "enabled", "budget_tokens": float64(24576)}, checkEffort: true,
		},
		{
			name: "none disables thinking", model: "opencode-go/minimax-m3(none)", format: "claude",
			body: `{"model":"minimax-m3(none)","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],` +
				`"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`,
			wantThinking: map[string]any{"type": "disabled"}, checkEffort: true,
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

func TestExecuteThinkingSuffixUnknownModelIs404(t *testing.T) {
	m, _ := newExecManager(t)
	env := mustExecute(t, m, "opencode-go/nope(high)", "openai", []byte(ccRequestBody))
	if env.OK || env.Error == nil || env.Error.Code != string(errclass.ClassInvalidModel) || env.Error.HTTPStatus != http.StatusNotFound {
		t.Fatalf("envelope = %+v", env.Error)
	}
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

func TestApplyReasoningSuffixSeamBranches(t *testing.T) {
	// Without a suffix effort the body stays byte-identical: no suffix must not
	// rewrite a request at all.
	body := []byte(`{"model":"m","reasoning_effort":"low"}`)
	if got, eErr := applyReasoningSuffix(catalog.RouteChatCompletions, body, ""); eErr != nil || string(got) != string(body) {
		t.Fatalf("empty effort = %q, %v", got, eErr)
	}
	// An unknown route has no reasoning control to override.
	if got, eErr := applyReasoningSuffix(catalog.Route("weird"), body, "high"); eErr != nil || string(got) != string(body) {
		t.Fatalf("unknown route = %q, %v", got, eErr)
	}
	// Defensive parse failures are classified translation errors, never a send.
	for _, in := range [][]byte{[]byte("{bad"), []byte("null")} {
		if _, eErr := applyReasoningSuffix(catalog.RouteChatCompletions, in, "high"); eErr == nil || eErr.Class != errclass.ClassTranslation {
			t.Fatalf("malformed body %q = %v", in, eErr)
		}
	}
	// Containers with the wrong JSON shape are replaced or left alone, not fatal.
	if got, eErr := applyReasoningSuffix(catalog.RouteResponses, []byte(`{"reasoning":"auto"}`), "high"); eErr != nil || !strings.Contains(string(got), `"effort":"high"`) {
		t.Fatalf("non-object reasoning = %s, %v", got, eErr)
	}
	if got, eErr := applyReasoningSuffix(catalog.RouteMessages, []byte(`{"output_config":"auto"}`), "none"); eErr != nil || !strings.Contains(string(got), `"type":"disabled"`) {
		t.Fatalf("non-object output_config = %s, %v", got, eErr)
	}
	// Auto drops the control, including an emptied parent object.
	if got, eErr := applyReasoningSuffix(catalog.RouteResponses, []byte(`{"reasoning":{"effort":"low"}}`), "auto"); eErr != nil || strings.Contains(string(got), "reasoning") {
		t.Fatalf("auto reasoning = %s, %v", got, eErr)
	}
	// Dropping the adaptive effort keeps every sibling field.
	got, eErr := applyReasoningSuffix(catalog.RouteMessages,
		[]byte(`{"thinking":{"type":"enabled","budget_tokens":1},"output_config":{"effort":"low","other":1}}`), "high")
	if eErr != nil || strings.Contains(string(got), `"effort"`) || !strings.Contains(string(got), `"other"`) {
		t.Fatalf("messages override = %s, %v", got, eErr)
	}
}

func TestExecuteStreamThinkingSuffix(t *testing.T) {
	m, f := newStreamManager(t, streamScript{
		upstreamID: "up-suffix",
		frames: []string{
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}` + "\n\n",
			"data: [DONE]\n\n",
		},
	})
	resp, err := m.HandleCall("executor.execute_stream",
		execStreamReqBody("opencode-go/glm-5.3(high)", "openai",
			[]byte(`{"model":"glm-5.3(high)","stream":true,"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`), "down-suffix"))
	if err != nil {
		t.Fatalf("execute_stream: %v", err)
	}
	env := decodeEnv(t, resp)
	if !env.OK {
		t.Fatalf("envelope = %+v", env.Error)
	}
	m.bridge.WaitForInFlight(5 * time.Second)
	body := upstreamRequestBody(t, f, pluginabi.MethodHostHTTPDoStream)
	assertField(t, body, "model", "glm-5.3")
	assertField(t, body, "reasoning_effort", "high")
}
