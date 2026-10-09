package catalog

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dillonzq/cpa-opencode-go/internal/config"
	"github.com/dillonzq/cpa-opencode-go/internal/modelmeta"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type metadataClient func(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)

func (f metadataClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return f(ctx, req)
}

const metadataCatalog = `{"data":[{"id":"glm-test","display_name":"Catalog","context_window":200,
"supported_reasoning_levels":[{"effort":"none"},{"effort":"high"}]},{"id":"glm-catalog"},{"id":"glm-unknown"}]}`
const metadataFallback = `{"opencode-go":{"models":{
"glm-test":{"name":"Fallback","description":"Fallback description","limit":{"context":100,"output":64},
"modalities":{"input":["text","image"],"output":["text"]},"reasoning":true,
"reasoning_options":[{"type":"effort","values":["low","medium"]}]},
"glm-catalog":{"limit":{"context":300}},"glm-extra":{"name":"Do not publish"}}},
"other":{"models":{"glm-unknown":{"limit":{"context":999}}}}}`

func metadataConfig() config.Config {
	cfg := testCfg()
	cfg.ModelsDev = config.ModelsDev{Enabled: true, URL: "https://metadata.test/api.json", RefreshInterval: time.Hour}
	return cfg
}

func TestMetadataPriorityCacheAndSeed(t *testing.T) {
	cfg := metadataConfig()
	cfg.Models = []config.ModelOverride{{Name: "glm-test", Metadata: modelmeta.Metadata{
		DisplayName: modelmeta.Ptr("User"), Output: modelmeta.Ptr(int64(0)), InputModalities: []string{},
		Thinking: &modelmeta.Thinking{ZeroAllowed: modelmeta.Ptr(false)},
	}}}
	fallbackCalls := 0
	client := metadataClient(func(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		body := metadataCatalog
		if req.URL == cfg.ModelsDev.URL {
			fallbackCalls++
			if req.Headers.Get("Authorization") != "" {
				t.Fatal("fallback leaked catalog credentials")
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > fallbackTimeout {
				t.Fatal("fallback deadline missing")
			}
			body = metadataFallback
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}, nil
	})
	m := New(cfg, client)
	mustRefresh(t, m)
	got := findModel(t, m.Models(), "glm-test")
	if got.DisplayName != "User" || got.ContextLimit != 200 || got.OutputLimit != 0 ||
		got.Description != "Fallback description" || got.InputModes == nil || len(got.InputModes) != 0 ||
		!reflect.DeepEqual(got.OutputModes, []string{"text"}) || got.Thinking == nil || got.Thinking.ZeroAllowed ||
		!reflect.DeepEqual(got.Thinking.Levels, []string{"none", "high"}) {
		t.Fatalf("field priority violated: %+v, thinking=%+v", got, got.Thinking)
	}
	if len(m.Models()) != 3 || findModel(t, m.Models(), "glm-unknown").ContextLimit != 0 {
		t.Fatal("fallback introduced a model or cross-provider metadata")
	}
	mustRefresh(t, m)
	if fallbackCalls != 1 {
		t.Fatalf("cache fetched %d times", fallbackCalls)
	}
	newCfg := cfg
	newCfg.Models = []config.ModelOverride{{Name: "glm-test", Metadata: modelmeta.Metadata{DisplayName: modelmeta.Ptr("Reconfigured")}}}
	seed := New(newCfg, client)
	seed.SeedFallbackFrom(m)
	seed.SeedFrom(m)
	got = findModel(t, seed.Models(), "glm-test")
	if got.DisplayName != "Reconfigured" || got.OutputLimit != 64 || got.Description != "Fallback description" || !reflect.DeepEqual(got.InputModes, []string{"text", "image"}) {
		t.Fatalf("seed retained old overrides or lost fallback: %+v", got)
	}
}

func TestFallbackFailureRetainsMetadata(t *testing.T) {
	cfg := metadataConfig()
	failed := false
	m := New(cfg, metadataClient(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if req.URL == cfg.ModelsDev.URL {
			if failed {
				return pluginapi.HTTPResponse{}, errors.New("private failure")
			}
			return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(metadataFallback)}, nil
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(metadataCatalog)}, nil
	}))
	mustRefresh(t, m)
	failed = true
	m.fallbackNext = time.Time{}
	mustRefresh(t, m)
	if findModel(t, m.Models(), "glm-test").OutputLimit != 64 || len(m.Warnings()) != 1 {
		t.Fatal("failed fallback discarded metadata or diagnostic")
	}
}

func TestFallbackInvalidAndDisabled(t *testing.T) {
	for _, body := range []string{`{}`, `{"opencode-go":{}}`, `null`, `not-json`} {
		t.Run(body, func(t *testing.T) {
			cfg := metadataConfig()
			m := New(cfg, metadataClient(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
				if req.URL == cfg.ModelsDev.URL {
					return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}, nil
				}
				return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(metadataCatalog)}, nil
			}))
			mustRefresh(t, m)
			if len(m.Models()) != 3 || len(m.Warnings()) != 1 {
				t.Fatal("invalid fallback must not fail catalog")
			}
		})
	}
	cfg := metadataConfig()
	cfg.ModelsDev.Enabled = false
	m := New(cfg, metadataClient(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if req.URL != cfg.CatalogURL {
			t.Fatal("disabled fallback called")
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(metadataCatalog)}, nil
	}))
	mustRefresh(t, m)
}

