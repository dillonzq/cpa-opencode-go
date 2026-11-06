package plugin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"

	"github.com/dillonzq/cpa-opencode-go/internal/config"
	"github.com/dillonzq/cpa-opencode-go/internal/errclass"
)

const limitChunk = `data: {"id":"c1","object":"chat.completion.chunk","model":"glm-5.4","choices":[{"index":0,"delta":{"content":"x"}}]}` + "\n\n"

// tickingUpstream serves one content chunk every interval. After chunks
// reads it finishes the stream; chunks < 0 streams until closed.
func tickingUpstream(interval time.Duration, chunks int) func(string, []byte) ([]byte, error) {
	var reads atomic.Int64
	var closed atomic.Bool
	return wrapWithCatalog(`{"data":[{"id":"glm-5.4"}]}`, func(method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostHTTPDoStream:
			return hostOK(hostStreamStartResp{StatusCode: http.StatusOK, StreamID: "up-tick"}), nil
		case pluginabi.MethodHostHTTPStreamRead:
			if closed.Load() {
				return hostOK(hostStreamReadResp{Done: true}), nil
			}
			time.Sleep(interval)
			n := reads.Add(1)
			if chunks >= 0 && n > int64(chunks) {
				return hostOK(hostStreamReadResp{
					Payload: []byte(`data: {"id":"c1","object":"chat.completion.chunk","model":"glm-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"),
					Done:    true,
				}), nil
			}
			return hostOK(hostStreamReadResp{Payload: []byte(limitChunk)}), nil
		case pluginabi.MethodHostHTTPStreamClose:
			closed.Store(true)
			return hostOK(map[string]any{}), nil
		default:
			return hostOK(map[string]any{}), nil
		}
	})
}

// runLimitedStream registers with extraYAML, runs one stream to completion,
// and returns the initial envelope and the downstream close payload.
func runLimitedStream(t *testing.T, extraYAML string, responder func(string, []byte) ([]byte, error)) (pluginabi.Envelope, string) {
	t.Helper()
	f := &fakeCaller{}
	m := NewManager(NewHostBridge(f.call))
	t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
	f.responder = responder
	if _, err := m.HandleCall("plugin.register", lifecycleRequestBody(testValidYAML+extraYAML)); err != nil {
		t.Fatalf("register: %v", err)
	}
	resp, err := m.HandleCall("executor.execute_stream",
		execStreamReqBody("opencode-go/glm-5.4", "openai", []byte(ccRequestBody), "down-limit"))
	if err != nil {
		t.Fatalf("execute_stream: %v", err)
	}
	env := decodeEnv(t, resp)
	m.bridge.WaitForInFlight(5 * time.Second)
	downCloses := f.callsOf(pluginabi.MethodHostStreamClose)
	if !env.OK {
		if len(downCloses) != 0 {
			t.Fatalf("failed open must not close a downstream stream: %v", downCloses)
		}
		return env, ""
	}
	if len(downCloses) != 1 {
		t.Fatalf("downstream closes = %d, want 1", len(downCloses))
	}
	return env, string(downCloses[0].payload)
}

func TestExecuteStreamActiveStreamOutlivesRequestTimeout(t *testing.T) {
	// The stream lasts several times request-timeout but never goes quiet
	// for stream-idle-timeout, so it must complete normally.
	_, closePayload := runLimitedStream(t,
		"request-timeout: 30ms\nstream-idle-timeout: 1s\nmax-stream-duration: 10s\n",
		tickingUpstream(10*time.Millisecond, 15))
	if strings.Contains(closePayload, `"error"`) {
		t.Fatalf("active stream closed with error: %s", closePayload)
	}
}

func TestExecuteStreamIdleTimeoutRestartsOnEachChunk(t *testing.T) {
	// Chunks arrive every 20ms against a 60ms idle limit for ~200ms total.
	_, closePayload := runLimitedStream(t,
		"stream-idle-timeout: 60ms\n",
		tickingUpstream(20*time.Millisecond, 10))
	if strings.Contains(closePayload, `"error"`) {
		t.Fatalf("stream with regular chunks closed with error: %s", closePayload)
	}
}

func TestExecuteStreamMaxDurationEndsActiveStream(t *testing.T) {
	start := time.Now()
	_, closePayload := runLimitedStream(t,
		"stream-idle-timeout: 1s\nmax-stream-duration: 80ms\n",
		tickingUpstream(10*time.Millisecond, -1))
	if !strings.Contains(closePayload, "stream exceeded max-stream-duration") {
		t.Fatalf("close payload = %s, want max-stream-duration error", closePayload)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("max-stream-duration enforced after %v", elapsed)
	}
}

func TestExecuteStreamOpenTimeout(t *testing.T) {
	responder := wrapWithCatalog(`{"data":[{"id":"glm-5.4"}]}`, func(method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostHTTPDoStream:
			time.Sleep(150 * time.Millisecond)
			return hostOK(hostStreamStartResp{StatusCode: http.StatusOK, StreamID: "up-slow"}), nil
		default:
			return hostOK(map[string]any{}), nil
		}
	})
	env, _ := runLimitedStream(t, "request-timeout: 30ms\nstream-idle-timeout: 10s\n", responder)
	if env.OK || env.Error == nil {
		t.Fatalf("want error envelope, got %+v", env)
	}
	if env.Error.Code != string(errclass.ClassNetwork) || !env.Error.Retryable ||
		!strings.Contains(env.Error.Message, "stream did not open within request-timeout") {
		t.Fatalf("envelope error = %+v", env.Error)
	}
}

func TestStreamStopMessage(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{errStreamOpenTimeout, "stream did not open within request-timeout"},
		{errStreamIdleTimeout, "stream exceeded stream-idle-timeout"},
		{errMaxStreamDuration, "stream exceeded max-stream-duration"},
		{nil, "stream canceled"},
	} {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(tc.cause)
		if got := streamStopMessage(ctx); got != tc.want {
			t.Errorf("cause %v: message = %q, want %q", tc.cause, got, tc.want)
		}
	}
}

func newTestLimits(idle time.Duration) (*streamLimits, context.Context) {
	ctx, cancel := context.WithCancelCause(context.Background())
	return startStreamLimits(cancel, config.Config{
		RequestTimeout:    time.Hour,
		StreamIdleTimeout: idle,
		MaxStreamDuration: time.Hour,
	}), ctx
}

func TestStreamLimitsLateOpenTimerIgnoredAfterOpen(t *testing.T) {
	l, ctx := newTestLimits(time.Hour)
	defer l.stop()
	l.opened()
	l.openExpired() // a callback already running when the stream opened
	if err := context.Cause(ctx); err != nil {
		t.Fatalf("open timer canceled an opened stream: %v", err)
	}
}

func TestStreamLimitsIdleExcludesDownstreamDelivery(t *testing.T) {
	l, ctx := newTestLimits(30 * time.Millisecond)
	defer l.stop()
	l.opened()
	l.readStarted()
	l.readDone(true)
	time.Sleep(80 * time.Millisecond) // delivering the chunk downstream
	if err := context.Cause(ctx); err != nil {
		t.Fatalf("idle limit fired while no read was waiting: %v", err)
	}
	l.readStarted() // the chunk restarts the limit for this wait
	l.idleExpired() // a stale callback must not cancel a fresh wait
	if err := context.Cause(ctx); err != nil {
		t.Fatalf("stale idle callback canceled a fresh wait: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if err := context.Cause(ctx); !errors.Is(err, errStreamIdleTimeout) {
		t.Fatalf("cause = %v, want idle timeout", err)
	}
}

func TestStreamLimitsEmptyReadKeepsIdleDeadline(t *testing.T) {
	l, ctx := newTestLimits(60 * time.Millisecond)
	defer l.stop()
	l.opened()
	deadline := time.Now().Add(time.Second)
	for context.Cause(ctx) == nil && time.Now().Before(deadline) {
		l.readStarted()
		time.Sleep(10 * time.Millisecond)
		l.readDone(false) // reads without data are not activity
	}
	if err := context.Cause(ctx); !errors.Is(err, errStreamIdleTimeout) {
		t.Fatalf("cause = %v, want idle timeout", err)
	}
}

func TestExecuteStreamSlowDownstreamIsNotUpstreamIdle(t *testing.T) {
	// Each emit takes longer than stream-idle-timeout while the upstream
	// always has the next chunk ready.
	upstream := tickingUpstream(0, 4)
	responder := func(method string, payload []byte) ([]byte, error) {
		if method == pluginabi.MethodHostStreamEmit {
			time.Sleep(60 * time.Millisecond)
		}
		return upstream(method, payload)
	}
	_, closePayload := runLimitedStream(t, "stream-idle-timeout: 30ms\n", responder)
	if strings.Contains(closePayload, `"error"`) {
		t.Fatalf("slow downstream closed the stream with error: %s", closePayload)
	}
}
