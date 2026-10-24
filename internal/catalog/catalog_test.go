package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/dillonzq/cpa-opencode-go/internal/config"
)

const testKey = "sk-test-key-123"

// fakeClient is a canned, thread-safe HostClient capturing the last request.
type fakeClient struct {
	mu     sync.Mutex
	resp   pluginapi.HTTPResponse
	err    error
	gotReq *pluginapi.HTTPRequest
}

func (f *fakeClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotReq = &req
	return f.resp, f.err
}

func (f *fakeClient) set(resp pluginapi.HTTPResponse, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resp, f.err = resp, err
}

func testCfg() config.Config {
	return config.Config{
		CatalogURL:       "https://opencode.ai/inference/go/v1/models",
		ModelPrefix:      config.ModelPrefix{Enabled: true, Value: "opencode-go"},
		MaxResponseBytes: 1 << 20,
		Catalog:          config.Catalog{StaleWhileUnavailable: true},
	}
}

type protoEntry struct {
	name string
	config.ProtocolOverride
}

// declare adds a models[] entry carrying only protocol settings.
func declare(cfg *config.Config, name string, protocols ...protoEntry) {
	set := make(map[string]config.ProtocolOverride, len(protocols))
	for _, p := range protocols {
		set[p.name] = p.ProtocolOverride
	}
	cfg.Models = append(cfg.Models, config.ModelOverride{Name: name, Protocols: set})
}

// proto switches a protocol on; an empty endpoint keeps the inherited path.
func proto(protocol, endpoint string) protoEntry {
	return protoEntry{protocol, config.ProtocolOverride{Endpoint: endpoint}}
}

// off switches a protocol off.
func off(protocol string) protoEntry {
	disabled := false
	return protoEntry{protocol, config.ProtocolOverride{Enabled: &disabled}}
}

// routesOf lists a record's protocols in order.
func routesOf(rec ModelRecord) []Route {
	out := make([]Route, len(rec.Routes))
	for i, t := range rec.Routes {
		out[i] = t.Protocol
	}
	return out
}

// primary returns the record's first (documented) protocol.
func primary(rec ModelRecord) Route {
	if len(rec.Routes) == 0 {
		return ""
	}
	return rec.Routes[0].Protocol
}

func newManager(cfg config.Config, fc *fakeClient) *Manager {
	return New(cfg, fc)
}

func mustRefresh(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Refresh(context.Background(), testKey); err != nil {
		t.Fatalf("Refresh: unexpected error %v", err)
	}
}

func findModel(t *testing.T, models []ModelRecord, upstreamID string) ModelRecord {
	t.Helper()
	for _, mo := range models {
		if mo.UpstreamID == upstreamID {
			return mo
		}
	}
	t.Fatalf("model %q not found in %+v", upstreamID, models)
	return ModelRecord{}
}

func TestRouteEndpointPath(t *testing.T) {
	cases := []struct {
		route Route
		want  string
	}{
		{RouteChatCompletions, "/v1/chat/completions"},
		{RouteMessages, "/v1/messages"},
		{RouteResponses, "/v1/responses"},
		{RouteGemini, "/google/v1beta/models/{model}"},
		{Route("bogus"), ""},
	}
	for _, c := range cases {
		if got := c.route.EndpointPath(); got != c.want {
			t.Errorf("%q.EndpointPath() = %q, want %q", c.route, got, c.want)
		}
	}
}

func TestRefreshRequestShape(t *testing.T) {
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[]}`)}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)
	req := fc.gotReq
	if req.Method != "GET" {
		t.Errorf("Method = %q, want GET", req.Method)
	}
	if req.URL != "https://opencode.ai/inference/go/v1/models" {
		t.Errorf("URL = %q", req.URL)
	}
	if got := req.Headers.Get("Authorization"); got != "Bearer "+testKey {
		t.Errorf("Authorization = %q, want %q", got, "Bearer "+testKey)
	}
	if got := req.Headers.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}
}

func TestCodexCatalogExtensions(t *testing.T) {
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[
		{"id":"glm-capable","context_window":200000,"max_tokens":64000,
		 "input_modalities":["text","image"],"output_modalities":["text"],
		 "supported_reasoning_levels":[{"effort":"none"},{"effort":"high","description":"More reasoning"}]},
		{"id":"glm-empty","supported_reasoning_levels":[]},
		{"id":"glm-legacy","context_length":123,"max_output_tokens":456,
		 "input_modes":["image"],"output_modes":["audio"],"thinking":{"levels":["high"]}}
	]}`)}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)
	got := findModel(t, m.Models(), "glm-capable")
	if got.ContextLimit != 200000 || got.OutputLimit != 64000 ||
		!reflect.DeepEqual(got.InputModes, []string{"text", "image"}) ||
		!reflect.DeepEqual(got.OutputModes, []string{"text"}) ||
		got.Thinking == nil || !got.Thinking.ZeroAllowed ||
		!reflect.DeepEqual(got.Thinking.Levels, []string{"none", "high"}) {
		t.Fatalf("Codex capability mapping: %+v", got)
	}
	empty := findModel(t, m.Models(), "glm-empty")
	if empty.Thinking == nil || len(empty.Thinking.Levels) != 0 {
		t.Fatalf("explicit empty reasoning capabilities: %+v", empty.Thinking)
	}
	legacy := findModel(t, m.Models(), "glm-legacy")
	if legacy.ContextLimit != 0 || legacy.OutputLimit != 0 ||
		legacy.InputModes != nil || legacy.OutputModes != nil || legacy.Thinking != nil {
		t.Fatalf("legacy extension names must be ignored: %+v", legacy)
	}
}

func TestRefreshSuccess(t *testing.T) {
	body := `{"data":[
		{"id":"gpt-5.6-luna"},
		{"id":"glm-5.2"},
		{"id":"minimax-m3"}
	]}`
	cfg := testCfg()
	declare(&cfg, "gpt-5.6-luna", proto("responses", "/v1/responses"))
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}}
	m := newManager(cfg, fc)
	mustRefresh(t, m)

	models := m.Models()
	luna := findModel(t, models, "gpt-5.6-luna")
	if luna.PublicID != "opencode-go/gpt-5.6-luna" {
		t.Errorf("PublicID = %q", luna.PublicID)
	}
	if luna.DisplayName != "gpt-5.6-luna" || !reflect.DeepEqual(luna.Routes, []Target{{RouteResponses, "/v1/responses"}}) {
		t.Errorf("luna mapping wrong: %+v", luna)
	}

	glm := findModel(t, models, "glm-5.2")
	if glm.DisplayName != "glm-5.2" {
		t.Errorf("DisplayName fallback = %q, want upstream id", glm.DisplayName)
	}
	if !reflect.DeepEqual(glm.Routes, []Target{{RouteChatCompletions, "/v1/chat/completions"}}) {
		t.Errorf("glm route wrong: %+v", glm)
	}
	mm := findModel(t, models, "minimax-m3")
	if mm.DisplayName != "minimax-m3" || primary(mm) != RouteMessages {
		t.Errorf("minimax wrong: %+v", mm)
	}

	if len(m.Unsupported()) != 0 || len(m.Warnings()) != 1 ||
		!strings.Contains(m.Warnings()[0], `model "gpt-5.6-luna" routed via user configuration (responses /v1/responses)`) {
		t.Errorf("unexpected diagnostics: %v %v", m.Unsupported(), m.Warnings())
	}
}

