// Package protocol bridges CPA format names and executor payloads to api-translator.
package protocol

import (
	"strings"

	translator "github.com/dillonzq/api-translator"
	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
)

func Format(format string) (string, *errclass.Error) {
	switch format {
	case "openai":
		return translator.FormatChatCompletions, nil
	case "claude":
		return translator.FormatMessages, nil
	case "openai-response":
		return translator.FormatResponses, nil
	default:
		return "", &errclass.Error{Class: errclass.ClassUnsupported, Message: "unsupported protocol format: " + format}
	}
}

func Route(route catalog.Route) (string, *errclass.Error) {
	switch route {
	case catalog.RouteChatCompletions:
		return translator.FormatChatCompletions, nil
	case catalog.RouteMessages:
		return translator.FormatMessages, nil
	case catalog.RouteResponses:
		return translator.FormatResponses, nil
	default:
		return "", &errclass.Error{Class: errclass.ClassUnsupported, Message: "unsupported route"}
	}
}

// Error preserves the library classification when crossing the CPA envelope boundary.
func Error(err *translator.Error) *errclass.Error {
	if err == nil {
		return nil
	}
	message := errclass.Redact(err.Message)
	if strings.TrimSpace(message) == "" {
		// CPA treats an empty stream-close error as success. Preserve failure
		// even when the upstream error event provides no readable message.
		message = "protocol conversion failed"
	}
	return &errclass.Error{Class: errclass.Class(err.Class), Message: message, StatusCode: err.StatusCode, Retryable: err.Retryable}
}

func ConvertRequest(route catalog.Route, model, source string, body []byte, state *translator.RequestState) ([]byte, *errclass.Error) {
	target, err := Route(route)
	if err != nil {
		return nil, err
	}
	from, err := Format(source)
	if err != nil {
		return nil, err
	}
	out, e := translator.ConvertRequest(target, model, from, body, state)
	return out, Error(e)
}

func ConvertResponse(route catalog.Route, output string, status int, body []byte, state *translator.RequestState) ([]byte, *errclass.Error) {
	upstream, err := Route(route)
	if err != nil {
		return nil, err
	}
	to, err := Format(output)
	if err != nil {
		return nil, err
	}
	out, e := translator.ConvertResponse(upstream, to, status, body, state)
	return out, Error(e)
}

type StreamConverter interface {
	Feed([]byte) ([][]byte, bool, *errclass.Error)
	Finish() ([][]byte, *errclass.Error)
}

type stream struct {
	converter translator.StreamConverter
	chat      bool
	decoder   translator.SSEDecoder
}

func NewStreamConverter(route catalog.Route, output string, state *translator.RequestState) (StreamConverter, *errclass.Error) {
	upstream, err := Route(route)
	if err != nil {
		return nil, err
	}
	to, err := Format(output)
	if err != nil {
		return nil, err
	}
	converter, e := translator.NewStreamConverter(upstream, to, state)
	if e != nil {
		return nil, Error(e)
	}
	return &stream{converter: converter, chat: to == translator.FormatChatCompletions}, nil
}

// CPA frames Chat JSON and writes DONE itself; other protocols accept complete SSE frames.
func (s *stream) hostEvents(frames [][]byte) [][]byte {
	if !s.chat {
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
	if s.chat {
		for _, event := range s.decoder.Finish() {
			if !event.Done && len(event.Data) > 0 {
				out = append(out, event.Data)
			}
		}
	}
	return out, Error(err)
}
