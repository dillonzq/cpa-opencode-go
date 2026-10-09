//go:build debug && cgo && (darwin || linux)

package tests_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run the pinned SDK's real Unix loader, RPC adapter, callback registry and
// HTTP transport against the debug shared library. The SDK's internal package
// cannot be imported here, so use a temporary SDK module with a Go overlay.
// SDK sources are read directly from the cache; testdata adds our host tests.
// The module cache and this repository are never modified by the harness.
func TestNativeHostABI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// Overlay keys must use the same physical paths as the Go subprocess;
	// macOS temp paths commonly have /var -> /private/var aliases.
	temp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "CPA_NATIVE_PLUGIN="+filepath.Join(temp, "cpa-opencode-go.so"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	// Go runs this package from tests/. Ask the module system for the root
	// so building the plugin and locating testdata do not depend on that layout.
	root := strings.TrimSpace(string(run("", "list", "-m", "-f", "{{.Dir}}")))
	if root == "" {
		t.Fatal("project module root is unavailable")
	}
	sdkDir := strings.TrimSpace(string(run(root, "list", "-m", "-f", "{{.Dir}}", "github.com/router-for-me/CLIProxyAPI/v8")))
	run(root, "build", "-tags", "debug", "-buildmode=c-shared", "-o", filepath.Join(temp, "cpa-opencode-go.so"), ".")
	hostDir := filepath.Join(temp, "host")
	replacements := make(map[string]string)
	err = filepath.WalkDir(sdkDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sdkDir, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(hostDir, rel)
		if entry.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Go needs a physical module root. Only its manifests are copied;
		// every source/embedded asset stays in the read-only module cache.
		if rel == "go.mod" || rel == "go.sum" {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(dest, data, 0644)
		}
		replacements[dest] = path
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// testdata is ignored by ./... in this repository. The overlay makes the
	// same standard Go test file a member of the real SDK's pluginhost package.
	replacements[filepath.Join(hostDir, "internal", "pluginhost", "opencode_native_test.go")] = filepath.Join(root, "tests", "testdata", "pluginhost", "native_host_abi_test.go")
	overlay, err := json.Marshal(struct{ Replace map[string]string }{Replace: replacements})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(temp, "host-overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TestOpenCodeNativeABI", "TestOpenCodeNativeUnload", "TestOpenCodeNativeAuthNames", "TestOpenCodeNativeTerminalShapes"} {
		// A fresh process for each load: C-shared Go runtimes need not support
		// unloading/reinitializing the same library in a single host process.
		out := run(hostDir, "test", "-overlay", overlayPath, "-tags", "debug", "./internal/pluginhost", "-run", "^"+name+"$", "-count=1", "-timeout=90s", "-v")
		t.Logf("%s", out)
	}
}
