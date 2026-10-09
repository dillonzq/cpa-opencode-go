package thinking

import (
	"strconv"
	"strings"
)

// Suffix is a CPA model-name thinking suffix: the trailing "model(value)"
// form CPA parses out of a requested model ID. CPA consumes the suffix only
// on its own executor paths, so a plugin executor that receives the raw ID
// must strip the suffix itself and apply the equivalent reasoning control.
type Suffix struct {
	// Model is the requested model name with the suffix removed.
	Model string
	// Raw is the value between the parentheses; empty unless HasSuffix.
	Raw string
	// HasSuffix reports whether the name ended with "(value)".
	HasSuffix bool
	// Effort is the canonical reasoning effort the raw value selects
	// (none, auto, minimal, low, medium, high, xhigh, max), or "" when the
	// value is not a recognized suffix. CPA strips an unrecognized suffix
	// and applies no configuration, so "" means "strip only".
	Effort string
}

// ParseSuffix splits a CPA thinking suffix off a model name, mirroring CPA
// internal/thinking.ParseSuffix: the last "(" of a name that ends with ")"
// opens the suffix. The content is not validated here; Effort is "" for
// values CPA would not interpret.
func ParseSuffix(model string) Suffix {
	lastOpen := strings.LastIndex(model, "(")
	if lastOpen < 0 || !strings.HasSuffix(model, ")") {
		return Suffix{Model: model}
	}
	raw := model[lastOpen+1 : len(model)-1]
	return Suffix{Model: model[:lastOpen], Raw: raw, HasSuffix: true, Effort: suffixEffort(raw)}
}

// suffixEffort interprets a raw suffix value, mirroring CPA
// parseSuffixToConfig plus reasoningEffortFromConfig: the special none/auto
// values ("-1" is auto), the discrete levels, and non-negative numeric
// budgets converted through the fixed budget thresholds. Anything else —
// including negative numbers other than -1 — is not a suffix value.
func suffixEffort(raw string) string {
	switch strings.ToLower(raw) {
	case "none":
		return "none"
	case "auto", "-1":
		return "auto"
	case "minimal", "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(raw)
	}
	budget, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || budget < 0 {
		return ""
	}
	effort, ok := EffortFromBudget(budget)
	if !ok {
		return ""
	}
	return effort
}