func TestRoutePriorityMatrix(t *testing.T) {
	cfg := testCfg()
	declare(&cfg, "totally-new-x", proto("messages", "/v1/messages-override"), proto("gemini", ""))
	declare(&cfg, "grok-4.5", proto("chat-completions", ""), off("responses"))
	declare(&cfg, "glm-5.1", proto("messages", ""))
	// Switching off a protocol the model never had adds nothing.
	declare(&cfg, "mystery-model", off("responses"))
	body := `{"data":[
		{"id":"glm-5.2"},
		{"id":"totally-new-x"},
		{"id":"mystery-model"},
		{"id":"glm-5.1"},
		{"id":"qwen3.8-max"},
		{"id":"grok-4.5"}
	]}`
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}}
	m := newManager(cfg, fc)
	mustRefresh(t, m)

	models := m.Models()
	if got := primary(findModel(t, models, "glm-5.2")); got != RouteChatCompletions {
		t.Errorf("compat route wrong: got %q", got)
	}
	ovr := findModel(t, models, "totally-new-x")
	if !reflect.DeepEqual(ovr.Routes, []Target{{RouteMessages, "/v1/messages-override"}, {RouteGemini, "/google/v1beta/models/{model}"}}) {
		t.Errorf("declaration not applied: %+v", ovr)
	}
	if got := routesOf(findModel(t, models, "glm-5.1")); !reflect.DeepEqual(got, []Route{RouteChatCompletions, RouteMessages}) {
		t.Errorf("declaration should add to compat after its protocols: got %q", got)
	}
	if got := primary(findModel(t, models, "qwen3.8-max")); got != RouteMessages {
		t.Errorf("compat route wrong: got %q", got)
	}
	if got := findModel(t, models, "grok-4.5").Routes; !reflect.DeepEqual(got, []Target{{RouteChatCompletions, "/v1/chat/completions"}}) {
		t.Errorf("declaration should switch compat protocols: got %+v", got)
	}
	unsup := m.Unsupported()
	if len(unsup) != 1 || unsup[0].UpstreamID != "mystery-model" || unsup[0].Reason != "no route determined" {
		t.Errorf("unsupported wrong: %+v", unsup)
	}
	for _, mo := range models {
		if mo.UpstreamID == "mystery-model" {
			t.Error("unsupported model leaked into routable set")
		}
	}
}

// TestCompatExactIDs pins every model ID in the Oct 2026 OpenCode Go
// endpoint table (https://opencode.ai/docs/go/#endpoints), plus earlier
// documented IDs, to its protocol set. DeepSeek also serves Responses and
// Messages beyond its documented Chat endpoint.
func TestCompatExactIDs(t *testing.T) {
	chat := []Route{RouteChatCompletions}
	messages := []Route{RouteMessages}
	responses := []Route{RouteResponses}
	deepseek := []Route{RouteChatCompletions, RouteResponses, RouteMessages}
	cases := map[string][]Route{
		// Responses.
		"grok-4.7":                   responses,
		"grok-4.5":                   responses,
		"gpt-6-luna":                 responses,
		"gpt-5.6-luna":               responses,
		"muse-spark-1.3-contributor": responses,
		"muse-spark-1.2-contributor": responses,
		// Messages.
		"claude-haiku-5-5": messages,
		"minimax-m3":       messages,
		"minimax-m2.7":     messages,
		"minimax-m2.5":     messages,
		"qwen3.8-max":      messages,
		"qwen3.8-flash":    messages,
		"qwen3.7-max":      messages,
		"qwen3.7-plus":     messages,
		"qwen3.6-plus":     messages,
		// Gemini.
		"gemini-4-argon-contributor": {RouteGemini},
		// Chat Completions.
		"glm-5.3-flash":            chat,
		"glm-5.3":                  chat,
		"glm-5.2":                  chat,
		"glm-5.1":                  chat,
		"kimi-k3.1-pro":            chat,
		"kimi-k3":                  chat,
		"kimi-k2.7-code":           chat,
		"kimi-k2.6":                chat,
		"longcat-2.0":              chat,
		"longcat-2.5-preview-free": chat,
		"mimo-v2.6-flash":          chat,
		"mimo-v2.6-pro":            chat,
		"mimo-v2.5":                chat,
		"mimo-v2.5-pro":            chat,
		"minimax-m3.1-flash":       chat,
		"minimax-m3.1-flash-free":  chat,
		"hy4-preview":              chat,
		"hy3":                      chat,
		"step-5-preview":           chat,
		"beam-free":                chat,
		// Chat Completions documented; Responses and Messages also served.
		"deepseek-v4.1-flash": deepseek,
		"deepseek-v4-pro":     deepseek,
		"deepseek-v4-flash":   deepseek,
	}
	var sb strings.Builder
	sb.WriteString(`{"data":[`)
	i := 0
	for id := range cases {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":%q}`, id)
		i++
	}
	sb.WriteString(`]}`)
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(sb.String())}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)
	for _, mo := range m.Models() {
		if want := cases[mo.UpstreamID]; !reflect.DeepEqual(routesOf(mo), want) {
			t.Errorf("%s: protocols = %q, want %q", mo.UpstreamID, routesOf(mo), want)
		}
		for _, target := range mo.Routes {
			if target.EndpointPath != target.Protocol.EndpointPath() {
				t.Errorf("%s: %s EndpointPath = %q", mo.UpstreamID, target.Protocol, target.EndpointPath)
			}
		}
	}
	if len(m.Models()) != len(cases) {
		t.Errorf("got %d models, want %d", len(m.Models()), len(cases))
	}
}

