package plugin

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// TestExcludedModelsDiscoveryAndExecution checks both discovery methods and
// rejects excluded execution IDs before dispatch while keeping allowed models usable.
func TestExcludedModelsDiscoveryAndExecution(t *testing.T) {
	for _, prefix := range []bool{true, false} {
		t.Run(map[bool]string{true: "prefixed", false: "bare"}[prefix], func(t *testing.T) {
			m, f, _, yamlText := newIntegrationManager(t)
			if !prefix {
				yamlText += "model-prefix:\n  enabled: false\n"
			}
			yamlText += "excluded-models: ['glm-*', 'gpt-5.6-luna']\nmodels:\n  - name: glm-declared\n"
			resp := mustHandle(t, m, "plugin.reconfigure", lifecycleRequestBody(yamlText))
			if env := decodeEnv(t, resp); !env.OK {
				t.Fatalf("reconfigure: %s", resp)
			}
			allowed := "qwen3.7-max"
			if prefix {
				allowed = "opencode-go/" + allowed
			}
			for _, method := range []string{"model.static", "model.for_auth"} {
				var result pluginapi.ModelResponse
				decodeResult(t, mustHandle(t, m, method, nil), &result)
				if len(result.Models) != 1 || result.Models[0].ID != allowed {
					t.Fatalf("%s published excluded model: %+v", method, result.Models)
				}
			}
			before := len(f.callsOf("host.http.do"))
			for _, id := range []string{"glm-5.2", "glm-declared", "gpt-5.6-luna", "opencode-go/glm-5.2", "opencode-go/gpt-5.6-luna", "opencode-go/glm-5.2(high)"} {
				for _, stream := range []bool{false, true} {
					method := "executor.execute"
					if stream {
						method = "executor.execute_stream"
					}
					env := decodeEnv(t, mustHandle(t, m, method, execReqBodyWithKey(id, "openai", []byte(`{"messages":[]}`), stream, "sk-test-1")))
					if env.OK || env.Error == nil || env.Error.Code != "invalid_model" || env.Error.HTTPStatus != http.StatusNotFound {
						t.Fatalf("%s %s: %+v", method, id, env)
					}
				}
			}
			if after := len(f.callsOf("host.http.do")); after != before {
				t.Fatal("excluded execution reached upstream")
			}
			env := mustExecute(t, m, allowed, "claude", []byte(`{"max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`))
			if !env.OK {
				t.Fatalf("allowed execution failed: %+v", env.Error)
			}
		})
	}
}

// TestExcludedModelsReconfigureDuringOutage exercises blocking every cached
// model and restoring them through lifecycle reconfiguration while upstream is offline.
func TestExcludedModelsReconfigureDuringOutage(t *testing.T) {
	m, _, st, yamlText := newIntegrationManager(t)
	st.setCatalogBody("{invalid")
	for _, exclusions := range []string{"excluded-models: ['*']\n", ""} {
		resp := mustHandle(t, m, "plugin.reconfigure", lifecycleRequestBody(yamlText+exclusions))
		if env := decodeEnv(t, resp); !env.OK {
			t.Fatalf("reconfigure during outage: %s", resp)
		}
		want := 3
		if exclusions != "" {
			want = 0
		}
		if got := len(integrationStaticIDs(t, m)); got != want {
			t.Fatalf("model count during outage = %d, want %d", got, want)
		}
	}
}
