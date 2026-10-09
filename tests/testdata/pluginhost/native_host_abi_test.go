//go:build debug && cgo && (darwin || linux)

package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenCodeNativeABI(t *testing.T) {
	started := make(chan string, 16)
	canceled := make(chan string, 16)
	var blockCatalog atomic.Bool
	var observedMu sync.Mutex
	var observed [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			if blockCatalog.Load() {
				started <- "catalog"
				<-r.Context().Done()
				canceled <- "catalog"
				return
			}
			io.WriteString(w, `{"data":[{"id":"glm-5.3"},{"id":"gpt-5.6-luna"},{"id":"minimax-m3"}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		observedMu.Lock()
		observed = append(observed, body)
		observedMu.Unlock()
		if r.URL.Path == "/responses" {
			w.Header().Set("X-Test-Session", r.Header.Get("X-Opencode-Session"))
			var req struct {
				Stream bool `json:"stream"`
			}
			_ = json.Unmarshal(body, &req)
			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "event: response.created\ndata: {\"response\":{\"id\":\"r1\"}}\n\nevent: response.output_text.delta\ndata: {\"delta\":\"partial\"}\n\nevent: response.completed")
			} else {
				io.WriteString(w, `{"id":"r1","object":"response","status":"completed","output":[]}`)
			}
			return
		}
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			Stream bool `json:"stream"`
		}
		if json.Unmarshal(body, &req) != nil || len(req.Messages) == 0 {
			http.Error(w, "invalid test body", 400)
			return
		}
		mode := req.Messages[0].Content
		if r.URL.Path == "/messages" && req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\nevent: message_stop")
			return
		}
		if strings.HasPrefix(mode, "cancel") || mode == "timeout" || mode == "quiesce" || mode == "scope-end" || mode == "backpressure" {
			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
				w.(http.Flusher).Flush()
				if mode == "backpressure" {
					for i := 0; i < 64; i++ {
						io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
						w.(http.Flusher).Flush()
					}
				}
			}
			started <- mode
			<-r.Context().Done()
			canceled <- mode
			return
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
			if mode != "truncated" {
				// Deliberately no [DONE] or final newline: legitimate terminal at EOF.
				io.WriteString(w, `data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`)
			}
			return
		}
		io.WriteString(w, `{"id":"c1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: t.TempDir()}
	file := pluginFile{ID: "cpa-opencode-go", Path: os.Getenv("CPA_NATIVE_PLUGIN")}
	nativeClient, err := defaultPluginLoader().Open(file, host)
	if err != nil {
		t.Fatal(err)
	}
	// Unload exactly as the real Unix loader does: close instance resources,
	// call the plugin shutdown export, free host API and dlclose.
	client := newGuardedPluginClient(nativeClient)
	defer client.Shutdown()
	register := func(timeout string) pluginapi.Plugin {
		t.Helper()
		yaml := []byte(fmt.Sprintf("api-keys:\n  - value: offline-test-key\nbase-url: %s\nallow-http: true\nrequest-timeout: %s\n", server.URL, timeout))
		plug, err := registerRPCPlugin(context.Background(), host, file.ID, client, pluginabi.MethodPluginReconfigure, yaml)
		if err != nil {
			t.Fatal(err)
		}
		return plug
	}
	plug := register("5s")
	rpc := &rpcPluginAdapter{id: file.ID, host: host, client: client, instance: pluginCallbackInstance(client)}
	auth := &coreauth.Auth{Provider: "opencode-go", Attributes: map[string]string{"api_key": "offline-test-key"}}
	// prepareExecutorCall/buildExecutorRequest are the actual host input contract.
	adapter := &executorAdapter{host: host, provider: "opencode-go", inputFormats: normalizeExecutorFormats(plug.Capabilities.ExecutorInputFormats), outputFormats: normalizeExecutorFormats(plug.Capabilities.ExecutorOutputFormats)}
	request := func(mode string, stream bool) pluginapi.ExecutorRequest {
		body := []byte(fmt.Sprintf(`{"model":"glm-5.3","messages":[{"role":"user","content":%q}],"stream":%t}`, mode, stream))
		prepared, err := adapter.prepareExecutorCall(coreexecutor.Request{Model: "opencode-go/glm-5.3", Payload: body}, coreexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"original"}]}`)})
		if err != nil {
			t.Fatal(err)
		}
		return buildExecutorRequest(host, "opencode-go", auth, prepared.req, prepared.opts)
	}
	await := func(ch <-chan string, want string) {
		t.Helper()
		select {
		case got := <-ch:
			if got != want {
				t.Fatalf("event %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %s event", want)
		}
	}
	t.Run("effective payload through host preparation", func(t *testing.T) {
		if _, err := rpc.Execute(context.Background(), request("intercepted", false)); err != nil {
			t.Fatal(err)
		}
		observedMu.Lock()
		body := string(observed[len(observed)-1])
		observedMu.Unlock()
		if !strings.Contains(body, "intercepted") || strings.Contains(body, "original") {
			t.Fatal("host effective payload was ignored")
		}
	})
	t.Run("native file without session override", func(t *testing.T) {
		req := pluginapi.ExecutorRequest{Model: "opencode-go/gpt-5.6-luna", AuthProvider: "opencode-go", AuthAttributes: auth.Attributes, SourceFormat: "openai-response", Format: "openai-response", Payload: []byte(`{"input":[{"role":"user","content":[{"type":"input_file","file_id":"file_example"}]}]}`)}
		if _, err := rpc.Execute(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("explicit empty payload never replays original", func(t *testing.T) {
		for _, stream := range []bool{false, true} {
			req := request("intercepted", stream)
			req.Payload = []byte{}
			req.Metadata = map[string]any{"canonical_session_id": "empty-effective-payload"}
			observedMu.Lock()
			before := len(observed)
			observedMu.Unlock()
			var err error
			if stream {
				_, err = rpc.ExecuteStream(context.Background(), req)
			} else {
				_, err = rpc.Execute(context.Background(), req)
			}
			if err == nil {
				t.Fatal("empty effective payload replayed original")
			}
			observedMu.Lock()
			after := len(observed)
			observedMu.Unlock()
			if after != before {
				t.Fatal("empty effective payload reached upstream")
			}
		}
	})
	t.Run("native image file references have distinct sessions", func(t *testing.T) {
		seen := map[string]bool{}
		for _, id := range []string{"image_file_a", "image_file_b"} {
			req := pluginapi.ExecutorRequest{Model: "opencode-go/gpt-5.6-luna", AuthProvider: "opencode-go", AuthAttributes: auth.Attributes, SourceFormat: "openai-response", Format: "openai-response", Payload: []byte(fmt.Sprintf(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":%q}]}]}`, id))}
			resp, err := rpc.Execute(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			sid := resp.Headers.Get("X-Test-Session")
			if len(sid) != 64 || seen[sid] {
				t.Fatal("native image files lost session identity")
			}
			seen[sid] = true
		}
	})
	t.Run("incomplete terminal headers fail through ABI", func(t *testing.T) {
		for _, tc := range []struct{ model, format, body string }{
			{"gpt-5.6-luna", "openai-response", `{"input":"partial","stream":true}`},
			{"minimax-m3", "claude", `{"messages":[{"role":"user","content":"partial"}],"max_tokens":16,"stream":true}`},
		} {
			req := pluginapi.ExecutorRequest{Model: "opencode-go/" + tc.model, AuthProvider: "opencode-go", AuthAttributes: auth.Attributes, SourceFormat: tc.format, Format: "openai-response", Payload: []byte(tc.body), Stream: true}
			resp, err := rpc.ExecuteStream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			var streamErr error
			for chunk := range resp.Chunks {
				output.Write(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if streamErr == nil || strings.Contains(output.String(), "response.completed") {
				t.Fatal("incomplete terminal header reported success")
			}
		}
	})
	t.Run("independent request scopes", func(t *testing.T) {
		ctxOne, cancelOne := context.WithCancel(context.Background())
		defer cancelOne()
		ctxTwo, cancelTwo := context.WithCancel(context.Background())
		defer cancelTwo()
		doneOne, doneTwo := make(chan error, 1), make(chan error, 1)
		go func() { _, err := rpc.Execute(ctxOne, request("cancel-one", false)); doneOne <- err }()
		await(started, "cancel-one")
		go func() { _, err := rpc.Execute(ctxTwo, request("cancel-two", false)); doneTwo <- err }()
		await(started, "cancel-two")
		cancelOne()
		await(canceled, "cancel-one")
		select {
		case <-doneTwo:
			t.Fatal("canceling one request canceled another")
		case <-time.After(30 * time.Millisecond):
		}
		cancelTwo()
		await(canceled, "cancel-two")
		if <-doneOne == nil || <-doneTwo == nil {
			t.Fatal("canceled execution succeeded")
		}
	})
	t.Run("callback scope end without context cancellation", func(t *testing.T) {
		id, closeScope := rpc.openHostCallbackContext(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := callPlugin[pluginapi.ExecutorResponse](context.Background(), client, pluginabi.MethodExecutorExecute, rpcExecutorRequest{ExecutorRequest: request("scope-end", false), HostCallbackID: id})
			done <- err
		}()
		await(started, "scope-end")
		closeScope()
		await(canceled, "scope-end")
		if <-done == nil {
			t.Fatal("scope end did not cancel HTTP")
		}
	})
	t.Run("stream scope lives past execute return", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		response, err := rpc.ExecuteStream(ctx, request("cancel-stream", true))
		if err != nil {
			t.Fatal(err)
		}
		await(started, "cancel-stream")
		select {
		case chunk := <-response.Chunks:
			if len(chunk.Payload) == 0 || chunk.Err != nil {
				t.Fatal("stream died on executor return")
			}
		case <-time.After(time.Second):
			t.Fatal("no stream data")
		}
		cancel()
		await(canceled, "cancel-stream")
		for range response.Chunks {
		}
	})
	t.Run("legitimate terminal and truncated EOF", func(t *testing.T) {
		for _, mode := range []string{"stream-ok", "truncated"} {
			req := request(mode, true)
			req.Format = "openai-response"
			response, err := rpc.ExecuteStream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			var streamErr error
			for chunk := range response.Chunks {
				output.Write(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if mode == "truncated" {
				if streamErr == nil || strings.Contains(output.String(), "response.completed") {
					t.Fatal("truncated stream reported success")
				}
			} else if streamErr != nil || strings.Count(output.String(), "event: response.completed") != 1 {
				t.Fatalf("legitimate EOF: %v", streamErr)
			}
		}
	})
	t.Run("plugin request timeout cancels transport", func(t *testing.T) {
		register("100ms")
		_, err := rpc.Execute(context.Background(), request("timeout", false))
		await(started, "timeout")
		await(canceled, "timeout")
		if err == nil {
			t.Fatal("timed out execution succeeded")
		}
		register("5s")
	})
	t.Run("quiesce cancels and drains", func(t *testing.T) {
		response, err := rpc.ExecuteStream(context.Background(), request("backpressure", true))
		if err != nil {
			t.Fatal(err)
		}
		await(started, "backpressure")
		// Stop consuming long enough to park a real host.stream.emit callback.
		time.Sleep(40 * time.Millisecond)
		blockCatalog.Store(true)
		reconfigured := make(chan error, 1)
		go func() {
			_, err := registerRPCPlugin(context.Background(), host, file.ID, client, pluginabi.MethodPluginReconfigure, []byte(fmt.Sprintf("api-keys:\n  - value: offline-test-key\nbase-url: %s\nallow-http: true\n", server.URL)))
			reconfigured <- err
		}()
		await(started, "catalog")
		drain := make(chan struct{})
		if _, err := callPlugin[struct{}](context.Background(), client, pluginabi.MethodPluginQuiesce, struct{}{}); err != nil {
			t.Fatal(err)
		}
		// HTTP cancellations may race each other.
		got := map[string]bool{}
		for i := 0; i < 2; i++ {
			select {
			case mode := <-canceled:
				got[mode] = true
			case <-time.After(2 * time.Second):
				t.Fatal("quiesce failed to cancel HTTP")
			}
		}
		if !got["catalog"] || !got["backpressure"] {
			t.Fatalf("unexpected canceled tasks: %v", got)
		}
		<-reconfigured
		go func() {
			for range response.Chunks {
			}
			close(drain)
		}()
		<-drain
		if _, err := rpc.Execute(context.Background(), request("intercepted", false)); err == nil {
			t.Fatal("quiesce admitted new work")
		}
	})
	host.httpOperations.mu.Lock()
	operations := len(host.httpOperations.operations)
	host.httpOperations.mu.Unlock()
	host.httpStreams.mu.Lock()
	streams := len(host.httpStreams.streams)
	host.httpStreams.mu.Unlock()
	if operations != 0 || streams != 0 {
		t.Fatalf("host resource leak: operations=%d streams=%d", operations, streams)
	}
}

// Host unload closes HTTP operations/streams before entering shutdown. Pin that
// ordering with live synchronous and asynchronous FFI work, without quiesce.
func TestOpenCodeNativeUnload(t *testing.T) {
	started, canceled := make(chan bool, 2), make(chan bool, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[{"id":"glm-5.3"}]}`)
			return
		}
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
			w.(http.Flusher).Flush()
		}
		started <- req.Stream
		<-r.Context().Done()
		canceled <- req.Stream
	}))
	defer server.Close()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: t.TempDir()}
	file := pluginFile{ID: "cpa-opencode-go", Path: os.Getenv("CPA_NATIVE_PLUGIN")}
	native, err := defaultPluginLoader().Open(file, host)
	if err != nil {
		t.Fatal(err)
	}
	client := newGuardedPluginClient(native)
	defer client.Shutdown()
	_, err = registerRPCPlugin(context.Background(), host, file.ID, client, pluginabi.MethodPluginRegister, []byte(fmt.Sprintf("api-keys:\n  - value: offline-test-key\nbase-url: %s\nallow-http: true\nrequest-timeout: 30s\n", server.URL)))
	if err != nil {
		t.Fatal(err)
	}
	rpc := &rpcPluginAdapter{id: file.ID, host: host, client: client, instance: pluginCallbackInstance(client)}
	req := func(stream bool) pluginapi.ExecutorRequest {
		return pluginapi.ExecutorRequest{Model: "opencode-go/glm-5.3", AuthProvider: "opencode-go", AuthAttributes: map[string]string{"api_key": "offline-test-key"}, SourceFormat: "openai", Stream: stream, Payload: []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"pending"}],"stream":%t}`, stream))}
	}
	nonstreamDone := make(chan error, 1)
	go func() { _, err := rpc.Execute(context.Background(), req(false)); nonstreamDone <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("nonstream did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response, err := rpc.ExecuteStream(ctx, req(true))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stream did not start")
	}
	unloaded := make(chan struct{})
	go func() {
		host.closeHostHTTPPluginResources(file.ID, pluginCallbackInstance(client))
		shutdownPluginClient(context.Background(), client)
		close(unloaded)
	}()
	select {
	case <-unloaded:
	case <-time.After(2 * time.Second):
		t.Fatal("unload deadlocked with host HTTP closure")
	}
	if <-nonstreamDone == nil {
		t.Fatal("unload let canceled HTTP succeed")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("unload left HTTP active")
		}
	}
	cancel() // host owns downstream cleanup after its callback instance closes
	for range response.Chunks {
	}
	host.httpOperations.mu.Lock()
	operations := len(host.httpOperations.operations)
	host.httpOperations.mu.Unlock()
	host.httpStreams.mu.Lock()
	streams := len(host.httpStreams.streams)
	host.httpStreams.mu.Unlock()
	if operations != 0 || streams != 0 {
		t.Fatal("unload leaked host HTTP resources")
	}
}

