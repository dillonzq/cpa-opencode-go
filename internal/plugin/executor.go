// Non-stream and stream execution paths (FR-005/FR-006/FR-007, arch §4/§5
// steps 5-8): resolve the public model ID against the catalog snapshot,
// translate the inbound payload to the record's upstream protocol, call
// upstream through the host HTTP callbacks, and translate back to the
// client protocol. Request authentication is selected by CPA.

package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	translator "github.com/dillonzq/api-translator"
	"github.com/dillonzq/api-translator/thinking"
	"github.com/dillonzq/cpa-opencode-go/internal/catalog"
	"github.com/dillonzq/cpa-opencode-go/internal/config"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
	"github.com/dillonzq/cpa-opencode-go/internal/protocol"
)

// executorRequest mirrors rpcExecutorRequest: the SDK embeds
// pluginapi.ExecutorRequest untagged, so its fields marshal under Go field
// names ("Model", "SourceFormat", "OriginalRequest", "Stream"). StreamID is
// the DOWNSTREAM host-allocated id for executor.execute_stream emissions;
// ids returned by DoStream are UPSTREAM and never interchangeable (§4).
type executorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// resolvedExecution carries everything both execution paths need after
// model/key resolution succeeded. suffix is the requested model's thinking
// suffix: a zero value means the model ID carried none, or that a literal
// parenthesized catalog ID won routing and keeps its own identity.
type resolvedExecution struct {
	cfg    config.Config
	rec    catalog.ModelRecord
	key    string
	suffix thinking.Suffix
	state  *translator.RequestState
}

// resolveExecution resolves the requested model against the snapshot and
// extracts the key selected by CPA. A non-nil second return is a ready-made
// failure envelope.
func (m *Manager) resolveExecution(req executorRequest) (*resolvedExecution, []byte) {
	m.mu.RLock()
	cfg, mgr := m.cfg, m.mgr
	m.mu.RUnlock()
	if req.AuthProvider != ProviderID {
		return nil, classEnvelope(&errclass.Error{Class: errclass.ClassAuth, Message: "selected auth provider is not opencode-go"})
	}
	key := strings.TrimSpace(req.AuthAttributes["api_key"])
	debugTrace("executor auth model=%s auth_id=%s provider=%s attr_api_key_present=%t attr_count=%d storage_json_bytes=%d", req.Model, req.AuthID, req.AuthProvider, key != "", len(req.AuthAttributes), len(req.StorageJSON))
	if key == "" {
		return nil, classEnvelope(&errclass.Error{Class: errclass.ClassAuth, Message: "selected auth has no api key"})
	}
	// CPA parses a model-name thinking suffix (model(high)) for its own
	// executors but hands the raw ID to plugins, so resolve the base model and
	// carry the suffix to the route override. A catalog ID that literally
	// carries parentheses still resolves when the base does not, and then keeps
	// its own identity with no suffix override.
	suffix := thinking.ParseSuffix(req.Model)
	lookupID := req.Model
	if suffix.HasSuffix && suffix.Model != "" {
		lookupID = suffix.Model
	}
	var rec catalog.ModelRecord
	var found bool
	if mgr != nil && lookupID != "" {
		rec, found = mgr.Lookup(lookupID)
	}
	if !found && lookupID != req.Model && mgr != nil {
		if rec, found = mgr.Lookup(req.Model); found {
			suffix = thinking.Suffix{}
		}
	}
	if !found {
		return nil, classEnvelope(&errclass.Error{
			Class:      errclass.ClassInvalidModel,
			Message:    "model not in routable catalog",
			StatusCode: http.StatusNotFound,
		})
	}
	debugTrace("executor resolved model lookup=%s suffix_raw=%q suffix_effort=%q", lookupID, suffix.Raw, suffix.Effort)
	return &resolvedExecution{cfg: cfg, rec: rec, key: key, suffix: suffix, state: translator.NewRequestState()}, nil
}