func TestCompatPrefixFallback(t *testing.T) {
	cases := map[string]Route{
		"glm-9.9":              RouteChatCompletions,
		"qwen99-max":           RouteMessages,
		"gpt-9":                RouteResponses,
		"muse-spark-9":         RouteResponses,
		"kimi-k99":             RouteChatCompletions,
		"deepseek-v99":         RouteChatCompletions,
		"minimax-m99":          RouteMessages,
		"minimax-m3.1-flash-x": RouteChatCompletions,
		"claude-sonnet-9":      RouteMessages,
		"gemini-9-pro":         RouteGemini,
		"step-9":               RouteChatCompletions,
		"beam-pro":             RouteChatCompletions,
		"mimo-x":               RouteChatCompletions,
		"hy3-extreme":          RouteChatCompletions,
		"hy4-turbo":            RouteChatCompletions,
		"longcat-max":          RouteChatCompletions,
		"GLM-5.2":              RouteChatCompletions,
		"DeepSeek-V4-X":        RouteChatCompletions,
		"HY3":                  RouteChatCompletions,
		"HY4":                  RouteChatCompletions,
		"GPT-5.6-LUNA-X":       RouteResponses,
		"QWEN4-MAX":            RouteMessages,
		"Gemini-X":             RouteGemini,
	}
	var sb strings.Builder
	sb.WriteString(`{"data":[`)
	i := 0
	for id := range cases {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":%q}`, id)
		i++
	}
	sb.WriteString(`]}`)
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(sb.String())}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)
	if got := len(m.Models()); got != len(cases) {
		t.Fatalf("got %d routable, want %d", got, len(cases))
	}
	for _, mo := range m.Models() {
		if want := cases[mo.UpstreamID]; primary(mo) != want {
			t.Errorf("%s: primary protocol = %q, want %q", mo.UpstreamID, primary(mo), want)
		}
	}
}

// TestPrefixRoutesLongestFirst keeps longest-prefix-wins true: a prefix may
// only follow the prefixes it does not extend.
func TestPrefixRoutesLongestFirst(t *testing.T) {
	for i, later := range prefixRoutes {
		for _, earlier := range prefixRoutes[:i] {
			if strings.HasPrefix(later.prefix, earlier.prefix) {
				t.Errorf("prefix %q is shadowed by earlier %q", later.prefix, earlier.prefix)
			}
		}
		if len(later.routes) == 0 {
			t.Errorf("prefix %q has no protocols", later.prefix)
		}
	}
}

func TestDedupFirstWins(t *testing.T) {
	body := `{"data":[
		{"id":"glm-5.2"},
		{"id":"glm-5.2"}
	]}`
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)

	models := m.Models()
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	warns := m.Warnings()
	if len(warns) != 1 || !strings.Contains(warns[0], `"glm-5.2"`) {
		t.Errorf("warnings = %v, want duplicate diagnostic", warns)
	}
}

func TestMissingIDSkippedAndDiagnosed(t *testing.T) {
	body := `{"data":[{}, {"id":"glm-5.2"}]}`
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)

	if len(m.Models()) != 1 {
		t.Fatalf("got %d models, want 1", len(m.Models()))
	}
	unsup := m.Unsupported()
	if len(unsup) != 1 || unsup[0].UpstreamID != "" || unsup[0].Reason != "missing id" {
		t.Errorf("unsupported = %+v, want missing-id diagnostic", unsup)
	}
}

func seedGoodSnapshot(t *testing.T, m *Manager) {
	t.Helper()
	mustRefresh(t, m)
}

func TestRefreshErrorsKeepStaleSnapshot(t *testing.T) {
	cases := []struct {
		name     string
		resp     pluginapi.HTTPResponse
		err      error
		maxBytes int64
		wantCat  string
	}{
		{"http 429", pluginapi.HTTPResponse{StatusCode: 429, Body: []byte("{}")}, nil, 1 << 20, "http 429"},
		{"http 500", pluginapi.HTTPResponse{StatusCode: 500}, nil, 1 << 20, "http 500"},
		{"malformed json", pluginapi.HTTPResponse{StatusCode: 200, Body: []byte("{not json")}, nil, 1 << 20, "invalid json"},
		{"transport error", pluginapi.HTTPResponse{}, errors.New("dial tcp: refused"), 1 << 20, "network error"},
		{"oversized body", pluginapi.HTTPResponse{StatusCode: 200, Body: make([]byte, catalogBudgetFloor+1)}, nil, 64, "response exceeds max-response-bytes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testCfg()
			cfg.MaxResponseBytes = c.maxBytes
			fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"gpt-5.6-luna"}]}`)}}
			m := newManager(cfg, fc)
			seedGoodSnapshot(t, m)

			fc.set(c.resp, c.err)
			err := m.Refresh(context.Background(), testKey)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), c.wantCat) {
				t.Errorf("error %q missing category %q", err, c.wantCat)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("error leaks api key: %q", err)
			}
			if len(m.Models()) != 1 {
				t.Errorf("stale snapshot lost: %d models", len(m.Models()))
			}
			if got := m.Models()[0].UpstreamID; got != "gpt-5.6-luna" {
				t.Errorf("stale snapshot wrong model %q", got)
			}
		})
	}
}

// TestFetchBudgetFloor pins the catalogBudgetFloor fix: an undersized
// max-response-bytes must not make every refresh fail permanently when the
// live catalog is larger than the knob (with stale-while-unavailable:false
// that would wipe the routable set each tick). The fetch check uses
// max(MaxResponseBytes, floor); genuinely oversized bodies (>floor) are
// still rejected.
func TestFetchBudgetFloor(t *testing.T) {
	// Valid ~2KB catalog body, larger than the undersized 1024-byte knob.
	body := `{"data":[` + strings.Repeat(`{"id":"glm-5.2"},`, 2048) + `{"id":"glm-5.1"}]}`
	if int64(len(body)) <= 1024 {
		t.Fatalf("precondition: catalog body %d bytes should exceed the knob", len(body))
	}
	cfg := testCfg()
	cfg.MaxResponseBytes = 1024
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}}
	m := newManager(cfg, fc)
	mustRefresh(t, m)
	models := m.Models()
	if len(models) != 2 || models[0].UpstreamID != "glm-5.2" || models[1].UpstreamID != "glm-5.1" {
		t.Fatalf("undersized max-response-bytes broke refresh: %+v", models)
	}

	// A body exceeding the floor stays rejected even with a small knob.
	fc.set(pluginapi.HTTPResponse{StatusCode: 200, Body: make([]byte, catalogBudgetFloor+1)}, nil)
	err := m.Refresh(context.Background(), testKey)
	if err == nil || !strings.Contains(err.Error(), "response exceeds max-response-bytes") {
		t.Fatalf("oversized body (>floor) accepted: err=%v", err)
	}
}

// TestRefreshNilClientClassifiedError pins that a Manager built without a
// host client (direct construction only; production always wires the bridge)
// fails Refresh through the classified path instead of panicking on Do.
func TestRefreshNilClientClassifiedError(t *testing.T) {
	m := New(testCfg(), nil)
	err := m.Refresh(context.Background(), testKey)
	if err == nil || !strings.Contains(err.Error(), "host client unavailable") {
		t.Fatalf("nil client must yield classified error, got %v", err)
	}
	if len(m.Models()) != 0 {
		t.Fatalf("nil-client refresh must not fabricate models: %+v", m.Models())
	}
}

func TestRefreshErrorClearsWhenNoStale(t *testing.T) {
	cfg := testCfg()
	cfg.Catalog.StaleWhileUnavailable = false
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"gpt-5.6-luna"}]}`)}}
	m := newManager(cfg, fc)
	seedGoodSnapshot(t, m)

	fc.resp = pluginapi.HTTPResponse{StatusCode: 503}
	clearErr := m.Refresh(context.Background(), testKey)
	if clearErr == nil || !strings.Contains(clearErr.Error(), "http 503") {
		t.Fatalf("clear-path error = %v, want http 503 category", clearErr)
	}
	if got := m.Models(); len(got) != 0 {
		t.Errorf("routable set not cleared: %+v", got)
	}
	if got := m.Unsupported(); len(got) != 0 {
		t.Errorf("diagnostics not cleared: %+v", got)
	}
	if got := m.Warnings(); len(got) != 0 {
		t.Errorf("warnings not cleared: %+v", got)
	}

	// Next success restores the routable set.
	fc.err = nil
	fc.resp = pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"glm-5.2"}]}`)}
	mustRefresh(t, m)
	if got := m.Models(); len(got) != 1 || got[0].UpstreamID != "glm-5.2" {
		t.Errorf("recovery failed: %+v", got)
	}
}

func TestPublicIDWithoutPrefix(t *testing.T) {
	cfg := testCfg()
	cfg.ModelPrefix.Enabled = false
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"glm-5.2"}]}`)}}
	m := newManager(cfg, fc)
	mustRefresh(t, m)
	if got := m.Models()[0].PublicID; got != "glm-5.2" {
		t.Errorf("PublicID = %q, want bare id", got)
	}
}