// Exercise credential naming through the real v8 host disk listing and
// host.auth.save (which uses os.WriteFile), plus the native plugin ABI.
func TestOpenCodeNativeAuthNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.Error(w, "unexpected offline request", 400)
			return
		}
		io.WriteString(w, `{"data":[{"id":"glm-5.3"}]}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	original := []byte(`{"type":"opencode-go","id":"existing-id","api_key":"offline-existing","label":"OpenCode Go credential Work","disabled":true,"custom":"preserved"}`)
	originalPath := filepath.Join(dir, "personal.json")
	if err := os.WriteFile(originalPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: dir}
	file := pluginFile{ID: "cpa-opencode-go", Path: os.Getenv("CPA_NATIVE_PLUGIN")}
	native, err := defaultPluginLoader().Open(file, host)
	if err != nil {
		t.Fatal(err)
	}
	client := newGuardedPluginClient(native)
	defer client.Shutdown()
	yaml := []byte(fmt.Sprintf("api-keys:\n  - value: offline-first\n    name: Personal\n  - value: offline-second\n    name: personal\n  - value: offline-third\n    name: CON\n  - value: offline-fourth\n    name: aux\nbase-url: %s\nallow-http: true\n", server.URL))
	for _, method := range []string{pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure} {
		if _, err := registerRPCPlugin(context.Background(), host, file.ID, client, method, yaml); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.ReadFile(originalPath)
	if err != nil || string(after) != string(original) {
		t.Fatal("real host save overwrote existing credential")
	}
	expected := map[string]string{"Personal-2.json": "Personal", "personal-3.json": "personal", "OpenCode-Go-CON.json": "CON", "OpenCode-Go-aux.json": "aux"}
	for name, label := range expected {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("credential %s missing: %v", name, err)
		}
		var record struct {
			Label string `json:"label"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(raw, &record); err != nil || record.Label != label || !strings.HasPrefix(record.ID, "opencode-go-key-") {
			t.Fatalf("credential label/identity changed: %s", name)
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != len(expected)+1 {
		t.Fatal("reconfigure duplicated credential files")
	}
	rpc := &rpcPluginAdapter{id: file.ID, host: host, client: client, instance: pluginCallbackInstance(client)}
	parsed, err := rpc.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "opencode-go", FileName: "personal.json", RawJSON: original})
	if err != nil || !parsed.Handled || parsed.Auth.Label != "OpenCode Go credential Work" || parsed.Auth.ID != "existing-id" || string(parsed.Auth.StorageJSON) != string(original) {
		t.Fatal("native parsing replaced custom label/metadata")
	}
}