// handleExecute implements executor.execute (non-stream). Stream-flagged
// requests are routed to the stream path instead of rejected.
func (m *Manager) handleExecute(request []byte) ([]byte, error) {
	var req executorRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return ErrEnvelope("invalid_request", "malformed executor request body"), nil
	}
	debugTrace("executor invoked model=%s source_format=%s stream=%t original_body_%s payload_%s", req.Model, req.SourceFormat, req.Stream, debugBodyMeta(req.OriginalRequest), debugBodyMeta(req.Payload))
	if req.Stream {
		return m.executeStream(req)
	}
	res, failEnv := m.resolveExecution(req)
	if res == nil {
		return failEnv, nil
	}
	sessionID, eErr := resolveOpenCodeSessionID(req)
	if eErr != nil {
		return classEnvelope(eErr), nil
	}
	debugTrace("executor session mode=%s source_format=%s x_opencode_session=%s fallback=%t", "non-stream", req.SourceFormat, sessionID, sessionID == emptyOpenCodeSessionID)
	upstreamBody, eErr := buildUpstreamBody(res, req)
	if eErr != nil {
		return classEnvelope(eErr), nil
	}

	if _, eErr := protocol.Format(req.outputFormat()); eErr != nil {
		return classEnvelope(eErr), nil
	}
	url := catalog.JoinUpstreamURL(res.cfg.BaseURL, res.rec.EndpointPath)
	debugTrace("executor resolved public_model=%s upstream_model=%s route=%s url=%s key_count=%d", req.Model, res.rec.UpstreamID, res.rec.Protocol, url, len(res.cfg.APIKeys))
	debugTrace("executor sending non-stream url=%s body_len=%d", url, len(upstreamBody))
	ctx, cancel := context.WithTimeout(withHostCallbackScope(m.workContext(), req.HostCallbackID), res.cfg.RequestTimeout)
	defer cancel()
	resp, err := m.bridge.Do(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     url,
		Headers: upstreamAuthHeaders(res.rec.Protocol, res.key, sessionID),
		Body:    upstreamBody,
	})
	if err != nil {
		debugTrace("executor non-stream network error: %v", err)
		return classEnvelope(errclass.FromNetwork(err)), nil
	}
	debugTrace("executor received non-stream status=%d body_len=%d", resp.StatusCode, len(resp.Body))
	if resp.StatusCode >= 400 {
		return classEnvelope(errclass.UpstreamStatusError(resp.StatusCode, resp.Body)), nil
	}
	// Parse/envelope guard only; true OOM prevention belongs to the host transport's byte cap.
	if int64(len(resp.Body)) > res.cfg.MaxResponseBytes {
		return classEnvelope(errclass.Translation("response exceeds max-response-bytes")), nil
	}
	converted, eErr := convertNonStream(res.rec.Protocol, req.outputFormat(), resp.StatusCode, resp.Body, res.state)
	if eErr != nil {
		return classEnvelope(eErr), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: converted, Headers: resp.Headers}), nil
}

// buildUpstreamRequest applies provider-specific native policy before translation.
func buildUpstreamRequest(route catalog.Route, model, source string, body []byte, _ *pluginapi.ThinkingSupport, states ...*translator.RequestState) ([]byte, *errclass.Error) {
	state := requestState(states)
	body, err := normalizeNativeRequest(route, model, source, body, state)
	if err != nil {
		return nil, err
	}
	return protocol.ConvertRequest(route, model, source, body, state)
}

func requestState(states []*translator.RequestState) *translator.RequestState {
	if len(states) > 0 && states[0] != nil {
		return states[0]
	}
	return translator.NewRequestState()
}

func upstreamAuthHeaders(route catalog.Route, key, sessionID string) http.Header {
	h := make(http.Header)
	if route == catalog.RouteMessages {
		h.Set("x-api-key", key)
		h.Set("anthropic-version", "2023-06-01")
	} else {
		h.Set("Authorization", "Bearer "+key)
	}
	h.Set("x-opencode-session", sessionID)
	return h
}

func convertNonStream(route catalog.Route, output string, status int, body []byte, states ...*translator.RequestState) ([]byte, *errclass.Error) {
	return protocol.ConvertResponse(route, output, status, body, requestState(states))
}

// classEnvelope renders a classified failure as the wire error envelope;
// ToEnvelopeError redacts and propagates Retryable/HTTPStatus host-side.
func classEnvelope(e *errclass.Error) []byte {
	wire := errclass.ToEnvelopeError(e)
	out, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: &wire})
	return out
}

type streamConverter = protocol.StreamConverter

func newStreamConverter(route catalog.Route, output string, state *translator.RequestState) (streamConverter, *errclass.Error) {
	return protocol.NewStreamConverter(route, output, state)
}

// handleExecuteStream implements executor.execute_stream (FR-006, §7).
// It decodes once and delegates to executeStream so a stream-flagged
// request arriving via executor.execute is never parsed twice.
func (m *Manager) handleExecuteStream(request []byte) ([]byte, error) {
	var req executorRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return ErrEnvelope("invalid_request", "malformed executor request body"), nil
	}
	debugTrace("executor stream invoked model=%s source_format=%s stream=%t original_body_%s payload_%s", req.Model, req.SourceFormat, req.Stream, debugBodyMeta(req.OriginalRequest), debugBodyMeta(req.Payload))
	return m.executeStream(req)
}