// TestLookupByPrefixedBareAndMiss pins O(1) ID resolution over the swap
// index: public (prefixed) and bare upstream IDs resolve; misses do not.
func TestLookupByPrefixedBareAndMiss(t *testing.T) {
	body := `{"data":[{"id":"glm-5.2"},{"id":"gpt-5.6-luna"}]}`
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)

	rec, ok := m.Lookup("opencode-go/glm-5.2")
	if !ok || rec.UpstreamID != "glm-5.2" || primary(rec) != RouteChatCompletions {
		t.Errorf("prefixed lookup = %+v %v", rec, ok)
	}
	rec, ok = m.Lookup("glm-5.2")
	if !ok || rec.PublicID != "opencode-go/glm-5.2" {
		t.Errorf("bare lookup = %+v %v", rec, ok)
	}
	rec, ok = m.Lookup("gpt-5.6-luna")
	if !ok || rec.PublicID != "opencode-go/gpt-5.6-luna" {
		t.Errorf("bare lookup second model = %+v %v", rec, ok)
	}
	if _, ok := m.Lookup("no-such-model"); ok {
		t.Error("miss must not resolve")
	}
	if _, ok := m.Lookup(""); ok {
		t.Error("empty id must not resolve")
	}

	// Lookup must track the refreshed snapshot.
	fc.set(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[]}`)}, nil)
	mustRefresh(t, m)
	if _, ok := m.Lookup("glm-5.2"); ok {
		t.Error("cleared snapshot must not serve stale lookups")
	}
}

// TestSeedFromServesOldRecordsWithNewCfg pins the F5 fix: SeedFrom gives m
// prev's last-good snapshot rebuilt under m's OWN cfg — prefix-off here, so
// seeded PublicIDs are bare where prev's were prefixed — and subsequent
// refreshes use m's catalog-url while prev stays untouched.
func TestSeedFromServesOldRecordsWithNewCfg(t *testing.T) {
	body := `{"data":[{"id":"glm-5.2"},{"id":"glm-5.2"},{"id":"mystery-model"}]}`
	prev := newManager(testCfg(), &fakeClient{resp: pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(body),
	}})
	mustRefresh(t, prev)

	cfg := testCfg()
	cfg.CatalogURL = "https://mirror.test/api/models"
	cfg.ModelPrefix.Enabled = false
	fc := &fakeClient{}
	m := newManager(cfg, fc)
	m.SeedFrom(prev)

	got := m.Models()
	if len(got) != 1 || got[0].UpstreamID != "glm-5.2" || got[0].PublicID != "glm-5.2" {
		t.Fatalf("seeded models = %+v, want glm-5.2 with recomputed bare PublicID", got)
	}
	if rec, ok := m.Lookup("glm-5.2"); !ok || rec.PublicID != "glm-5.2" {
		t.Errorf("seeded bare lookup = %+v %v", rec, ok)
	}
	if _, ok := m.Lookup("opencode-go/glm-5.2"); ok {
		t.Error("prev's prefixed public id must not resolve under m's prefix-off cfg")
	}
	if gotU, wantU := m.Unsupported(), prev.Unsupported(); !reflect.DeepEqual(gotU, wantU) {
		t.Errorf("seeded unsupported = %+v, want %+v", gotU, wantU)
	}
	if gotW, wantW := m.Warnings(), prev.Warnings(); !reflect.DeepEqual(gotW, wantW) {
		t.Errorf("seeded warnings = %v, want %v", gotW, wantW)
	}

	// The next refresh uses m's own cfg: its catalog-url and prefix-off
	// PublicIDs; prev keeps serving its own snapshot untouched.
	fc.resp = pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[{"id":"kimi-k3"}]}`)}
	mustRefresh(t, m)
	if fc.gotReq.URL != "https://mirror.test/api/models" {
		t.Errorf("refresh URL = %q, want the seeded manager's own catalog-url", fc.gotReq.URL)
	}
	if got := m.Models(); len(got) != 1 || got[0].PublicID != "kimi-k3" || got[0].UpstreamID != "kimi-k3" {
		t.Errorf("post-seed refresh models = %+v, want prefix-off kimi-k3", got)
	}
	if got := prev.Models(); len(got) != 1 || got[0].UpstreamID != "glm-5.2" {
		t.Errorf("prev mutated by seeding: %+v", got)
	}
}

func TestRouteCountsViaModels(t *testing.T) {
	body := `{"data":[
		{"id":"glm-5.2"},{"id":"glm-5.1"},
		{"id":"minimax-m3"},{"id":"qwen3.8-max"},{"id":"qwen3.7-plus"},
		{"id":"gpt-5.6-luna"}
	]}`
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)
	counts := map[Route]int{}
	for _, mo := range m.Models() {
		counts[primary(mo)]++
	}
	if counts[RouteChatCompletions] != 2 || counts[RouteMessages] != 3 || counts[RouteResponses] != 1 {
		t.Errorf("counts = %v", counts)
	}
}

