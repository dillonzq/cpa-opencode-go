package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

type callbackScopeKey struct{}

// Scope travels in the request context, never in shared bridge configuration.
func withHostCallbackScope(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callbackScopeKey{}, id)
}

type httpOperation struct {
	bridge     *HostBridge
	callbackID string
	id         string
	once       sync.Once
	cancelOnce sync.Once
	done       chan struct{}
}

type httpOperationRequest struct {
	HostCallbackID string `json:"host_callback_id,omitempty"`
	OperationID    string `json:"operation_id,omitempty"`
}

func (b *HostBridge) cancelHTTP(callbackID, operationID string) {
	if operationID == "" {
		return
	}
	_, _ = b.invoke(context.Background(), pluginabi.MethodHostHTTPCancel,
		httpOperationRequest{HostCallbackID: callbackID, OperationID: operationID}, "host http cancel")
}

func (b *HostBridge) openHTTP(ctx context.Context) (*httpOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	callbackID, _ := ctx.Value(callbackScopeKey{}).(string)
	payload, _ := json.Marshal(httpOperationRequest{HostCallbackID: callbackID})
	// Opening itself can finish late; release the reservation even when its ID
	// becomes available after cancellation. The callback/cleanup stays tracked.
	raw, _, err := b.callWithTimeoutResult(ctx, pluginabi.MethodHostHTTPOperationOpen, payload, 0, func(raw []byte) {
		if id, _ := decodeHTTPOperation(raw); id != "" {
			b.cancelHTTP(callbackID, id)
		}
	})
	if err != nil {
		return nil, err
	}
	id, err := decodeHTTPOperation(raw)
	if err != nil {
		return nil, err
	}
	op := &httpOperation{bridge: b, callbackID: callbackID, id: id, done: make(chan struct{})}
	b.inFlight.Add(1)
	go func() {
		defer b.inFlight.Done()
		select {
		case <-ctx.Done():
			// The caller may have completed just before its deferred cancel. Both
			// channels can then be ready; do not cancel an already-finished owner.
			select {
			case <-op.done:
				return
			default:
			}
			op.cancel()
		case <-op.done:
		}
	}()
	return op, nil
}

func decodeHTTPOperation(raw []byte) (string, error) {
	env, err := decodeEnvelope(raw)
	if err != nil || !env.OK {
		return "", fmt.Errorf("host http operation open failed")
	}
	var result httpOperationRequest
	if json.Unmarshal(env.Result, &result) != nil || result.OperationID == "" {
		return "", fmt.Errorf("host http operation open returned no operation ID")
	}
	return result.OperationID, nil
}

// finish releases local ownership. Host do finishes its operation on return;
// host stream_close finishes a streaming operation. Cancellation is idempotent.
func (op *httpOperation) finish() { op.once.Do(func() { close(op.done) }) }

// Cancellation itself stays tracked when a host callback is unresponsive;
// returning a local timeout never grants shutdown permission to unload it.
func (op *httpOperation) cancel() {
	op.cancelOnce.Do(func() {
		op.bridge.inFlight.Add(1)
		go func() {
			defer op.bridge.inFlight.Done()
			op.bridge.cancelHTTP(op.callbackID, op.id)
		}()
	})
	op.finish()
}
