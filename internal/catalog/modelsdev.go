package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/dillonzq/cpa-opencode-go/internal/modelmeta"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const fallbackTimeout = 3 * time.Second
const fallbackResponseBudget = 16 << 20

type modelsDevModel struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Limit       struct {
		Context *int64 `json:"context"`
		Output  *int64 `json:"output"`
	} `json:"limit"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Reasoning        *bool `json:"reasoning"`
	ReasoningOptions []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
		Min    *int     `json:"min"`
		Max    *int     `json:"max"`
	} `json:"reasoning_options"`
}

func (m modelsDevModel) metadata() modelmeta.Metadata {
	out := modelmeta.Metadata{DisplayName: m.Name, Description: m.Description,
		Context: m.Limit.Context, Output: m.Limit.Output,
		InputModalities: m.Modalities.Input, OutputModalities: m.Modalities.Output}
	if m.Reasoning != nil || m.ReasoningOptions != nil {
		t := &modelmeta.Thinking{}
		if m.Reasoning != nil && !*m.Reasoning {
			t.Levels = []string{"none"}
			t.ZeroAllowed = modelmeta.Ptr(true)
		} else {
			for _, option := range m.ReasoningOptions {
				switch option.Type {
				case "effort":
					t.Levels = option.Values
					for _, effort := range option.Values {
						if effort == "none" {
							t.ZeroAllowed = modelmeta.Ptr(true)
						}
					}
				case "budget_tokens":
					t.Min, t.Max = option.Min, option.Max
				case "toggle":
					t.ZeroAllowed = modelmeta.Ptr(true)
				}
			}
		}
		out.Thinking = t
	}
	return out
}

// refreshFallback never forwards upstream credentials to the public metadata service.
// A failed fetch leaves the last good metadata intact and does not fail discovery.
func (m *Manager) refreshFallback(parent context.Context) string {
	m.mu.Lock()
	next := m.fallbackNext
	m.mu.Unlock()
	if time.Now().Before(next) {
		return ""
	}
	ctx, cancel := context.WithTimeout(parent, fallbackTimeout)
	defer cancel()
	resp, err := m.client.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet,
		URL: m.cfg.ModelsDev.URL, Headers: http.Header{"Accept": []string{"application/json"}}})
	if err != nil || resp.StatusCode != http.StatusOK {
		return "models.dev fallback unavailable; retaining previous metadata"
	}
	if len(resp.Body) > fallbackResponseBudget {
		return "models.dev fallback response exceeds size limit"
	}
	var providers map[string]json.RawMessage
	var provider struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if json.Unmarshal(resp.Body, &providers) != nil || json.Unmarshal(providers["opencode-go"], &provider) != nil || provider.Models == nil {
		return "models.dev fallback has invalid opencode-go metadata"
	}
	fallback := make(map[string]modelmeta.Metadata)
	for id, raw := range provider.Models {
		var model modelsDevModel
		if json.Unmarshal(raw, &model) != nil {
			continue
		}
		metadata := model.metadata()
		if metadata.Context != nil && *metadata.Context < 0 || metadata.Output != nil && *metadata.Output < 0 {
			continue
		}
		fallback[id] = metadata
	}
	m.mu.Lock()
	m.fallback = fallback
	m.fallbackNext = time.Now().Add(m.cfg.ModelsDev.RefreshInterval)
	m.mu.Unlock()
	return ""
}
