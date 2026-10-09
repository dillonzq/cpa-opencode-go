package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/dillonzq/cpa-opencode-go/internal/config"
	"github.com/dillonzq/cpa-opencode-go/internal/modelmeta"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestConfiguredModelsAddAndOverride(t *testing.T) {
	cfg := testCfg()
	cfg.Models = []config.ModelOverride{
		{Name: "glm-existing", Metadata: modelmeta.Metadata{DisplayName: modelmeta.Ptr("Override")}},
		{Name: "glm-custom", Metadata: modelmeta.Metadata{Context: modelmeta.Ptr(int64(200000))}},
		{Name: "custom-model", Metadata: modelmeta.Metadata{Output: modelmeta.Ptr(int64(64000))}},
		{Name: "unknown-model"},
	}
	cfg.RouteOverrides = map[string]config.RouteOverride{"custom-model": {Protocol: "responses", Endpoint: "/v1/responses"}}
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"glm-existing","display_name":"Catalog"}]}`)}}
	m := New(cfg, fc)
	mustRefresh(t, m)
	if len(m.Models()) != 3 {
		t.Fatalf("models=%+v", m.Models())
	}
	existing := findModel(t, m.Models(), "glm-existing")
	if existing.DisplayName != "Override" || existing.UserDefined {
		t.Fatalf("existing model duplicated or misclassified: %+v", existing)
	}
	custom := findModel(t, m.Models(), "glm-custom")
	if !custom.UserDefined || custom.PublicID != "opencode-go/glm-custom" || custom.Protocol != RouteChatCompletions || custom.ContextLimit != 200000 {
		t.Fatalf("family-routed declaration: %+v", custom)
	}
	custom = findModel(t, m.Models(), "custom-model")
	if !custom.UserDefined || custom.Protocol != RouteResponses || custom.OutputLimit != 64000 {
		t.Fatalf("explicit route: %+v", custom)
	}
	for _, id := range []string{"custom-model", "opencode-go/custom-model"} {
		if _, ok := m.Lookup(id); !ok {
			t.Fatalf("declared model not executable by ID %q", id)
		}
	}
	if len(m.Unsupported()) != 1 || m.Unsupported()[0].UpstreamID != "unknown-model" {
		t.Fatalf("unroutable declaration diagnostic: %+v", m.Unsupported())
	}
}

func TestConfiguredModelsSurviveCatalogEmptyAndFailure(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "fail-closed", true: "stale"}[stale], func(t *testing.T) {
			cfg := testCfg()
			cfg.Catalog.StaleWhileUnavailable = stale
			cfg.Models = []config.ModelOverride{{Name: "glm-custom"}}
			fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"glm-discovered"}]}`)}}
			m := New(cfg, fc)
			if len(m.Models()) != 1 || !m.Models()[0].UserDefined {
				t.Fatal("configured model unavailable before first refresh")
			}
			mustRefresh(t, m)
			fc.set(pluginapi.HTTPResponse{}, errors.New("catalog unavailable"))
			if err := m.Refresh(t.Context(), testKey); err == nil {
				t.Fatal("catalog failure hidden")
			}
			if _, ok := m.Lookup("glm-custom"); !ok {
				t.Fatal("outage removed explicit declaration")
			}
			if _, ok := m.Lookup("glm-discovered"); ok != stale {
				t.Fatal("upstream stale policy violated")
			}
			fc.set(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[]}`)}, nil)
			mustRefresh(t, m)
			if len(m.Models()) != 1 || m.Models()[0].UpstreamID != "glm-custom" {
				t.Fatal("empty remote catalog removed custom model or retained discovered one")
			}
		})
	}
}

func TestConfiguredModelRemovalAndProtocolFlags(t *testing.T) {
	cfg := testCfg()
	cfg.Models = []config.ModelOverride{{Name: "glm-custom"}}
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"glm-discovered"}]}`)}}
	old := New(cfg, fc)
	mustRefresh(t, old)
	cfg.Models = nil
	m := New(cfg, fc)
	m.SeedFrom(old)
	if _, ok := m.Lookup("glm-custom"); ok {
		t.Fatal("removed declaration survived as stale upstream data")
	}
	if _, ok := m.Lookup("glm-discovered"); !ok {
		t.Fatal("genuine discovered snapshot lost")
	}
	cfg.Models = []config.ModelOverride{{Name: "glm-custom"}}
	cfg.Protocols.ChatCompletions = false
	m = New(cfg, fc)
	if len(m.Models()) != 0 || len(m.Unsupported()) != 1 {
		t.Fatal("custom model bypassed protocol disable")
	}
}

func TestConfiguredModelFallbackWithFailedCatalog(t *testing.T) {
	cfg := metadataConfig()
	cfg.Models = []config.ModelOverride{{Name: "glm-extra", Metadata: modelmeta.Metadata{Output: modelmeta.Ptr(int64(64000))}}}
	m := New(cfg, metadataClient(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if req.URL == cfg.CatalogURL {
			return pluginapi.HTTPResponse{}, errors.New("offline")
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(metadataFallback)}, nil
	}))
	if err := m.Refresh(t.Context(), testKey); err == nil {
		t.Fatal("catalog failure hidden")
	}
	model := findModel(t, m.Models(), "glm-extra")
	if !model.UserDefined || model.DisplayName != "Do not publish" || model.OutputLimit != 64000 || len(m.Models()) != 1 {
		t.Fatalf("custom fallback/priority: %+v", model)
	}
}
