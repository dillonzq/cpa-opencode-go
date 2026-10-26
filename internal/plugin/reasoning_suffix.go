package plugin

import (
	"github.com/dillonzq/apix/reasoning"
	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
	"github.com/dillonzq/cpa-opencode-go/internal/protocol"
)

// buildUpstreamBody translates the effective payload for the resolved route and
// then applies the requested model-name thinking suffix. CPA consumes a suffix
// only on its own executor paths, so a plugin executor receives the raw ID: the
// plugin strips the suffix for routing and applies the equivalent control
// itself, with suffix priority over the body — in CPA the suffix selects the
// configuration and the body's own control is never consumed.
func buildUpstreamBody(res *resolvedExecution, req executorRequest) ([]byte, *errclass.Error) {
	source := req.effectivePayload()
	if res.suffix.Effort != "" {
		// A recognized suffix replaced the client's control, so that control
		// must not be translated: a value the target cannot represent would
		// fail the conversion before the override below runs.
		source = stripSupersededReasoning(res.route.Protocol, req.inputFormat(), source)
	}
	if res.suffix.Effort == "" && res.route.Protocol == catalog.RouteChatCompletions && req.inputFormat() == "claude" {
		// The Messages decoder reads effort only with adaptive thinking.
		// Make a standalone none explicit before conversion can discard it.
		source = normalizeDeepSeekThinking(catalog.RouteMessages, res.rec.UpstreamID, source)
	}
	body, eErr := buildUpstreamRequest(res.route.Protocol, res.rec.UpstreamID, req.inputFormat(), source, res.rec.Thinking, res.state)
	if eErr != nil {
		return nil, eErr
	}
	body, eErr = applyReasoningSuffix(res.route.Protocol, body, res.suffix)
	if eErr != nil {
		return nil, eErr
	}
	return normalizeDeepSeekThinking(res.route.Protocol, res.rec.UpstreamID, body), nil
}

// Keep the host format bridge here; generic reasoning transformations belong
// to the library, with DeepSeek gateway compatibility applied separately.
func stripSupersededReasoning(route catalog.Route, source string, body []byte) []byte {
	target, err := protocol.Route(route)
	if err != nil {
		return body
	}
	from, err := protocol.Format(source)
	if err != nil {
		return body
	}
	return reasoning.StripSupersededReasoning(target, from, body)
}

func applyReasoningSuffix(route catalog.Route, body []byte, suffix reasoning.Suffix) ([]byte, *errclass.Error) {
	target, err := protocol.Route(route)
	if err != nil {
		return body, nil
	}
	out, e := reasoning.ApplySuffix(target, body, suffix)
	return out, protocol.Error(e)
}