func TestModelsReturnCopies(t *testing.T) {
	fc := &fakeClient{resp: pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(`{"data":[{"id":"gpt-5.6-luna"}]}`),
	}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)

	// The returned slice is a copy; records are rebuilt fresh on every swap
	// and consumers treat them as read-only, so only list isolation is
	// guaranteed (nested slices are shared by design).
	got := m.Models()
	got[0].UpstreamID = "mutated"
	got[0] = ModelRecord{}
	fresh := m.Models()
	if fresh[0].UpstreamID != "gpt-5.6-luna" {
		t.Errorf("snapshot mutated through returned slice: %+v", fresh[0])
	}

	unsup := m.Unsupported()
	_ = append(unsup, UnsupportedModel{UpstreamID: "x"})
	if len(m.Unsupported()) != 0 {
		t.Error("Unsupported not returning a copy")
	}
	warns := m.Warnings()
	_ = append(warns, "x")
	if len(m.Warnings()) != 0 {
		t.Error("Warnings not returning a copy")
	}
}

func TestConcurrentRefreshAndReads(t *testing.T) {
	fc := &fakeClient{}
	m := newManager(testCfg(), fc)

	var writerWG, readerWG sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 3; r++ {
		writerWG.Add(1)
		go func(r int) {
			defer writerWG.Done()
			for i := 0; i < 50; i++ {
				if i%10 == 0 {
					fc.set(pluginapi.HTTPResponse{StatusCode: 500}, nil)
				} else {
					fc.set(pluginapi.HTTPResponse{StatusCode: 200,
						Body: []byte(fmt.Sprintf(`{"data":[{"id":"glm-%d"}]}`, r))}, nil)
				}
				_ = m.Refresh(context.Background(), testKey)
			}
		}(r)
	}
	for r := 0; r < 3; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = m.Models()
					_ = m.Unsupported()
					_ = m.Warnings()
				}
			}
		}()
	}
	writerWG.Wait() // writers are bounded; then release readers
	close(stop)
	readerWG.Wait()
}

func TestProtocolSwitchesExcludeRoutes(t *testing.T) {
	catalogBody := `{"data":[
		{"id":"glm-5.2"},
		{"id":"qwen3.7-max"},
		{"id":"gpt-5.6-luna"}
	]}`
	routes := []struct {
		protocol  string
		excluded  string
		routableA string
		routableB string
	}{
		{"chat-completions", "glm-5.2", "qwen3.7-max", "gpt-5.6-luna"},
		{"messages", "qwen3.7-max", "glm-5.2", "gpt-5.6-luna"},
		{"responses", "gpt-5.6-luna", "glm-5.2", "qwen3.7-max"},
	}
	for _, tc := range routes {
		t.Run(tc.protocol, func(t *testing.T) {
			cfg := testCfg()
			declare(&cfg, tc.excluded, off(tc.protocol))
			fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(catalogBody)}}
			m := newManager(cfg, fc)
			mustRefresh(t, m)

			models := m.Models()
			for _, mo := range models {
				if mo.UpstreamID == tc.excluded {
					t.Errorf("disabled-protocol model %q still routable: %+v", tc.excluded, mo)
				}
			}
			findModel(t, models, tc.routableA)
			findModel(t, models, tc.routableB)

			unsup := m.Unsupported()
			if len(unsup) != 1 || unsup[0].UpstreamID != tc.excluded ||
				unsup[0].Reason != "protocol "+tc.protocol+" disabled by config" {
				t.Fatalf("unsupported = %+v, want %q excluded with disabled-reason", unsup, tc.excluded)
			}
		})
	}
}

// A switch removes only its own protocol from a multi-protocol model; the
// model stays routable until every protocol it serves is disabled.
func TestProtocolSwitchesFilterMultiProtocolModels(t *testing.T) {
	body := `{"data":[{"id":"deepseek-v4-pro"},{"id":"gemini-4-argon-contributor"}]}`
	cfg := testCfg()
	declare(&cfg, "deepseek-v4-pro", off("chat-completions"))
	declare(&cfg, "gemini-4-argon-contributor", off("gemini"))
	m := newManager(cfg, &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}})
	mustRefresh(t, m)
	deepseek := findModel(t, m.Models(), "deepseek-v4-pro")
	if got := routesOf(deepseek); !reflect.DeepEqual(got, []Route{RouteResponses, RouteMessages}) {
		t.Fatalf("deepseek protocols = %q, want chat filtered out", got)
	}
	unsup := m.Unsupported()
	if len(unsup) != 1 || unsup[0].UpstreamID != "gemini-4-argon-contributor" || unsup[0].Reason != "protocol gemini disabled by config" {
		t.Fatalf("unsupported = %+v", unsup)
	}

	cfg = testCfg()
	declare(&cfg, "deepseek-v4-pro", off("chat-completions"), off("responses"), off("messages"))
	declare(&cfg, "gemini-4-argon-contributor", off("gemini"))
	m = newManager(cfg, &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}})
	mustRefresh(t, m)
	if len(m.Models()) != 0 {
		t.Fatalf("models = %+v, want none routable", m.Models())
	}
	if unsup := m.Unsupported(); len(unsup) != 2 || unsup[0].Reason != "protocols chat-completions, responses, messages disabled by config" {
		t.Fatalf("unsupported = %+v", unsup)
	}
}

// Global protocols set the path for every model serving the protocol; a
// model's own endpoint takes precedence.
func TestGlobalProtocolEndpoints(t *testing.T) {
	body := `{"data":[{"id":"glm-5.2"},{"id":"glm-5.1"},{"id":"deepseek-v4-pro"},{"id":"gemini-4-argon-contributor"}]}`
	cfg := testCfg()
	cfg.Protocols = map[string]config.ProtocolOverride{
		"chat-completions": {Endpoint: "/v1/global-chat"},
		"gemini":           {Endpoint: "/gemini/models/{model}"},
	}
	declare(&cfg, "glm-5.1", proto("chat-completions", "/v1/own-chat"))
	m := newManager(cfg, &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}})
	mustRefresh(t, m)
	models := m.Models()
	cases := map[string][]Target{
		"glm-5.2": {{RouteChatCompletions, "/v1/global-chat"}},
		"glm-5.1": {{RouteChatCompletions, "/v1/own-chat"}},
		"deepseek-v4-pro": {
			{RouteChatCompletions, "/v1/global-chat"},
			{RouteResponses, "/v1/responses"},
			{RouteMessages, "/v1/messages"},
		},
		"gemini-4-argon-contributor": {{RouteGemini, "/gemini/models/{model}"}},
	}
	for id, want := range cases {
		if got := findModel(t, models, id).Routes; !reflect.DeepEqual(got, want) {
			t.Errorf("%s routes = %+v, want %+v", id, got, want)
		}
	}
}

