package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/dillonzq/cpa-opencode-go/internal/config"
)

const cleanupConfig = "api-keys: [{name: A, value: key-a}, {name: B, value: key-b}]\nmodels-dev: {enabled: false}\n"

func writeCleanupAuth(t *testing.T, dir, name, key string) []byte {
	t.Helper()
	raw := mustJSON(map[string]any{"type": ProviderID, "id": credentialID(key), "api_key": key, "disabled": true, "proxy_url": "http://localhost:9090", "priority": 5, "custom": map[string]any{"keep": "value"}})
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

// A disk-backed host double: runtime state is confirmed by the native host
// test; here listing the written disabled flag models CPA's file watcher.
func cleanupFixture(t *testing.T, dir string) (*Manager, *fakeCaller) {
	t.Helper()
	f := &fakeCaller{responder: func(method string, data []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			files, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			entries := []pluginapi.HostAuthFileEntry{}
			for _, file := range files {
				if !strings.HasSuffix(file.Name(), ".json") {
					continue
				}
				path := filepath.Join(dir, file.Name())
				raw, _ := os.ReadFile(path)
				var record authRecord
				_ = json.Unmarshal(raw, &record)
				entries = append(entries, pluginapi.HostAuthFileEntry{Name: file.Name(), Path: path, ID: file.Name(), Provider: record.Type, Type: record.Type, AuthIndex: "index-" + file.Name(), Disabled: record.Disabled})
			}
			return hostOK(hostAuthListResponse{Files: entries}), nil
		case pluginabi.MethodHostAuthSave:
			var req pluginapi.HostAuthSaveRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, err
			}
			return hostOK(struct{}{}), os.WriteFile(filepath.Join(dir, req.Name), req.JSON, 0600)
		case pluginabi.MethodHostHTTPDo:
			return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(testCatalogJSON)}), nil
		default:
			return hostOK(struct{}{}), nil
		}
	}}
	m := NewManager(NewHostBridge(f.call))
	t.Cleanup(func() { _, _ = m.HandleCall(pluginabi.MethodPluginShutdown, nil) })
	return m, f
}

func publishedModels(t *testing.T, m *Manager) int {
	t.Helper()
	count := 0
	for _, method := range []string{pluginabi.MethodModelStatic, pluginabi.MethodModelForAuth} {
		raw, _ := m.HandleCall(method, mustJSON(pluginapi.AuthModelRequest{AuthProvider: ProviderID}))
		var models pluginapi.ModelResponse
		decodeResult(t, raw, &models)
		count += len(models.Models)
	}
	return count
}