// executeStream runs the already-decoded stream execution: pre-first-byte errors
// (invalid request, unroutable model, upstream HTTP >=400) return immediately as
// error envelopes so CPA can failover pre-emission.
// On success (upstream HTTP 200 OK), the reading and emitting pump loop runs in
// a background goroutine and executeStream returns okEnvelope immediately so the
// host can start draining chunks to the downstream client without buffer deadlocks.
func (m *Manager) executeStream(req executorRequest) ([]byte, error) {
	res, failEnv := m.resolveExecution(req)
	if res == nil {
		return failEnv, nil
	}
	sessionID, eErr := resolveOpenCodeSessionID(req)
	if eErr != nil {
		return classEnvelope(eErr), nil
	}
	debugTrace("executor session mode=%s source_format=%s x_opencode_session=%s fallback=%t", "stream", req.SourceFormat, sessionID, sessionID == emptyOpenCodeSessionID)
	upstreamBody, eErr := buildUpstreamBody(res, req)
	if eErr != nil {
		return classEnvelope(eErr), nil
	}

	conv, eErr := newStreamConverter(res.rec.Protocol, req.outputFormat(), res.state)
	if eErr != nil {
		return classEnvelope(eErr), nil
	}
	url := catalog.JoinUpstreamURL(res.cfg.BaseURL, res.rec.EndpointPath)
	debugTrace("executor sending stream url=%s body_len=%d", url, len(upstreamBody))
	ctx, cancel := context.WithTimeout(withHostCallbackScope(m.workContext(), req.HostCallbackID), res.cfg.RequestTimeout)
	owned := false
	defer func() {
		if !owned {
			cancel()
		}
	}()
	st, _, id, err := m.bridge.DoStream(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     url,
		Headers: upstreamAuthHeaders(res.rec.Protocol, res.key, sessionID),
		Body:    upstreamBody,
	})
	debugTrace("executor stream DoStream status=%d upstreamID=%s err=%v", st, id, err)
	if err != nil {
		return classEnvelope(errclass.FromNetwork(err)), nil
	}
	if st >= 400 {
		var body []byte
		if id != "" {
			var once sync.Once
			closeUpstream := func() { once.Do(func() { _ = m.bridge.StreamClose(id) }) }
			watchDone, watchExited := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(watchExited)
				select {
				case <-ctx.Done():
					closeUpstream()
				case <-watchDone:
				}
			}()
			body, _, _, _ = m.bridge.StreamRead(id)
			closeUpstream()
			close(watchDone)
			<-watchExited
		}
		return classEnvelope(errclass.UpstreamStatusError(st, body)), nil
	}

	downID := req.StreamID
	owned = true
	m.work.Add(1) // the admitted handler still holds a work count
	if m.bridge != nil {
		m.bridge.inFlight.Add(1)
	}
	go func() {
		defer m.work.Done()
		defer cancel()
		if m.bridge != nil {
			defer m.bridge.inFlight.Done()
		}
		m.pumpStreamContext(ctx, downID, id, res, conv)
	}()
	return okEnvelope(struct{}{}), nil
}

func (m *Manager) pumpStreamContext(ctx context.Context, downID, upstreamID string, res *resolvedExecution, conv streamConverter) {
	var closeOnce sync.Once
	closeStreams := func(downErrMsg string) {
		closeOnce.Do(func() {
			_ = m.bridge.StreamClose(upstreamID)
			_ = m.bridge.StreamCloseDownstream(downID, downErrMsg)
		})
	}
	defer closeStreams("")

	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		select {
		case <-ctx.Done():
			closeStreams("stream canceled or exceeded request-timeout")
		case <-watchDone:
		}
	}()
	defer func() { close(watchDone); <-watchExited }()

	var (
		total          int64
		upstreamClosed bool
		convDone       bool
	)
	for {
		payload, readErrMsg, closed, err := m.bridge.StreamRead(upstreamID)
		debugTrace("executor stream read chunk_len=%d closed=%t readErrMsg=%q err=%v", len(payload), closed, readErrMsg, err)
		upstreamClosed = closed
		if ctx.Err() != nil {
			closeStreams("stream canceled or exceeded request-timeout")
			return
		}
		if err != nil {
			closeStreams(errclass.Redact(err.Error()))
			return
		}
		if readErrMsg != "" {
			closeStreams(errclass.Redact(readErrMsg))
			return
		}
		total += int64(len(payload))
		if total > res.cfg.MaxResponseBytes {
			closeStreams(errclass.Redact("stream exceeded max-response-bytes"))
			return
		}
		events, done, convErr := conv.Feed(payload)
		if emitErr := m.emitAll(downID, events); emitErr != nil {
			closeStreams(errclass.Redact(emitErr.Error()))
			return
		}
		if convErr != nil {
			closeStreams(errclass.Redact(convErr.Message))
			return
		}
		convDone = done
		if convDone || upstreamClosed {
			break
		}
	}
	debugTrace("executor stream loop end convDone=%t upstreamClosed=%t", convDone, upstreamClosed)
	if !convDone && upstreamClosed {
		events, finishErr := conv.Finish()
		if emitErr := m.emitAll(downID, events); emitErr != nil {
			closeStreams(errclass.Redact(emitErr.Error()))
			return
		}
		if finishErr != nil {
			closeStreams(errclass.Redact(finishErr.Message))
			return
		}
	}
}

// emitAll feeds converted events downstream in order. StreamEmit is
// deadline-bounded (emitTimeout), so a host that stops draining the
// downstream stream surfaces here as an error; the pump loop treats that
// as a post-first-byte stream-fatal failure and runs fail(), whose
// Once-closer releases both streams.
func (m *Manager) emitAll(downStreamID string, events [][]byte) error {
	for _, evt := range events {
		if err := m.bridge.StreamEmit(downStreamID, evt); err != nil {
			return err
		}
	}
	return nil
}
