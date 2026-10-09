package thinking

import "testing"

func TestParseSuffix(t *testing.T) {
	for _, tc := range []struct {
		model     string
		wantModel string
		raw       string
		has       bool
		effort    string
	}{
		{"glm-5.2", "glm-5.2", "", false, ""},
		{"opencode-go/glm-5.2(high)", "opencode-go/glm-5.2", "high", true, "high"},
		{"glm-5.2(HIGH)", "glm-5.2", "HIGH", true, "high"},
		{"glm-5.2(16384)", "glm-5.2", "16384", true, "high"},
		{"glm-5.2(08192)", "glm-5.2", "08192", true, "medium"},
		{"glm-5.2(0)", "glm-5.2", "0", true, "none"},
		{"glm-5.2(-1)", "glm-5.2", "-1", true, "auto"},
		{"glm-5.2(none)", "glm-5.2", "none", true, "none"},
		{"glm-5.2(auto)", "glm-5.2", "auto", true, "auto"},
		{"glm-5.2(max)", "glm-5.2", "max", true, "max"},
		{"glm-5.2(xhigh)", "glm-5.2", "xhigh", true, "xhigh"},
		{"glm-5.2(24577)", "glm-5.2", "24577", true, "xhigh"},
		{"glm-5.2(turbo)", "glm-5.2", "turbo", true, ""},
		{"glm-5.2(-2)", "glm-5.2", "-2", true, ""},
		{"glm-5.2()", "glm-5.2", "", true, ""},
		{"glm-5.2( high )", "glm-5.2", " high ", true, ""},
		{"glm-5.2(9223372036854775808)", "glm-5.2", "9223372036854775808", true, ""},
		// CPA strips a suffix only when the name ends with ")".
		{"glm-5.2(high", "glm-5.2(high", "", false, ""},
		{"glm-5.2)", "glm-5.2)", "", false, ""},
		// The last pair wins, matching CPA's LastIndex parse.
		{"minimax(m3)(low)", "minimax(m3)", "low", true, "low"},
	} {
		got := ParseSuffix(tc.model)
		if got.Model != tc.wantModel || got.Raw != tc.raw || got.HasSuffix != tc.has || got.Effort != tc.effort {
			t.Errorf("ParseSuffix(%q) = %+v, want model=%q raw=%q has=%t effort=%q",
				tc.model, got, tc.wantModel, tc.raw, tc.has, tc.effort)
		}
	}
}
