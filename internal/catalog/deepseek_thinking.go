package catalog

import (
	"slices"
	"strings"

	"github.com/dillonzq/cpa-opencode-go/internal/modelmeta"
)

// DeepSeek's thinking toggle is separate from its effort levels. Metadata
// sources can list only enabled efforts, which makes CPA clamp none to the
// lowest level before our executor sees it. Publish the toggle explicitly,
// supplementing known levels without inventing efforts when they are unknown.
// User metadata is merged afterward; an explicit opt-out also suppresses the
// added none level so it cannot re-enable the toggle in CPA.
func deepSeekThinkingMetadata(model string, metadata modelmeta.Metadata, override *modelmeta.Thinking) modelmeta.Metadata {
	if !strings.HasPrefix(strings.ToLower(model), "deepseek") ||
		(override != nil && override.ZeroAllowed != nil && !*override.ZeroAllowed) {
		return metadata
	}
	thinking := modelmeta.Thinking{}
	if metadata.Thinking != nil {
		thinking = *metadata.Thinking
	}
	thinking.ZeroAllowed = modelmeta.Ptr(true)
	if len(thinking.Levels) > 0 && !slices.Contains(thinking.Levels, "none") {
		thinking.Levels = append([]string{"none"}, thinking.Levels...)
	}
	metadata.Thinking = &thinking
	return metadata
}
