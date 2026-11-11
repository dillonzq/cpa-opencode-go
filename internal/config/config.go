// Package config loads and validates the opencode-go plugin configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dillonzq/cpa-opencode-go/internal/modelmeta"

	"gopkg.in/yaml.v3"
)

// Defaults.
const (
	DefaultBaseURL          = "https://opencode.ai/inference/go"
	DefaultModelPrefix      = "opencode-go"
	DefaultRefreshInterval  = 15 * time.Minute
	DefaultRequestTimeout   = 5 * time.Minute
	DefaultStreamIdle       = 5 * time.Minute
	DefaultMaxStream        = time.Hour
	DefaultMaxResponseBytes = int64(67108864) // 64 MiB
)

type ModelPrefix struct {
	Enabled bool
	Value   string
}

type APIKey struct {
	Name  string
	Value string
}

type Catalog struct {
	RefreshInterval       time.Duration
	StaleWhileUnavailable bool
}

type Config struct {
	Models         []ModelOverride
	ModelsDev      ModelsDev
	BaseURL        string
	CatalogURL     string
	ModelPrefix    ModelPrefix
	APIKeys        []APIKey
	Catalog        Catalog
	Protocols      map[string]ProtocolOverride // endpoints for every model; switches live on models
	AllowHTTP      bool
	RequestTimeout time.Duration
	// StreamIdleTimeout bounds the silence between upstream stream chunks;
	// each chunk restarts it.
	StreamIdleTimeout time.Duration
	// MaxStreamDuration bounds one stream's whole lifetime, opening included.
	MaxStreamDuration time.Duration
	MaxResponseBytes  int64
}

type ModelOverride struct {
	Name string `yaml:"name"`
	// Alias replaces the upstream name in the published model ID; the
	// model prefix still applies. Requests still send Name upstream.
	Alias string `yaml:"alias"`
	// Protocols patches the built-in protocol set, keyed by protocol: an
	// entry switches its protocol on unless Enabled is false, and a
	// non-empty Endpoint overrides the global path.
	Protocols          map[string]ProtocolOverride `yaml:"protocols"`
	modelmeta.Metadata `yaml:",inline"`
}

// ProtocolOverride is one protocols entry.
type ProtocolOverride struct {
	Enabled  *bool  `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"`
}

// IsEnabled reports whether the entry switches its protocol on.
func (p ProtocolOverride) IsEnabled() bool {
	return p.Enabled == nil || *p.Enabled
}

// ModelPlaceholder marks where a model-dependent endpoint path (Gemini)
// carries the upstream model ID.
const ModelPlaceholder = "{model}"

type ModelsDev struct {
	Enabled         bool
	URL             string
	RefreshInterval time.Duration
}

// rawConfig mirrors the YAML shape; pointer fields distinguish "unset"
// (apply default) from explicitly-set values including "" (validate as-is).
// Host-owned plugin controls are accepted alongside plugin settings.
type rawConfig struct {
	Enabled   *bool           `yaml:"enabled"`
	Priority  int             `yaml:"priority"`
	Store     map[string]any  `yaml:"store"`
	Models    []ModelOverride `yaml:"models"`
	ModelsDev struct {
		Enabled         *bool   `yaml:"enabled"`
		URL             *string `yaml:"url"`
		RefreshInterval *string `yaml:"refresh-interval"`
	} `yaml:"models-dev"`
	BaseURL          *string                     `yaml:"base-url"`
	CatalogURL       *string                     `yaml:"catalog-url"`
	ModelPrefix      rawPrefix                   `yaml:"model-prefix"`
	APIKeys          []rawAPIKey                 `yaml:"api-keys"`
	Catalog          rawCatalog                  `yaml:"catalog"`
	Protocols        map[string]ProtocolOverride `yaml:"protocols"`
	AllowHTTP        bool                        `yaml:"allow-http"`
	RequestTimeout   *string                     `yaml:"request-timeout"`
	StreamIdle       *string                     `yaml:"stream-idle-timeout"`
	MaxStream        *string                     `yaml:"max-stream-duration"`
	MaxResponseBytes *int64                      `yaml:"max-response-bytes"`
	// Removed settings stay accepted and ignored so older configurations
	// keep loading; release notes promise this.
	Routing        any `yaml:"routing"`
	ExcludedModels any `yaml:"excluded-models"`
	RouteOverrides any `yaml:"route-overrides"`
}

type rawPrefix struct {
	Enabled *bool   `yaml:"enabled"`
	Value   *string `yaml:"value"`
}

type rawAPIKey struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
	// Priority was removed with the plugin scheduler; it is ignored.
	Priority any `yaml:"priority"`
}

type rawCatalog struct {
	RefreshInterval       *string `yaml:"refresh-interval"`
	StaleWhileUnavailable *bool   `yaml:"stale-while-unavailable"`
}