func TestDefaultProtocolsKeepEverything(t *testing.T) {
	fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[
		{"id":"glm-5.2"},{"id":"qwen3.7-max"},{"id":"gpt-5.6-luna"}
	]}`)}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)
	if got := len(m.Models()); got != 3 {
		t.Fatalf("models = %d, want all three routable with default protocols", got)
	}
	if len(m.Unsupported()) != 0 {
		t.Fatalf("unexpected unsupported entries: %+v", m.Unsupported())
	}
}

// TestOverrideApplicationRecordedInWarnings pins spec 04 §5: user protocol
// routing MUST be visible in diagnostics, including defaulted endpoints.
func TestOverrideApplicationRecordedInWarnings(t *testing.T) {
	cfg := testCfg()
	declare(&cfg, "weird-model", proto("chat-completions", "/v1/custom"), proto("messages", ""))
	fc := &fakeClient{resp: pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(`{"data":[{"id":"weird-model"}]}`),
	}}
	m := newManager(cfg, fc)
	mustRefresh(t, m)

	warns := m.Warnings()
	if len(warns) != 1 || !strings.Contains(warns[0], `model "weird-model" routed via user configuration`) ||
		!strings.Contains(warns[0], "chat-completions /v1/custom, messages /v1/messages") {
		t.Fatalf("override warning wrong: %+v", warns)
	}
}

// TestJoinUpstreamURL pins the shared join formula: base-url carries no
// version segment, so endpoint paths are appended verbatim.
func TestJoinUpstreamURL(t *testing.T) {
	cases := []struct{ base, endpoint, want string }{
		{"https://opencode.ai/inference/go", "/v1/chat/completions", "https://opencode.ai/inference/go/v1/chat/completions"},
		{"https://opencode.ai/inference/go/", "/v1/messages", "https://opencode.ai/inference/go/v1/messages"},
		{"https://opencode.ai", "/google/v1beta/models/m", "https://opencode.ai/google/v1beta/models/m"},
		{"https://opencode.ai", "@evil.com", "https://opencode.ai@evil.com"},
		{"https://opencode.ai/", "", "https://opencode.ai"},
	}
	for _, c := range cases {
		if got := JoinUpstreamURL(c.base, c.endpoint); got != c.want {
			t.Errorf("JoinUpstreamURL(%q, %q) = %q, want %q", c.base, c.endpoint, got, c.want)
		}
	}
}

// TestTargetURL pins per-protocol URLs: Gemini substitutes the escaped model
// and appends the operation for the streaming mode.
func TestTargetURL(t *testing.T) {
	const base = "https://opencode.ai/inference/go"
	gemini := Target{RouteGemini, RouteGemini.EndpointPath()}
	cases := []struct {
		target Target
		model  string
		stream bool
		want   string
	}{
		{Target{RouteChatCompletions, "/v1/chat/completions"}, "glm-5.2", true, base + "/v1/chat/completions"},
		{Target{RouteResponses, "/v1/custom-responses"}, "gpt-5.6-luna", false, base + "/v1/custom-responses"},
		{gemini, "gemini-4-argon-contributor", false, base + "/google/v1beta/models/gemini-4-argon-contributor:generateContent"},
		{gemini, "gemini-4-argon-contributor", true, base + "/google/v1beta/models/gemini-4-argon-contributor:streamGenerateContent?alt=sse"},
		{gemini, "a/b?c", false, base + "/google/v1beta/models/a%2Fb%3Fc:generateContent"},
		{Target{RouteGemini, "/custom/{model}/x"}, "m", false, base + "/custom/m/x:generateContent"},
	}
	for _, c := range cases {
		if got := c.target.URL(base, c.model, c.stream); got != c.want {
			t.Errorf("%+v.URL(%q, %t) = %q, want %q", c.target, c.model, c.stream, got, c.want)
		}
	}
}

// TestEndpointEscapesBase pins the authority-escape check used at snapshot
// build: joining base-url with an endpoint the way executor.upstreamURL does
// must never move the request off the configured host/scheme, and parse
// failures fail closed.
func TestEndpointEscapesBase(t *testing.T) {
	cases := []struct {
		base     string
		endpoint string
		want     bool
	}{
		{"https://opencode.ai", "@evil.com/x", true},
		{"https://opencode.ai", "@evil.com/", true},
		{"https://opencode.ai", "/v1@evil.com/x", false}, // @ lands in path
		{"https://opencode.ai", "/v1/chat/completions", false},
		{"https://opencode.ai/inference/go", "/v1/messages", false},
		{"https://opencode.ai/", "/v1/responses", false},
		{"https://OPENCODE.ai", "/v1/chat/completions", false}, // host compare is case-insensitive
		{"https://opencode.ai", ".evil.com", true},
		{"https://opencode.ai", "\x7f", true},   // unparseable join
		{"https://opencode.ai\x00", "/x", true}, // unparseable base
	}
	for _, c := range cases {
		target := Target{RouteChatCompletions, c.endpoint}
		if got := endpointEscapesBase(c.base, target, "m"); got != c.want {
			t.Errorf("endpointEscapesBase(%q, %q) = %v, want %v", c.base, c.endpoint, got, c.want)
		}
	}
	// The substituted model is path-escaped and cannot rewrite the authority.
	gemini := Target{RouteGemini, "/google/v1beta/models/{model}"}
	for _, model := range []string{"x@evil.com", "../../@evil.com", "a/b#frag"} {
		if endpointEscapesBase("https://opencode.ai", gemini, model) {
			t.Errorf("gemini model %q escapes the base", model)
		}
	}
	if !endpointEscapesBase("https://opencode.ai", Target{RouteGemini, "{model}"}, "@evil.com") {
		t.Error("a model placeholder ahead of any path separator must fail the check")
	}
}

// F-security pin: user endpoints are concatenated onto base-url; one that
// rewrites the URL authority (e.g. "@evil.com/x" against a bare-host
// base-url, if config validation were bypassed) must exclude the model from
// the routable set (spec 05 §2).
func TestEndpointEscapeExcludedFromRoutableSet(t *testing.T) {
	const escapeReason = "resolved endpoint escapes the configured upstream host"

	t.Run("bare-host base rejects authority rewrite", func(t *testing.T) {
		cfg := testCfg()
		cfg.BaseURL = "https://opencode.ai"
		declare(&cfg, "evil", proto("chat-completions", "@evil.com/x"))
		declare(&cfg, "custom-ok", proto("responses", "/v1/custom"))
		fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[
			{"id":"evil"},
			{"id":"custom-ok"}
		]}`)}}
		m := newManager(cfg, fc)
		mustRefresh(t, m)

		for _, mo := range m.Models() {
			if mo.UpstreamID == "evil" {
				t.Fatalf("authority-rewriting endpoint still routable: %+v", mo)
			}
			for _, target := range mo.Routes {
				u := target.URL(cfg.BaseURL, mo.UpstreamID, true)
				if p, err := url.Parse(u); err != nil || !strings.EqualFold(p.Host, "opencode.ai") || p.Scheme != "https" {
					t.Fatalf("routable record escapes base: %q parses to %+v err=%v", u, p, err)
				}
			}
		}
		customFound := false
		for _, mo := range m.Models() {
			if mo.UpstreamID == "custom-ok" && reflect.DeepEqual(mo.Routes, []Target{{RouteResponses, "/v1/custom"}}) {
				customFound = true
			}
		}
		if !customFound {
			t.Fatal("normal custom endpoint must stay routable")
		}
		unsup := m.Unsupported()
		if len(unsup) != 1 || unsup[0].UpstreamID != "evil" || unsup[0].Reason != escapeReason {
			t.Fatalf("unsupported = %+v, want evil excluded with %q", unsup, escapeReason)
		}
	})

	t.Run("@ inside the path stays routable", func(t *testing.T) {
		cfg := testCfg()
		cfg.BaseURL = "https://opencode.ai"
		declare(&cfg, "glm-5.2", proto("chat-completions", "/v1@evil.com/x"))
		fc := &fakeClient{resp: pluginapi.HTTPResponse{
			StatusCode: 200,
			Body:       []byte(`{"data":[{"id":"glm-5.2"}]}`),
		}}
		m := newManager(cfg, fc)
		mustRefresh(t, m)
		rec := findModel(t, m.Models(), "glm-5.2")
		if !reflect.DeepEqual(rec.Routes, []Target{{RouteChatCompletions, "/v1@evil.com/x"}}) {
			t.Errorf("Routes = %+v; @ stays in path after the leading slash", rec.Routes)
		}
	})

	t.Run("only the escaping protocol is dropped", func(t *testing.T) {
		cfg := testCfg()
		cfg.BaseURL = "https://opencode.ai"
		declare(&cfg, "mystery-model", proto("chat-completions", "@evil.com/y"), proto("messages", ""))
		fc := &fakeClient{resp: pluginapi.HTTPResponse{
			StatusCode: 200,
			Body:       []byte(`{"data":[{"id":"mystery-model"}]}`),
		}}
		m := newManager(cfg, fc)
		mustRefresh(t, m)

		rec := findModel(t, m.Models(), "mystery-model")
		if !reflect.DeepEqual(rec.Routes, []Target{{RouteMessages, "/v1/messages"}}) {
			t.Fatalf("Routes = %+v, want only messages", rec.Routes)
		}
		if len(m.Unsupported()) != 0 {
			t.Fatalf("unsupported = %+v", m.Unsupported())
		}
		var found bool
		for _, w := range m.Warnings() {
			found = found || w == `model "mystery-model" chat-completions: `+escapeReason
		}
		if !found {
			t.Fatalf("warnings = %v, want dropped-protocol diagnostic", m.Warnings())
		}
	})
}