func TestCleanupThroughReconfigureAndKeylessLifecycle(t *testing.T) {
	dir := t.TempDir()
	writeCleanupAuth(t, dir, "A.json", "key-a")
	keep := writeCleanupAuth(t, dir, "B.json", "key-b")
	m, f := cleanupFixture(t, dir)
	apply := func(method, settings string) {
		t.Helper()
		raw, _ := m.HandleCall(method, lifecycleRequestBody(settings))
		if !decodeEnv(t, raw).OK {
			t.Fatalf("lifecycle failed: %s", raw)
		}
	}
	apply(pluginabi.MethodPluginRegister, cleanupConfig)
	if publishedModels(t, m) == 0 {
		t.Fatal("configured plugin published no models")
	}
	bad, _ := m.HandleCall(pluginabi.MethodPluginReconfigure, lifecycleRequestBody("api-keys: [{value: ''}]"))
	if decodeEnv(t, bad).OK {
		t.Fatal("invalid configuration accepted")
	}
	if files, _ := os.ReadDir(dir); len(files) != 2 {
		t.Fatal("invalid configuration removed credentials")
	}
	apply(pluginabi.MethodPluginReconfigure, "api-keys: [{name: Renamed, value: key-b}]\nmodels-dev: {enabled: false}\n")
	if _, err := os.Stat(filepath.Join(dir, "A.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("removed key's credential remains")
	}
	got, err := os.ReadFile(filepath.Join(dir, "B.json"))
	if err != nil || !bytes.Equal(got, keep) {
		t.Fatal("configured credential's disabled state or metadata changed")
	}
	requests := len(f.callsOf(pluginabi.MethodHostHTTPDo))
	apply(pluginabi.MethodPluginReconfigure, "api-keys: []\nmodels: [{name: glm-manual}]\n")
	apply(pluginabi.MethodPluginReconfigure, "")
	files, _ := os.ReadDir(dir)
	if len(files) != 0 || len(f.callsOf(pluginabi.MethodHostHTTPDo)) != requests {
		t.Fatal("empty key list retained files or requested upstream")
	}
	if publishedModels(t, m) != 0 {
		t.Fatal("keyless plugin published models")
	}
	apply(pluginabi.MethodPluginReconfigure, cleanupConfig)
	if files, _ = os.ReadDir(dir); len(files) != 2 {
		t.Fatal("adding keys after idle did not create credentials")
	}
	if publishedModels(t, m) == 0 {
		t.Fatal("adding keys after idle published no models")
	}
	_, _ = m.HandleCall(pluginabi.MethodPluginShutdown, nil)
	if files, _ := os.ReadDir(dir); len(files) != 2 {
		t.Fatal("shutdown removed configured credentials")
	}
}

func TestKeylessColdStartCleansStaleRecords(t *testing.T) {
	for _, settings := range []string{"", "api-keys: []", "api-keys: null"} {
		t.Run(settings, func(t *testing.T) {
			dir := t.TempDir()
			writeCleanupAuth(t, dir, "stale.json", "old-key")
			m, f := cleanupFixture(t, dir)
			raw, _ := m.HandleCall(pluginabi.MethodPluginRegister, lifecycleRequestBody(settings))
			if !decodeEnv(t, raw).OK || len(f.callsOf(pluginabi.MethodHostHTTPDo)) != 0 || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 0 {
				t.Fatalf("keyless registration failed: %s", raw)
			}
			if files, _ := os.ReadDir(dir); len(files) != 0 {
				t.Fatal("stale files remain")
			}
		})
	}
}

func TestCleanupOwnershipAndDiskOnlyRecords(t *testing.T) {
	dir := t.TempDir()
	writeCleanupAuth(t, dir, "managed.json", "old-key")
	foreign := map[string]string{
		"manual.json":   `{"type":"opencode-go","api_key":"manual"}`,
		"mismatch.json": `{"type":"opencode-go","id":"opencode-go-key-forged","api_key":"secret"}`,
		"provider.json": string(mustJSON(map[string]any{"provider": ProviderID, "id": credentialID("legacy"), "api_key": "legacy"})),
		"other.json":    string(mustJSON(map[string]any{"type": "codex", "id": credentialID("other"), "api_key": "other"})),
		"invalid.json":  `{`,
	}
	for name, raw := range foreign {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "managed.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	m, _ := cleanupFixture(t, dir)
	for _, name := range []string{"manual.json", "mismatch.json", "provider.json", "other.json", "invalid.json", "link.json", "managed.json"} {
		if err := m.cleanupAuthFile(context.Background(), config.Config{}, pluginapi.HostAuthFileEntry{Name: name, Path: filepath.Join(dir, name)}); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range foreign {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("foreign credential changed: %s", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "link.json")); err != nil {
		t.Fatal("symlink removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "managed.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disk-only owned record not removed")
	}
}

func TestCleanupRequiresRuntimeConfirmation(t *testing.T) {
	dir := t.TempDir()
	writeCleanupAuth(t, dir, "pending.json", "removed")
	entry := pluginapi.HostAuthFileEntry{Name: "pending.json", Path: filepath.Join(dir, "pending.json"), AuthIndex: "pending-index"}
	bridge := NewHostBridge(func(string, []byte) ([]byte, error) {
		return hostOK(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{entry}}), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := NewManager(bridge).cleanupAuthFile(ctx, config.Config{}, entry); err == nil {
		t.Fatal("unconfirmed runtime disable reported success")
	}
	if _, err := os.Stat(entry.Path); err != nil {
		t.Fatal("file removed before runtime confirmation")
	}
}

func TestTimedOutAuthWriteBlocksCleanupUntilSettled(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	bridge := NewHostBridge(func(string, []byte) ([]byte, error) {
		close(started)
		<-release
		return hostOK(struct{}{}), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- bridge.AuthSave(ctx, pluginapi.HostAuthSaveRequest{Name: "pending.json", JSON: []byte(`{}`)})
	}()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled save reported success")
	}
	wait, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err := bridge.waitAuthWrite(wait); err == nil {
		t.Fatal("cleanup allowed while timed-out write was still running")
	}
	close(release)
	bridge.inFlight.Wait()
	if err := bridge.waitAuthWrite(context.Background()); err != nil {
		t.Fatalf("settled write still blocks cleanup: %v", err)
	}
}

func TestFailedAuthSaveReleasesWriteSlot(t *testing.T) {
	bridge := NewHostBridge(func(string, []byte) ([]byte, error) {
		return hostErr("save_failed", "simulated"), nil
	})
	if err := bridge.AuthSave(context.Background(), pluginapi.HostAuthSaveRequest{Name: "x.json", JSON: []byte(`{}`)}); err == nil {
		t.Fatal("failed save reported success")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := bridge.waitAuthWrite(ctx); err != nil {
		t.Fatalf("failed save kept the write slot: %v", err)
	}
}

func TestCleanupPreservesReplacementFile(t *testing.T) {
	dir := t.TempDir()
	writeCleanupAuth(t, dir, "changed.json", "removed")
	entry := pluginapi.HostAuthFileEntry{Name: "changed.json", Path: filepath.Join(dir, "changed.json"), AuthIndex: "changed-index", Disabled: true}
	foreign := []byte(`{"type":"opencode-go","api_key":"operator-owned"}`)
	bridge := NewHostBridge(func(string, []byte) ([]byte, error) {
		// Replace the file just as CPA confirms runtime disable.
		if err := os.WriteFile(entry.Path, foreign, 0600); err != nil {
			return nil, err
		}
		return hostOK(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{entry}}), nil
	})
	if err := NewManager(bridge).cleanupAuthFile(context.Background(), config.Config{}, entry); err == nil {
		t.Fatal("replacement file reported as removed")
	}
	got, err := os.ReadFile(entry.Path)
	if err != nil || !bytes.Equal(got, foreign) {
		t.Fatal("replacement file was lost")
	}
	if files, _ := os.ReadDir(dir); len(files) != 1 {
		t.Fatal("temporary credential file was not restored")
	}
}

func TestKeylessStartupIgnoresUnmanagedInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(foreign, []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	// A configured startup refuses a plugin record whose identity is unreadable.
	unreadable := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unreadable, []byte(`{"type":"opencode-go","id":"manual"}`), 0600); err != nil {
		t.Fatal(err)
	}
	m, f := cleanupFixture(t, dir)
	raw, _ := m.HandleCall(pluginabi.MethodPluginRegister, lifecycleRequestBody("api-keys: []"))
	if !decodeEnv(t, raw).OK {
		t.Fatalf("idle startup rejected an unmanaged file: %s", raw)
	}
	got, err := os.ReadFile(foreign)
	if err != nil || string(got) != "{" || len(f.callsOf(pluginabi.MethodHostAuthSave)) != 0 {
		t.Fatal("idle startup changed an unmanaged credential")
	}
	if _, err := os.Stat(unreadable); err != nil {
		t.Fatal("idle startup removed an unowned credential")
	}
}

func TestCleanupRestoreDoesNotOverwriteNewFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	staged := "old.json.opencode-go-cleanup-test"
	old := writeCleanupAuth(t, dir, staged, "old-key")
	newFile := writeCleanupAuth(t, dir, "old.json", "new-key")
	if err := restoreCleanupFile(root, staged, "old.json"); err == nil {
		t.Fatal("restoration reported success over an occupied filename")
	}
	for name, want := range map[string][]byte{staged: old, "old.json": newFile} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("restoration lost credential %s", name)
		}
	}
}

func TestCleanupWaitsForExplicitFileBackedDisable(t *testing.T) {
	for _, mode := range []string{"empty", "unrelated", "runtime-only", "wrong-path"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			writeCleanupAuth(t, dir, "pending.JSON", "removed")
			entry := pluginapi.HostAuthFileEntry{Name: "pending.JSON", Path: filepath.Join(dir, "pending.JSON"), AuthIndex: "target"}
			listed := entry
			listed.Disabled = true
			switch mode {
			case "unrelated":
				listed.Path, listed.AuthIndex = filepath.Join(dir, "other.json"), "other"
			case "runtime-only":
				listed.RuntimeOnly = true
			case "wrong-path":
				listed.Path = filepath.Join(dir, "other.json")
			}
			bridge := NewHostBridge(func(string, []byte) ([]byte, error) {
				entries := []pluginapi.HostAuthFileEntry{listed}
				if mode == "empty" {
					entries = nil
				}
				return hostOK(hostAuthListResponse{Files: entries}), nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			if err := NewManager(bridge).cleanupAuthFile(ctx, config.Config{}, entry); err == nil {
				t.Fatal("missing physical disable confirmation accepted")
			}
			if _, err := os.Stat(entry.Path); err != nil {
				t.Fatal("unconfirmed file removed")
			}
		})
	}
	// A transient empty list must be retried until the actual file is disabled.
	entry := pluginapi.HostAuthFileEntry{Path: "/test/target.json", AuthIndex: "target", Disabled: true}
	calls := 0
	bridge := NewHostBridge(func(string, []byte) ([]byte, error) {
		calls++
		entries := []pluginapi.HostAuthFileEntry{}
		if calls > 1 {
			entries = append(entries, entry)
		}
		return hostOK(hostAuthListResponse{Files: entries}), nil
	})
	if err := NewManager(bridge).awaitDisabledAuth(context.Background(), entry); err != nil || calls < 2 {
		t.Fatalf("did not wait for explicit confirmation: %v", err)
	}
}

func TestCleanupDiscoversUnlistedAndNestedFiles(t *testing.T) {
	for _, keyless := range []bool{false, true} {
		for _, emptyList := range []bool{false, true} {
			t.Run(fmt.Sprintf("keyless=%t/empty=%t", keyless, emptyList), func(t *testing.T) {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
				writeCleanupAuth(t, dir, "removed.JSON", "removed")
				writeCleanupAuth(t, filepath.Join(dir, "nested"), "removed.JsOn", "nested-removed")
				kept := writeCleanupAuth(t, dir, "kept.json", "kept")
				foreign := filepath.Join(dir, "other.json")
				if err := os.WriteFile(foreign, []byte(`{"type":"codex"}`), 0600); err != nil {
					t.Fatal(err)
				}
				listed := []pluginapi.HostAuthFileEntry{{Name: "other.json", Path: foreign, Provider: "codex"}}
				bridge := NewHostBridge(func(string, []byte) ([]byte, error) {
					if emptyList {
						return hostOK(hostAuthListResponse{}), nil
					}
					return hostOK(hostAuthListResponse{Files: listed}), nil
				})
				m := NewManager(bridge)
				m.rememberAuthDirs(listed)
				cfg := config.Config{}
				if !keyless {
					cfg.APIKeys = []config.APIKey{{Value: "kept"}}
				}
				if err := m.cleanupAuthRecords(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{filepath.Join(dir, "removed.JSON"), filepath.Join(dir, "nested", "removed.JsOn")} {
					if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("unlisted credential remains")
					}
				}
				got, err := os.ReadFile(filepath.Join(dir, "kept.json"))
				if keyless {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatal("keyless cleanup retained credential")
					}
				} else if err != nil || !bytes.Equal(got, kept) {
					t.Fatal("configured credential changed")
				}
				if got, err := os.ReadFile(foreign); err != nil || string(got) != `{"type":"codex"}` {
					t.Fatal("foreign file changed")
				}
			})
		}
	}
}
