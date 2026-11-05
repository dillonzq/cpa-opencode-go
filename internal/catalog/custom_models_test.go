package catalog

import (
	"context"
	"errors"
	"slices"
	"strings"
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
		{Name: "custom-model", Protocols: map[string]config.ProtocolOverride{"responses": {}}, Metadata: modelmeta.Metadata{Output: modelmeta.Ptr(int64(64000))}},
		{Name: "unknown-model"},
	}
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
	if !custom.UserDefined || custom.PublicID != "opencode-go/glm-custom" || primary(custom) != RouteChatCompletions || custom.ContextLimit != 200000 {
		t.Fatalf("family-routed declaration: %+v", custom)
	}
	custom = findModel(t, m.Models(), "custom-model")
	if !custom.UserDefined || len(custom.Routes) != 1 || custom.Routes[0] != (Target{RouteResponses, "/openai/v1/responses"}) || custom.OutputLimit != 64000 {
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

func TestConfiguredModelRemovalAndProtocolSwitches(t *testing.T) {
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
	cfg.Models = nil
	declare(&cfg, "glm-custom", off("chat-completions"))
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

func TestModelAliasPublishesAliasAndSendsUpstreamName(t *testing.T) {
	for _, c := range []struct {
		prefix bool
		want   string
	}{{true, "opencode-go/MiniMax-M3"}, {false, "MiniMax-M3"}} {
		cfg := testCfg()
		cfg.ModelPrefix.Enabled = c.prefix
		cfg.Models = []config.ModelOverride{{Name: "minimax-m3", Alias: "MiniMax-M3"}}
		fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"minimax-m3"}]}`)}}
		m := New(cfg, fc)
		mustRefresh(t, m)
		rec := findModel(t, m.Models(), "minimax-m3")
		if rec.PublicID != c.want || rec.UserDefined {
			t.Fatalf("prefix %v: record = %+v, want PublicID %q", c.prefix, rec, c.want)
		}
		for _, id := range []string{c.want, "minimax-m3"} {
			if got, ok := m.Lookup(id); !ok || got.UpstreamID != "minimax-m3" {
				t.Errorf("prefix %v: Lookup(%q) = %+v, %v", c.prefix, id, got, ok)
			}
		}
		if _, ok := m.Lookup("opencode-go/minimax-m3"); ok {
			t.Errorf("prefix %v: original public ID still routes after aliasing", c.prefix)
		}
	}
}

func TestModelAliasCollidingWithAnotherModelIsExcluded(t *testing.T) {
	cfg := testCfg()
	cfg.Models = []config.ModelOverride{{Name: "glm-5.2", Alias: "glm-5.3"}}
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"glm-5.3"},{"id":"glm-5.2"}]}`)}}
	m := New(cfg, fc)
	mustRefresh(t, m)
	var got []string
	for _, rec := range m.Models() {
		got = append(got, rec.PublicID)
	}
	if !slices.Equal(got, []string{"opencode-go/glm-5.3"}) {
		t.Fatalf("models = %v", got)
	}
	if u := m.Unsupported(); len(u) != 1 || u[0].UpstreamID != "glm-5.2" || !strings.Contains(u[0].Reason, "collides") {
		t.Fatalf("unsupported = %+v", u)
	}
	if rec, ok := m.Lookup("opencode-go/glm-5.3"); !ok || rec.UpstreamID != "glm-5.3" {
		t.Fatalf("contested key resolves to %+v (%v)", rec, ok)
	}
}