var (
	validProtocols = map[string]bool{"chat-completions": true, "messages": true, "responses": true, "gemini": true}
)

// Load decodes YAML, expands ${VAR} references in api-keys[].value only,
// applies defaults, and validates. Unknown settings are rejected. Decode
// errors never echo decoded node values — a malformed entry (e.g. a
// bare-scalar API key) must not leak into the invalid_config envelope the
// host logs.
func Load(yamlBytes []byte) (Config, error) {
	var raw rawConfig
	decoder := yaml.NewDecoder(bytes.NewReader(yamlBytes))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		if n := regexp.MustCompile(`line (\d+)`).FindStringSubmatch(err.Error()); n != nil {
			return Config{}, fmt.Errorf("decode config: invalid YAML structure near line %s", n[1])
		}
		return Config{}, fmt.Errorf("decode config: invalid YAML structure")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode config: expected one YAML document")
	}
	keys := make([]APIKey, len(raw.APIKeys))
	for i, k := range raw.APIKeys {
		keys[i] = APIKey{Value: strings.TrimSpace(os.ExpandEnv(k.Value)), Name: strings.TrimSpace(k.Name)}
	}
	refreshInterval, err := parseDuration("catalog.refresh-interval", raw.Catalog.RefreshInterval, DefaultRefreshInterval)
	if err != nil {
		return Config{}, err
	}
	requestTimeout, err := parsePositiveDuration("request-timeout", raw.RequestTimeout, DefaultRequestTimeout)
	if err != nil {
		return Config{}, err
	}
	streamIdle, err := parsePositiveDuration("stream-idle-timeout", raw.StreamIdle, DefaultStreamIdle)
	if err != nil {
		return Config{}, err
	}
	maxStream, err := parsePositiveDuration("max-stream-duration", raw.MaxStream, DefaultMaxStream)
	if err != nil {
		return Config{}, err
	}
	c := Config{
		Models:    raw.Models,
		ModelsDev: ModelsDev{Enabled: orDefault(raw.ModelsDev.Enabled, true), URL: orDefault(raw.ModelsDev.URL, "https://models.dev/api.json")},
		BaseURL:   normalizeBaseURL(orDefault(raw.BaseURL, DefaultBaseURL)),
		ModelPrefix: ModelPrefix{
			Enabled: orDefault(raw.ModelPrefix.Enabled, true),
			Value:   orDefault(raw.ModelPrefix.Value, DefaultModelPrefix),
		},
		APIKeys: keys,
		Catalog: Catalog{
			RefreshInterval:       refreshInterval,
			StaleWhileUnavailable: orDefault(raw.Catalog.StaleWhileUnavailable, true),
		},
		Protocols:         raw.Protocols,
		AllowHTTP:         raw.AllowHTTP,
		RequestTimeout:    requestTimeout,
		StreamIdleTimeout: streamIdle,
		MaxStreamDuration: maxStream,
		MaxResponseBytes:  orDefault(raw.MaxResponseBytes, DefaultMaxResponseBytes),
	}
	c.ModelsDev.RefreshInterval, err = parseDuration("models-dev.refresh-interval", raw.ModelsDev.RefreshInterval, 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	if raw.CatalogURL != nil {
		// Mirror the derived-default trim so an explicit trailing-slash
		// catalog-url cannot double up separators downstream.
		c.CatalogURL = strings.TrimRight(*raw.CatalogURL, "/")
	} else {
		c.CatalogURL = c.BaseURL + "/v1/models"
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// normalizeBaseURL drops trailing slashes and a trailing /v1: endpoint paths
// carry their own version segment, and earlier releases documented a base-url
// ending in /v1.
func normalizeBaseURL(raw string) string {
	return strings.TrimSuffix(strings.TrimRight(raw, "/"), "/v1")
}

// PublicID returns the client-facing model ID.
func PublicID(c Config, upstreamID string) string {
	if c.ModelPrefix.Enabled {
		return c.ModelPrefix.Value + "/" + upstreamID
	}
	return upstreamID
}

// HasAPIKey reports whether key is currently configured. Cleanup keeps the
// plugin-created credential files of configured keys.
func (c Config) HasAPIKey(key string) bool {
	for _, entry := range c.APIKeys {
		if entry.Value == key {
			return true
		}
	}
	return false
}

func (c Config) validate() error {
	if err := validateURL("models-dev.url", c.ModelsDev.URL, c.AllowHTTP); err != nil {
		return err
	}
	if c.ModelsDev.RefreshInterval < time.Minute {
		return fmt.Errorf("models-dev.refresh-interval: must be at least 1m")
	}
	if err := validateProtocols("protocols", c.Protocols, false); err != nil {
		return err
	}
	seenModels := map[string]bool{}
	seenAliases := map[string]bool{}
	for i, model := range c.Models {
		if strings.TrimSpace(model.Name) == "" || seenModels[model.Name] {
			return fmt.Errorf("models[%d].name: must be non-empty and unique", i)
		}
		seenModels[model.Name] = true
		if model.Alias != "" {
			if strings.TrimSpace(model.Alias) != model.Alias || seenAliases[model.Alias] {
				return fmt.Errorf("models[%d].alias: must be unique without surrounding whitespace", i)
			}
			seenAliases[model.Alias] = true
		}
		if model.Context != nil && *model.Context < 0 || model.Output != nil && *model.Output < 0 {
			return fmt.Errorf("models[%d]: token limits must be non-negative", i)
		}
		if model.Thinking != nil {
			t := model.Thinking
			if t.Min != nil && *t.Min < 0 || t.Max != nil && *t.Max < 0 || t.Min != nil && t.Max != nil && *t.Min > *t.Max {
				return fmt.Errorf("models[%d].thinking: invalid budget range", i)
			}
		}
		if err := validateProtocols(fmt.Sprintf("models[%d].protocols", i), model.Protocols, true); err != nil {
			return err
		}
	}
	if err := validateURL("base-url", c.BaseURL, c.AllowHTTP); err != nil {
		return err
	}
	if err := validateURL("catalog-url", c.CatalogURL, c.AllowHTTP); err != nil {
		return err
	}
	for i, k := range c.APIKeys {
		if k.Value == "" {
			return fmt.Errorf("api-keys[%d].value: expanded to empty", i)
		}
		if strings.ContainsAny(k.Value, "\r\n") {
			return fmt.Errorf("api-keys[%d]: headers must not contain newlines", i)
		}
	}
	seen := make(map[string]bool, len(c.APIKeys))
	for _, k := range c.APIKeys {
		if seen[k.Value] {
			return fmt.Errorf("api-keys: duplicate key values are not allowed")
		}
		seen[k.Value] = true
	}
	if c.Catalog.RefreshInterval < time.Minute {
		return fmt.Errorf("catalog.refresh-interval: must be at least 1m")
	}
	if c.MaxResponseBytes <= 0 {
		return fmt.Errorf("max-response-bytes: must be positive")
	}
	if c.ModelPrefix.Enabled && !validPrefix(c.ModelPrefix.Value) {
		return fmt.Errorf("model-prefix.value: invalid provider-ID characters %q", c.ModelPrefix.Value)
	}
	return nil
}

// validateProtocols checks one protocols map. Only model entries may switch
// a protocol; global entries set paths.
func validateProtocols(field string, protocols map[string]ProtocolOverride, switchable bool) error {
	for _, name := range slices.Sorted(maps.Keys(protocols)) {
		p := protocols[name]
		if !validProtocols[name] {
			return fmt.Errorf("%s: unsupported protocol %q", field, name)
		}
		if p.Enabled != nil && !switchable {
			return fmt.Errorf("%s.%s: only endpoint is allowed; switch protocols under models[].protocols", field, name)
		}
		if p.Endpoint == "" {
			continue
		}
		if !strings.HasPrefix(p.Endpoint, "/") {
			return fmt.Errorf("%s.%s.endpoint: must start with /", field, name)
		}
		if name == "gemini" && !strings.Contains(p.Endpoint, ModelPlaceholder) {
			return fmt.Errorf("%s.%s.endpoint: gemini endpoint must contain %s", field, name, ModelPlaceholder)
		}
	}
	return nil
}

func validateURL(name, raw string, allowHTTP bool) error {
	if raw == "" {
		return fmt.Errorf("%s: must not be empty", name)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: invalid URL", name)
	}
	if u.Scheme == "" || u.Host == "" {
		// Echo only scheme://host — the configured string may embed
		// userinfo credentials (https://user:key@host).
		return fmt.Errorf("%s: invalid URL %s://%s", name, u.Scheme, u.Host)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%s: must not contain query, fragment, or userinfo", name)
	}
	// Scheme allowlist: https always; http only behind
	// allow-http; anything else (ftp://, file://, custom schemes) is
	// rejected at load instead of failing at transport time.
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return fmt.Errorf("%s: http scheme requires allow-http", name)
		}
	default:
		return fmt.Errorf("%s: unsupported scheme %q; use https", name, u.Scheme)
	}
	return nil
}

func validPrefix(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 && !alnum {
			return false
		}
		if !alnum && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func parseDuration(name string, p *string, def time.Duration) (time.Duration, error) {
	if p == nil {
		return def, nil
	}
	d, err := time.ParseDuration(*p)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration", name)
	}
	return d, nil
}

func parsePositiveDuration(name string, p *string, def time.Duration) (time.Duration, error) {
	d, err := parseDuration(name, p, def)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be positive", name)
	}
	return d, nil
}

func orDefault[T any](p *T, def T) T {
	if p != nil {
		return *p
	}
	return def
}