func TestModelsDevReasoningMapping(t *testing.T) {
	var budget modelsDevModel
	budget.Reasoning = modelmeta.Ptr(true)
	// Capability true alone must not invent effort levels.
	if got := budget.metadata().Thinking.Support(); got == nil || len(got.Levels) != 0 {
		t.Fatalf("reasoning flag invented efforts: %+v", got)
	}
	budget.Reasoning = modelmeta.Ptr(false)
	if got := budget.metadata().Thinking.Support(); !got.ZeroAllowed || !reflect.DeepEqual(got.Levels, []string{"none"}) {
		t.Fatalf("disabled reasoning: %+v", got)
	}
}

func TestFallbackCacheReconfigureDuringOutage(t *testing.T) {
	cfg := metadataConfig()
	fallbackFailed := false
	client := metadataClient(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if req.URL == cfg.ModelsDev.URL {
			if fallbackFailed {
				return pluginapi.HTTPResponse{}, errors.New("metadata unavailable")
			}
			return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(metadataFallback)}, nil
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(metadataCatalog)}, nil
	})
	old := New(cfg, client)
	mustRefresh(t, old)
	old.mu.Lock()
	old.fallbackNext = time.Now().Add(-time.Minute)
	old.mu.Unlock()
	fallbackFailed = true
	cfg.Catalog.StaleWhileUnavailable = false
	cfg.Models = []config.ModelOverride{{Name: "glm-test", Metadata: modelmeta.Metadata{DisplayName: modelmeta.Ptr("Reconfigured")}}}
	reconfigured := New(cfg, client)
	reconfigured.SeedFallbackFrom(old)
	if len(reconfigured.Models()) != 1 || !reconfigured.Models()[0].UserDefined {
		t.Fatal("fallback cache seeded upstream models instead of retaining only explicit declarations")
	}
	mustRefresh(t, reconfigured)
	model := findModel(t, reconfigured.Models(), "glm-test")
	if model.DisplayName != "Reconfigured" || model.OutputLimit != 64 || model.Description != "Fallback description" {
		t.Fatalf("reconfigure lost fallback or retained old overrides: %+v", model)
	}
	if len(reconfigured.Warnings()) != 1 {
		t.Fatal("fallback outage diagnostic missing")
	}
	changed := cfg
	changed.ModelsDev.URL = "https://different.test/api.json"
	other := New(changed, client)
	other.SeedFallbackFrom(old)
	if len(other.fallback) != 0 {
		t.Fatal("cache reused after source URL changed")
	}
	changed = cfg
	changed.ModelsDev.Enabled = false
	other = New(changed, client)
	other.SeedFallbackFrom(old)
	if len(other.fallback) != 0 {
		t.Fatal("cache reused after fallback disabled")
	}
}

func TestFallbackCacheNewRefreshInterval(t *testing.T) {
	cfg := metadataConfig()
	old := New(cfg, nil)
	fetched := time.Now()
	old.fallbackNext = fetched.Add(cfg.ModelsDev.RefreshInterval)
	cfg.ModelsDev.RefreshInterval = time.Minute
	m := New(cfg, nil)
	m.SeedFallbackFrom(old)
	if !m.fallbackNext.Equal(fetched.Add(time.Minute)) {
		t.Fatal("reconfigured refresh interval ignored")
	}
}

func TestSeedCatalogPreservesFallbackRefreshedDuringOutage(t *testing.T) {
	cfg := metadataConfig()
	catalogFailed := false
	fallbackBody := metadataFallback
	fallbackCalls := 0
	client := metadataClient(func(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		if req.URL == cfg.ModelsDev.URL {
			fallbackCalls++
			return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(fallbackBody)}, nil
		}
		if catalogFailed {
			return pluginapi.HTTPResponse{}, errors.New("catalog unavailable")
		}
		return pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(metadataCatalog)}, nil
	})
	old := New(cfg, client)
	mustRefresh(t, old)
	old.mu.Lock()
	old.fallbackNext = time.Now().Add(-time.Minute)
	old.mu.Unlock()
	catalogFailed = true
	fallbackBody = strings.ReplaceAll(metadataFallback, `"output":64`, `"output":128`)
	cfg.Models = []config.ModelOverride{{Name: "glm-test", Metadata: modelmeta.Metadata{DisplayName: modelmeta.Ptr("Reconfigured")}}}
	m := New(cfg, client)
	// Match lifecycle ordering: inherit fallback, attempt refresh, then recover
	// the previous upstream catalog if discovery failed with stale serving on.
	m.SeedFallbackFrom(old)
	if err := m.Refresh(t.Context(), testKey); err == nil {
		t.Fatal("catalog failure hidden")
	}
	if got := findModel(t, m.Models(), "glm-test").OutputLimit; got != 128 {
		t.Fatalf("fallback was not refreshed: %d", got)
	}
	next := m.fallbackNext
	m.SeedFrom(old)
	model := findModel(t, m.Models(), "glm-test")
	if model.OutputLimit != 128 || model.DisplayName != "Reconfigured" || model.UserDefined {
		t.Fatalf("catalog recovery replaced fresh fallback or ignored new configuration: %+v", model)
	}
	if _, ok := m.Lookup("glm-catalog"); !ok {
		t.Fatal("stale catalog was not restored")
	}
	if !m.fallbackNext.Equal(next) || !m.fallbackNext.After(time.Now()) {
		t.Fatal("fresh cache expiry replaced with old expiry")
	}
	if err := m.Refresh(t.Context(), testKey); err == nil {
		t.Fatal("catalog failure hidden")
	}
	if fallbackCalls != 2 {
		t.Fatalf("fresh fallback cache refetched: calls=%d", fallbackCalls)
	}
}
