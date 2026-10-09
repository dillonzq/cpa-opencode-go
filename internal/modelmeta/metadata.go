// Package modelmeta merges descriptive metadata without using it to validate requests.
package modelmeta

import "github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

// Metadata uses CPA provider configuration names for user overrides.
// Pointers distinguish missing fields from explicit zero/false values.
type Metadata struct {
	DisplayName      *string   `yaml:"display-name"`
	Description      *string   `yaml:"description"`
	Context          *int64    `yaml:"max-context-length"`
	Output           *int64    `yaml:"max-tokens"`
	InputModalities  []string  `yaml:"input-modalities"`
	OutputModalities []string  `yaml:"output-modalities"`
	Thinking         *Thinking `yaml:"thinking"`
}

type Thinking struct {
	Min            *int     `yaml:"min"`
	Max            *int     `yaml:"max"`
	ZeroAllowed    *bool    `yaml:"zero-allowed"`
	DynamicAllowed *bool    `yaml:"dynamic-allowed"`
	Levels         []string `yaml:"levels"`
}

// Merge overlays present high-priority fields, including nested thinking.
func Merge(low, high Metadata) Metadata {
	out := low
	if high.DisplayName != nil {
		out.DisplayName = high.DisplayName
	}
	if high.Description != nil {
		out.Description = high.Description
	}
	if high.Context != nil {
		out.Context = high.Context
	}
	if high.Output != nil {
		out.Output = high.Output
	}
	if high.InputModalities != nil {
		out.InputModalities = high.InputModalities
	}
	if high.OutputModalities != nil {
		out.OutputModalities = high.OutputModalities
	}
	if high.Thinking != nil {
		t := Thinking{}
		if low.Thinking != nil {
			t = *low.Thinking
		}
		h := high.Thinking
		if h.Min != nil {
			t.Min = h.Min
		}
		if h.Max != nil {
			t.Max = h.Max
		}
		if h.ZeroAllowed != nil {
			t.ZeroAllowed = h.ZeroAllowed
		}
		if h.DynamicAllowed != nil {
			t.DynamicAllowed = h.DynamicAllowed
		}
		if h.Levels != nil {
			t.Levels = h.Levels
		}
		out.Thinking = &t
	}
	return out
}

func (t *Thinking) Support() *pluginapi.ThinkingSupport {
	if t == nil {
		return nil
	}
	out := &pluginapi.ThinkingSupport{Levels: t.Levels}
	if t.Min != nil {
		out.Min = *t.Min
	}
	if t.Max != nil {
		out.Max = *t.Max
	}
	if t.ZeroAllowed != nil {
		out.ZeroAllowed = *t.ZeroAllowed
	}
	if t.DynamicAllowed != nil {
		out.DynamicAllowed = *t.DynamicAllowed
	}
	return out
}

func Value[T any](p *T) (zero T) {
	if p != nil {
		return *p
	}
	return zero
}

func Ptr[T any](value T) *T { return &value }
