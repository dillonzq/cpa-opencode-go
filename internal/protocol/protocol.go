// Package protocol bridges CPA format names and executor payloads to apix.
package protocol

import (
	"strings"

	"github.com/dillonzq/apix"
	"github.com/dillonzq/apix/protocol/anthropic"
	"github.com/dillonzq/apix/protocol/gemini"
	"github.com/dillonzq/apix/protocol/openai"
	"github.com/dillonzq/apix/sse"
	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
)

var registry = mustRegistry(openai.ChatCompletionsCodec(), openai.ResponsesCodec(), anthropic.MessagesCodec(), gemini.GenerateContentCodec())

func mustRegistry(codecs ...apix.Codec) *apix.Registry {
	reg, err := apix.NewRegistry(codecs...)
	if err != nil {
		panic(err)
	}
	return reg
}

func Format(format string) (apix.Protocol, *errclass.Error) {
	switch format {
	case "openai":
		return openai.ChatCompletions, nil
	case "claude":
		return anthropic.Messages, nil
	case "openai-response":
		return openai.Responses, nil
	case "gemini":
		return gemini.GenerateContent, nil
	default:
		return "", &errclass.Error{Class: errclass.ClassUnsupported, Message: "unsupported protocol format: " + format}
	}
}

func Route(route catalog.Route) (apix.Protocol, *errclass.Error) {
	switch route {
	case catalog.RouteChatCompletions:
		return openai.ChatCompletions, nil
	case catalog.RouteMessages:
		return anthropic.Messages, nil
	case catalog.RouteResponses:
		return openai.Responses, nil
	case catalog.RouteGemini:
		return gemini.GenerateContent, nil
	default:
		return "", &errclass.Error{Class: errclass.ClassUnsupported, Message: "unsupported route"}
	}
}

// Error preserves the library classification when crossing the CPA envelope boundary.
func Error(err error) *errclass.Error {
	e := apix.AsError(err)
	if e == nil {
		return nil
	}
	message := errclass.Redact(e.Message)
	if strings.TrimSpace(message) == "" {
		// CPA treats an empty stream-close error as success. Preserve failure
		// even when the upstream error event provides no readable message.
		message = "protocol conversion failed"
	}
	return &errclass.Error{Class: errclass.Class(e.Class), Message: message, StatusCode: e.StatusCode, Retryable: e.Retryable}
}

// State holds one execution's apix exchange. Its zero value is ready to use.
// The first request conversion creates the exchange, which fixes the upstream
// and output protocols that response conversion must then use.
type State struct {
	// LossPolicy applies to the request conversion.
	LossPolicy apix.LossPolicy
	// Output is the host format responses convert to; empty means the
	// request's source format.
	Output string
	// NormalizeTools adapts native Responses requests for upstreams that only
	// accept function tools.
	NormalizeTools bool
	// Stream is the host's streaming mode. GenerateContent carries it in the
	// URL rather than the body, so only a Gemini source request takes it from
	// here; other sources keep their body's flag.
	Stream bool

	ex               apix.Exchange
	upstream, output apix.Protocol
}

func (s *State) Diagnostics() []apix.Diagnostic {
	if s == nil || s.ex == nil {
		return nil
	}
	return s.ex.Diagnostics()
}

func newExchange(upstream, client, output apix.Protocol, opts apix.ExchangeOptions) (apix.Exchange, *errclass.Error) {
	opts.Output = output
	ex, err := registry.NewExchange(client, upstream, opts)
	return ex, Error(err)
}

func (s *State) start(upstream, client apix.Protocol) *errclass.Error {
	output := client
	if s.Output != "" {
		var err *errclass.Error
		if output, err = Format(s.Output); err != nil {
			return err
		}
	}
	opts := apix.ExchangeOptions{LossPolicy: s.LossPolicy}
	if upstream == openai.Responses && s.NormalizeTools {
		opts.Ext = openai.ResponsesOptions{NormalizeTools: true}
	}
	ex, err := newExchange(upstream, client, output, opts)
	if err != nil {
		return err
	}
	s.ex, s.upstream, s.output = ex, upstream, output
	return nil
}

// exchange returns the state's exchange for responses from upstream to
// output. Without a converted request, a stateless exchange serves them.
func (s *State) exchange(upstream, output apix.Protocol) (apix.Exchange, *errclass.Error) {
	if s == nil || s.ex == nil {
		return newExchange(upstream, output, output, apix.ExchangeOptions{})
	}
	if s.upstream != upstream || s.output != output {
		return nil, &errclass.Error{Class: errclass.ClassUnsupported, Message: "conversion state belongs to another route or output format"}
	}
	return s.ex, nil
}

func ConvertRequest(route catalog.Route, model, source string, body []byte, state *State) ([]byte, *errclass.Error) {
	target, err := Route(route)
	if err != nil {
		return nil, err
	}
	from, err := Format(source)
	if err != nil {
		return nil, err
	}
	if state == nil {
		state = &State{}
	}
	if err := state.start(target, from); err != nil {
		return nil, err
	}
	payload := apix.Payload{Body: body}
	if from == gemini.GenerateContent {
		payload.Metadata.Stream = &state.Stream
	}
	out, e := state.ex.ConvertRequest(payload, model)
	return out.Body, Error(e)
}

func ConvertResponse(route catalog.Route, output string, status int, body []byte, state *State) ([]byte, *errclass.Error) {
	upstream, err := Route(route)
	if err != nil {
		return nil, err
	}
	to, err := Format(output)
	if err != nil {
		return nil, err
	}
	ex, err := state.exchange(upstream, to)
	if err != nil {
		return nil, err
	}
	out, e := ex.ConvertResponse(status, apix.Payload{Body: body})
	return out.Body, Error(e)
}

type StreamConverter interface {
	Feed([]byte) ([][]byte, bool, *errclass.Error)
	Finish() ([][]byte, *errclass.Error)
}

type stream struct {
	converter apix.StreamConverter
	bareJSON  bool
	decoder   sse.Decoder
}

func NewStreamConverter(route catalog.Route, output string, state *State) (StreamConverter, *errclass.Error) {
	upstream, err := Route(route)
	if err != nil {
		return nil, err
	}
	to, err := Format(output)
	if err != nil {
		return nil, err
	}
	ex, err := state.exchange(upstream, to)
	if err != nil {
		return nil, err
	}
	converter, e := ex.NewStreamConverter()
	if e != nil {
		return nil, Error(e)
	}
	return &stream{converter: converter, bareJSON: to == openai.ChatCompletions || to == gemini.GenerateContent}, nil
}

// CPA frames Chat and Gemini JSON itself (and writes the Chat DONE); other
// protocols accept complete SSE frames.
func (s *stream) hostEvents(frames [][]byte) [][]byte {
	if !s.bareJSON {
		return frames
	}
	var out [][]byte
	for _, frame := range frames {
		for _, event := range s.decoder.Feed(frame) {
			if !event.Done && len(event.Data) > 0 {
				out = append(out, event.Data)
			}
		}
	}
	return out
}

func (s *stream) Feed(chunk []byte) ([][]byte, bool, *errclass.Error) {
	events, done, err := s.converter.Feed(chunk)
	return s.hostEvents(events), done, Error(err)
}

func (s *stream) Finish() ([][]byte, *errclass.Error) {
	events, err := s.converter.Finish()
	out := s.hostEvents(events)
	if s.bareJSON {
		for _, event := range s.decoder.Finish() {
			if !event.Done && len(event.Data) > 0 {
				out = append(out, event.Data)
			}
		}
	}
	return out, Error(err)
}