func TestFailClearsLookupIndex(t *testing.T) {
	cfg := testCfg()
	cfg.Catalog.StaleWhileUnavailable = false
	fc := &fakeClient{resp: pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(`{"data":[{"id":"glm-5.2"}]}`),
	}}
	m := newManager(cfg, fc)
	mustRefresh(t, m)
	if _, ok := m.Lookup("glm-5.2"); !ok {
		t.Fatal("precondition: glm-5.2 must resolve after good refresh")
	}

	fc.resp = pluginapi.HTTPResponse{StatusCode: 503}
	if err := m.Refresh(context.Background(), testKey); err == nil {
		t.Fatal("expected refresh failure")
	}
	if got := m.Models(); len(got) != 0 {
		t.Fatalf("models not cleared: %+v", got)
	}
	// FR-002: a cleared snapshot must stop resolving previously-valid IDs.
	if _, ok := m.Lookup("glm-5.2"); ok {
		t.Fatal("Lookup still resolves IDs from the cleared snapshot")
	}
}

// TestSeedFromRevalidatesEndpointsAgainstNewBase pins the seed-revalidation
// fix: a record whose endpoint was safe under the OLD base-url (path-hosted,
// so "@evil.com/x" stays in the path) must not keep serving once seeded into
// a manager whose base-url is the bare host — there the join turns the old
// host into userinfo and the endpoint escapes to an attacker authority.
func TestSeedFromRevalidatesEndpointsAgainstNewBase(t *testing.T) {
	body := `{"data":[
		{"id":"glm-5.2"},
		{"id":"totally-new-x"}
	]}`
	oldCfg := testCfg()
	oldCfg.BaseURL = "https://api.test/v1"
	oldCfg.CatalogURL = "https://api.test/v1/models"
	declare(&oldCfg, "totally-new-x", proto("chat-completions", "@evil.com/x"))
	prev := newManager(oldCfg, &fakeClient{resp: pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(body),
	}})
	mustRefresh(t, prev)
	findModel(t, prev.Models(), "totally-new-x") // precondition: routable pre-seed

	cfg := testCfg()
	cfg.BaseURL = "https://opencode.ai"
	cfg.CatalogURL = "https://opencode.ai/go/v1/models"
	declare(&cfg, "totally-new-x", proto("chat-completions", "@evil.com/x"))
	m := newManager(cfg, &fakeClient{})
	m.SeedFrom(prev)

	findModel(t, m.Models(), "glm-5.2") // safe record keeps serving
	for _, mo := range m.Models() {
		if mo.UpstreamID == "totally-new-x" {
			t.Fatalf("escaping seeded endpoint still routable: %+v", mo)
		}
	}
	if _, ok := m.Lookup("opencode-go/totally-new-x"); ok {
		t.Fatal("escaping record must not resolve via Lookup")
	}
	const wantReason = "resolved endpoint escapes the configured upstream host"
	var found bool
	for _, u := range m.Unsupported() {
		if u.UpstreamID == "totally-new-x" && u.Reason == wantReason {
			found = true
		}
	}
	if !found {
		t.Fatalf("unsupported = %+v, want totally-new-x with %q", m.Unsupported(), wantReason)
	}
	// Seeding must not mutate prev's snapshot (shared backing arrays).
	findModel(t, prev.Models(), "totally-new-x")
}

