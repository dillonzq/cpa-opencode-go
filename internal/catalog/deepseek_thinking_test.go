package catalog

import (
	"context"
	"reflect"
	"testing"

	"github.com/dillonzq/cpa-opencode-go/internal/config"
	"github.com/dillonzq/cpa-opencode-go/internal/modelmeta"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestDeepSeekThinkingCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, model, catalog, fallback string
		override                       *modelmeta.Thinking
		want                           *pluginapi.ThinkingSupport
	}{
		{"flash fallback", "deepseek-v4.1-flash", "", `{"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high","max"]}]}`, nil,
			&pluginapi.ThinkingSupport{ZeroAllowed: true, Levels: []string{"none", "low", "high", "max"}}},
		{"pro fallback", "deepseek-v4-pro", "", `{"reasoning":true,"reasoning_options":[{"type":"effort","values":["high","max"]}]}`, nil,
			&pluginapi.ThinkingSupport{ZeroAllowed: true, Levels: []string{"none", "high", "max"}}},
		{"catalog levels win", "DeepSeek-test", `,"supported_reasoning_levels":[{"effort":"high"}]`, `{"reasoning_options":[{"type":"effort","values":["low","max"]}]}`, nil,
			&pluginapi.ThinkingSupport{ZeroAllowed: true, Levels: []string{"none", "high"}}},
		{"none not duplicated", "deepseek-test", `,"supported_reasoning_levels":[{"effort":"high"},{"effort":"none"}]`, `{}`, nil,
			&pluginapi.ThinkingSupport{ZeroAllowed: true, Levels: []string{"high", "none"}}},
		{"unknown levels stay unknown", "deepseek-test", "", `{}`, nil,
			&pluginapi.ThinkingSupport{ZeroAllowed: true}},
		{"empty levels stay empty", "deepseek-test", `,"supported_reasoning_levels":[]`, `{}`, nil,
			&pluginapi.ThinkingSupport{ZeroAllowed: true, Levels: []string{}}},
		{"other family", "glm-test", `,"supported_reasoning_levels":[{"effort":"high"}]`, `{}`, nil,
			&pluginapi.ThinkingSupport{Levels: []string{"high"}}},
		{"user disables capability", "deepseek-test", `,"supported_reasoning_levels":[{"effort":"high"}]`, `{}`, &modelmeta.Thinking{ZeroAllowed: modelmeta.Ptr(false)},
			&pluginapi.ThinkingSupport{Levels: []string{"high"}}},
		{"user levels win", "deepseek-test", `,"supported_reasoning_levels":[{"effort":"high"}]`, `{}`, &modelmeta.Thinking{Levels: []string{"low"}},
			&pluginapi.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low"}}},
		{"user empty levels win", "deepseek-test", `,"supported_reasoning_levels":[{"effort":"high"}]`, `{}`, &modelmeta.Thinking{Levels: []string{}},
			&pluginapi.ThinkingSupport{ZeroAllowed: true, Levels: []string{}}},
		{"budget metadata preserved", "deepseek-test", "", `{"reasoning_options":[{"type":"budget_tokens","min":1024,"max":8192}]}`, nil,
			&pluginapi.ThinkingSupport{ZeroAllowed: true, Min: 1024, Max: 8192}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := metadataConfig()
			if tc.override != nil {
				cfg.Models = []config.ModelOverride{{Name: tc.model, Metadata: modelmeta.Metadata{Thinking: tc.override}}}
			}
			client := metadataClient(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
				body := `{"data":[{"id":"` + tc.model + `"` + tc.catalog + `}]}`
				if req.URL == cfg.ModelsDev.URL {
					body = `{"opencode-go":{"models":{"` + tc.model + `":` + tc.fallback + `}}}`
				}
				return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}, nil
			})
			m := New(cfg, client)
			for range 2 {
				mustRefresh(t, m)
				if got := findModel(t, m.Models(), tc.model).Thinking; !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("thinking = %+v, want %+v", got, tc.want)
				}
			}
		})
	}
}

func TestDeepSeekThinkingReconfigurePreservesSources(t *testing.T) {
	cfg := metadataConfig()
	cfg.Models = []config.ModelOverride{{Name: "deepseek-test"}}
	m := New(cfg, nil)
	m.fallback = map[string]modelmeta.Metadata{"deepseek-test": {Thinking: &modelmeta.Thinking{Levels: []string{"high"}}}}
	m.swap(nil)
	if got := findModel(t, m.Models(), "deepseek-test"); !got.UserDefined || !got.Thinking.ZeroAllowed || !reflect.DeepEqual(got.Thinking.Levels, []string{"none", "high"}) {
		t.Fatalf("declared DeepSeek capabilities missing: %+v", got)
	}
	// Reconfiguration reuses the cached fallback. The compatibility default must
	// not mutate it, or a later explicit opt-out would retain the added none.
	cfg.Models = []config.ModelOverride{{Name: "deepseek-test", Metadata: modelmeta.Metadata{Thinking: &modelmeta.Thinking{ZeroAllowed: modelmeta.Ptr(false)}}}}
	reconfigured := New(cfg, nil)
	reconfigured.SeedFallbackFrom(m)
	reconfigured.SeedFrom(m)
	got := findModel(t, reconfigured.Models(), "deepseek-test").Thinking
	if got.ZeroAllowed || !reflect.DeepEqual(got.Levels, []string{"high"}) {
		t.Fatalf("default mutated cached metadata or overrode user config: %+v", got)
	}
}