// Invalid terminal snapshots must fail through the real host stream bridge,
// for both native passthrough and cross-protocol synthesis, with or without
// the final SSE separator. Valid snapshots must retain one terminal only.
func TestOpenCodeNativeTerminalShapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[{"id":"minimax-m3"},{"id":"gpt-5.6-luna"}]}`)
			return
		}
		var req struct {
			Input    string `json:"input"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid offline request", 400)
			return
		}
		mode := req.Input
		if len(req.Messages) > 0 {
			mode = req.Messages[0].Content
		}
		parts := strings.Split(mode, "/")
		if len(parts) != 3 {
			http.Error(w, "invalid offline test mode", 400)
			return
		}
		shape, ending, event := parts[0], parts[1], parts[2]
		payload := ""
		if r.URL.Path == "/messages" {
			switch shape {
			case "empty":
				payload = `{}`
			case "unrelated":
				payload = `{"type":"ping"}`
			case "valid":
				payload = `{"type":"message_stop"}`
			}
		} else {
			status := strings.TrimPrefix(event, "response.")
			switch shape {
			case "empty":
				payload = `{}`
			case "empty-response":
				payload = `{"response":{}}`
			case "missing-output":
				payload = fmt.Sprintf(`{"type":%q,"response":{"id":"resp_1","object":"response","status":%q}}`, event, status)
			case "valid":
				payload = fmt.Sprintf(`{"type":%q,"response":{"id":"resp_1","object":"response","status":%q,"output":[]}}`, event, status)
			}
		}
		if payload == "" {
			http.Error(w, "unknown offline shape", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == "/messages" {
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"}}\n\n")
			if shape == "valid" {
				io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
			}
		} else {
			io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n")
		}
		io.WriteString(w, "event: "+event+"\ndata: "+payload)
		if ending == "framed" {
			io.WriteString(w, "\n\n")
		}
	}))
	defer server.Close()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: t.TempDir()}
	file := pluginFile{ID: "cpa-opencode-go", Path: os.Getenv("CPA_NATIVE_PLUGIN")}
	native, err := defaultPluginLoader().Open(file, host)
	if err != nil {
		t.Fatal(err)
	}
	client := newGuardedPluginClient(native)
	defer client.Shutdown()
	_, err = registerRPCPlugin(context.Background(), host, file.ID, client, pluginabi.MethodPluginRegister, []byte(fmt.Sprintf("api-keys:\n  - value: offline-test-key\nbase-url: %s\nallow-http: true\n", server.URL)))
	if err != nil {
		t.Fatal(err)
	}
	rpc := &rpcPluginAdapter{id: file.ID, host: host, client: client, instance: pluginCallbackInstance(client)}
	for _, route := range []struct {
		model, source  string
		events, shapes []string
	}{
		{"minimax-m3", "claude", []string{"message_stop"}, []string{"empty", "unrelated", "valid"}},
		{"gpt-5.6-luna", "openai-response", []string{"response.completed", "response.incomplete"}, []string{"empty", "empty-response", "missing-output", "valid"}},
	} {
		for _, event := range route.events {
			for _, shape := range route.shapes {
				for _, ending := range []string{"framed", "eof"} {
					for _, output := range []string{"openai", "claude", "openai-response"} {
						t.Run(route.model+"/"+event+"/"+shape+"/"+ending+"/"+output, func(t *testing.T) {
							mode := shape + "/" + ending + "/" + event
							var body []byte
							if route.source == "claude" {
								body = []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":%q}],"max_tokens":16,"stream":true}`, mode))
							} else {
								body = []byte(fmt.Sprintf(`{"input":%q,"stream":true}`, mode))
							}
							req := pluginapi.ExecutorRequest{Model: "opencode-go/" + route.model, AuthProvider: "opencode-go", AuthAttributes: map[string]string{"api_key": "offline-test-key"}, SourceFormat: route.source, Format: output, Payload: body, Stream: true}
							resp, err := rpc.ExecuteStream(context.Background(), req)
							if err != nil {
								t.Fatal(err)
							}
							var events strings.Builder
							var streamErr error
							for chunk := range resp.Chunks {
								events.Write(chunk.Payload)
								if chunk.Err != nil {
									streamErr = chunk.Err
								}
							}
							if shape != "valid" {
								if streamErr == nil || strings.Contains(events.String(), "response.completed") || strings.Contains(events.String(), "response.incomplete") || strings.Contains(events.String(), "message_stop") || strings.Contains(events.String(), `"finish_reason":"stop"`) {
									t.Fatal("invalid terminal reported success through ABI")
								}
							} else {
								if streamErr != nil {
									t.Fatalf("valid terminal failed: %v", streamErr)
								}
								marker := "event: message_stop"
								if output == "openai-response" {
									marker = "event: response.completed"
									if route.source == "openai-response" {
										marker = "event: " + event
									}
								} else if output == "openai" {
									marker = `"finish_reason":"stop"`
									if event == "response.incomplete" {
										marker = `"finish_reason":"length"`
									}
								}
								if strings.Count(events.String(), marker) != 1 {
									t.Fatal("valid terminal missing/duplicated")
								}
							}
						})
					}
				}
			}
		}
	}
}
