package plugin

import "github.com/dillonzq/cpa-opencode-go/internal/catalog"

// routePreference orders upstream protocols for each host input format: the
// format's own protocol first (no conversion), then the others by increasing
// semantic loss of the request conversion (apix docs/protocols/llm.md and
// gemini.md). Responses keeps reasoning_text, OpenAI file IDs, and hosted
// search shared with Messages; Messages has no audio, video, or file IDs;
// Chat cannot carry URI-only documents; Gemini differs most in signatures
// and thinking, so it comes last for every other client.
var routePreference = map[string][]catalog.Route{
	"openai":          {catalog.RouteChatCompletions, catalog.RouteResponses, catalog.RouteMessages, catalog.RouteGemini},
	"openai-response": {catalog.RouteResponses, catalog.RouteChatCompletions, catalog.RouteMessages, catalog.RouteGemini},
	"claude":          {catalog.RouteMessages, catalog.RouteResponses, catalog.RouteChatCompletions, catalog.RouteGemini},
	"gemini":          {catalog.RouteGemini, catalog.RouteResponses, catalog.RouteChatCompletions, catalog.RouteMessages},
}

// selectRoute picks the record's upstream for one request. An unknown format
// takes the primary route so conversion reports the unsupported format.
func selectRoute(rec catalog.ModelRecord, format string) catalog.Target {
	for _, route := range routePreference[format] {
		if t, ok := rec.Target(route); ok {
			return t
		}
	}
	return rec.Routes[0]
}
