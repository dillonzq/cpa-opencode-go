package catalog

import (
	"errors"
	"testing"

	"github.com/dillonzq/cpa-opencode-go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestExcludedModelPatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		id      string
		want    bool
	}{
		{"glm-5.2", "glm-5.2", true},
		{"glm-5.2", "glm-5.2-plus", false},
		{" GLM-5.2 ", "glm-5.2", true},
		{"glm-*", "glm-5.2", true},
		{"glm-*", "glm-", true},
		{"glm-*", "other-glm-5.2", false},
		{"*-preview", "gpt-preview", true},
		{"*-preview", "gpt-preview-plus", false},
		{"*luna*", "gpt-luna-preview", true},
		{"gpt-*-preview*", "gpt-5-preview-mini", true},
		{"gpt-*-preview*", "gpt-preview-mini", false},
		{"a*b*c", "axbyc", true},
		{"a*b*c", "acb", false},
		{"ab*bc", "abc", false},
		{"**", "provider/model", true},
		{"*", "anything", true},
		{"glm*", "glm/provider/model", true},
		{"glm-?.[x]", "glm-5.x", false},
		{"glm-?.[x]", "glm-?.[x]", true},
		{"", "glm-5.2", false},
	} {
		t.Run(tc.pattern+"/"+tc.id, func(t *testing.T) {
			if got := excludedModel(tc.id, []string{tc.pattern}); got != tc.want {
				t.Fatalf("excludedModel(%q, %q) = %v, want %v", tc.id, tc.pattern, got, tc.want)
			}
		})
	}
	if excludedModel("glm-5.2", nil) || !excludedModel("glm-5.2", []string{"gpt-*", "glm-*"}) {
		t.Fatal("empty list or multiple patterns matched incorrectly")
	}
}

func TestExcludedModelsFilterAllSources(t *testing.T) {
	for _, prefix := range []bool{true, false} {
		cfg := testCfg()
		cfg.ModelPrefix = config.ModelPrefix{Enabled: prefix, Value: "custom"}
		cfg.ExcludedModels = []string{"glm-blocked", "qwen-*", "unknown-*"}
		cfg.Models = []config.ModelOverride{{Name: "qwen-custom"}, {Name: "glm-allowed"}}
		cfg.RouteOverrides = map[string]config.RouteOverride{
			"glm-blocked": {Protocol: "responses", Endpoint: "/v1/responses"},
		}
		fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"glm-blocked"},{"id":"qwen-remote"},{"id":"glm-allowed"},{"id":"unknown-model"}]}`)}}
		m := New(cfg, fc)
		mustRefresh(t, m)
		if got := m.Models(); len(got) != 1 || got[0].UpstreamID != "glm-allowed" {
			t.Fatalf("excluded models published: %+v", got)
		}
		for _, id := range []string{"glm-blocked", "qwen-remote", "qwen-custom", "unknown-model"} {
			for _, lookup := range []string{id, config.PublicID(cfg, id)} {
				if _, ok := m.Lookup(lookup); ok {
					t.Fatalf("excluded ID %q remains executable", lookup)
				}
			}
		}
		if len(m.Unsupported()) != 4 {
			t.Fatalf("missing exclusion diagnostics: %+v", m.Unsupported())
		}
		for _, unsupported := range m.Unsupported() {
			if unsupported.Reason != "excluded by excluded-models config" {
				t.Fatalf("wrong exclusion reason: %+v", unsupported)
			}
		}
		fc.set(pluginapi.HTTPResponse{}, errors.New("offline"))
		if err := m.Refresh(t.Context(), testKey); err == nil || len(m.Models()) != 1 {
			t.Fatal("stale refresh bypassed exclusions")
		}
	}
}

func TestExcludedModelsReconfigureStaleSnapshot(t *testing.T) {
	cfg := testCfg()
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"glm-5.2"}]}`)}}
	previous := New(cfg, fc)
	mustRefresh(t, previous)
	cfg.ExcludedModels = []string{"*"}
	blocked := New(cfg, fc)
	blocked.SeedFrom(previous)
	if len(blocked.Models()) != 0 {
		t.Fatal("new exclusion did not apply to seeded snapshot")
	}
	cfg.ExcludedModels = nil
	restored := New(cfg, fc)
	restored.SeedFrom(blocked)
	if _, ok := restored.Lookup("opencode-go/glm-5.2"); !ok {
		t.Fatal("removing exclusion did not restore retained upstream model")
	}
}