// TestSeedFromAppliesProtocolKillSwitch pins the seed-revalidation fix: a
// protocol disabled between snapshots must demote its routed records at
// seed time. Otherwise an operator switching a model's messages protocol off
// during a catalog outage keeps serving it off the stale snapshot until a
// refresh succeeds — the switch is inert exactly when it needs to bite.
func TestSeedFromAppliesProtocolSwitch(t *testing.T) {
	body := `{"data":[{"id":"minimax-m3"},{"id":"glm-5.2"}]}`
	prev := newManager(testCfg(), &fakeClient{resp: pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(body),
	}})
	mustRefresh(t, prev)
	findModel(t, prev.Models(), "minimax-m3") // precondition: routable pre-seed

	cfg := testCfg()
	declare(&cfg, "minimax-m3", off("messages"))
	m := newManager(cfg, &fakeClient{})
	m.SeedFrom(prev)

	for _, mo := range m.Models() {
		if mo.Supports(RouteMessages) {
			t.Fatalf("disabled-protocol record still routable after seed: %+v", mo)
		}
	}
	findModel(t, m.Models(), "glm-5.2") // unaffected route keeps serving
	if _, ok := m.Lookup("minimax-m3"); ok {
		t.Fatal("disabled-protocol record must not resolve via Lookup")
	}
	var found bool
	for _, u := range m.Unsupported() {
		if u.UpstreamID == "minimax-m3" && u.Reason == "protocol messages disabled by config" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unsupported = %+v, want minimax-m3 demotion diagnostic", m.Unsupported())
	}
	findModel(t, prev.Models(), "minimax-m3") // seeding must not mutate prev
}

// TestSeedFromRecomputesPublicIDsUnderNewPrefix pins prefix recomputation at
// seed time: records rebuilt from prev's raw entries publish m's configured
// prefix value, and prev's prefixed public id stops resolving.
func TestSeedFromRecomputesPublicIDsUnderNewPrefix(t *testing.T) {
	prev := newManager(testCfg(), &fakeClient{resp: pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(`{"data":[{"id":"glm-5.2"}]}`),
	}})
	mustRefresh(t, prev)
	if got := findModel(t, prev.Models(), "glm-5.2").PublicID; got != "opencode-go/glm-5.2" {
		t.Fatalf("precondition: prev PublicID = %q", got)
	}

	cfg := testCfg()
	cfg.ModelPrefix.Value = "og"
	m := newManager(cfg, &fakeClient{})
	m.SeedFrom(prev)

	got := m.Models()
	if len(got) != 1 || got[0].UpstreamID != "glm-5.2" || got[0].PublicID != "og/glm-5.2" {
		t.Fatalf("seeded models = %+v, want glm-5.2 republished as og/glm-5.2", got)
	}
	if rec, ok := m.Lookup("og/glm-5.2"); !ok || rec.UpstreamID != "glm-5.2" {
		t.Errorf("new-prefix lookup = %+v %v", rec, ok)
	}
	if _, ok := m.Lookup("opencode-go/glm-5.2"); ok {
		t.Error("old-prefix public id must stop resolving after seed")
	}
}

// TestSwapPublicUpstreamCrossKeyCollision pins the index-collision fix: with
// the prefix enabled, upstream "foo" publishes public "opencode-go/foo" while
// an upstream literally named "opencode-go/foo" claims that same key as its
// UpstreamID; last-write-wins would silently misroute one of them. First in
// catalog order wins, warning is emitted, loser lands in Unsupported.
func TestSwapPublicUpstreamCrossKeyCollision(t *testing.T) {
	const wantMsg = `public id "opencode-go/foo" collides with upstream id "opencode-go/foo"`
	for _, order := range []string{"foo first", "opencode-go/foo first"} {
		t.Run(order, func(t *testing.T) {
			ids := []string{"foo", "opencode-go/foo"}
			if order == "opencode-go/foo first" {
				ids[0], ids[1] = ids[1], ids[0]
			}
			body := fmt.Sprintf(`{"data":[{"id":%q},{"id":%q}]}`, ids[0], ids[1])
			fc := &fakeClient{resp: pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)}}
			cfg := testCfg()
			declare(&cfg, ids[0], proto("chat-completions", ""))
			declare(&cfg, ids[1], proto("messages", ""))
			m := newManager(cfg, fc)
			mustRefresh(t, m)

			models := m.Models()
			if len(models) != 1 || models[0].UpstreamID != ids[0] || models[0].PublicID != "opencode-go/"+ids[0] {
				t.Fatalf("models = %+v, want single winner %q in catalog order", models, ids[0])
			}
			if rec, ok := m.Lookup("opencode-go/foo"); !ok || rec.UpstreamID != ids[0] {
				t.Fatalf("contested key resolves to %+v (%v), want the first-in-order record", rec, ok)
			}
			foundWarn := false
			for _, w := range m.Warnings() {
				if w == wantMsg {
					foundWarn = true
				}
			}
			if !foundWarn {
				t.Fatalf("warnings = %v, want %q", m.Warnings(), wantMsg)
			}
			unsup := m.Unsupported()
			if len(unsup) != 1 || unsup[0].UpstreamID != ids[1] || unsup[0].Reason != wantMsg {
				t.Fatalf("unsupported = %+v, want loser %q with %q", unsup, ids[1], wantMsg)
			}
		})
	}
}

// TestRefreshDataKeyAbsentVsEmpty pins the absence-vs-empty split: a body
// without a "data" key is upstream shape drift — the snapshot clears like an
// empty plan AND a diagnostic is recorded — while a present-but-empty array
// is an intended clear that stays silent.
func TestRefreshDataKeyAbsentVsEmpty(t *testing.T) {
	fc := &fakeClient{resp: pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(`{"data":[{"id":"glm-5.2"}]}`),
	}}
	m := newManager(testCfg(), fc)
	mustRefresh(t, m)

	const driftWarn = `upstream catalog response missing "data" field`

	// Key absent: cleared snapshot plus shape-drift warning.
	fc.set(pluginapi.HTTPResponse{
		StatusCode: 200,
		Body:       []byte(`{"object":"list","url":"/v1/models"}`),
	}, nil)
	mustRefresh(t, m)
	if got := m.Models(); len(got) != 0 {
		t.Fatalf("absent data key kept stale models: %+v", got)
	}
	found := false
	for _, w := range m.Warnings() {
		if w == driftWarn {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want %q", m.Warnings(), driftWarn)
	}

	// Key present but empty array: intended clear, no warning.
	fc.set(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"data":[]}`)}, nil)
	mustRefresh(t, m)
	if got := m.Models(); len(got) != 0 {
		t.Fatalf("empty plan did not clear: %+v", got)
	}
	if got := m.Warnings(); len(got) != 0 {
		t.Fatalf("intended clear must stay silent, got %v", got)
	}
}
