# Contributing to cpa-opencode-go

Thank you for contributing. This guide covers how to set up the development environment, build the plugin, and submit pull requests.

Submit issues and pull requests to [dillonzq/cpa-opencode-go](https://github.com/dillonzq/cpa-opencode-go), which is maintained independently of the original project.

---

## Development Setup

### Prerequisites
- Go 1.26.7+, CGO enabled, and a C compiler targeting the CPA host platform
- Git

### Build & Test Commands

**Always build and test using the `debug` build tag for local development. Do not build in release mode.**

Run the test suite:
```powershell
go test -tags debug ./...
go test ./.github/scripts/...
```

Run the race detector:
```powershell
go test -tags debug -race ./...
```

On macOS/Linux with CGO, the debug suite also builds a temporary C-shared library and runs offline ABI tests against the pinned CLIProxyAPI SDK's actual loader and HTTP callbacks. The driver is `tests/native_host_test.go` and resolves the project root through the Go module system. The host tests are standard Go files under `tests/testdata/pluginhost`, which `./...` skips. The driver uses a Go overlay in a temporary SDK module to read host sources from the module cache and include those tests; only `go.mod` and `go.sum` are copied. Module downloads are disabled. Cache dependencies beforehand with `go mod download all`; tests need permission to bind local loopback ports. No real upstream or credentials are used. Other platforms still require a local debug C-shared build.

Run static analysis:
```powershell
go vet -tags debug ./... ./.github/scripts/...
```

Build the native shared library for local testing:
```powershell
go build -tags debug -buildmode=c-shared -o plugins/windows/amd64/cpa-opencode-go.dll .
```

---

## Pull Request Guidelines

1. **Focused Scope:** Keep each PR targeted to a single fix or feature. Avoid bundling unrelated refactors or features together.
2. **Debug-Mode Verification:** Verify all changes using `-tags debug`. Release packaging is handled separately by CI.
3. **Test Coverage:** All new or modified behavioral paths require unit tests. Both streaming and non-streaming tests are required for protocol changes.
4. **Offline Execution:** Unit tests must run locally and offline against mock host clients. Do not introduce dependencies on live external APIs.
5. **Documentation:** `RELEASE_NOTES.md` is a changelog. Add or merge a section for your change. Keep the newest version first and use `# vX.Y.Z` headings. The release tag must match the first heading. CI generates the release body from that section only; older sections stay in the file. Update both `README.md` and `README.zh-CN.md` for user-facing behavior or configuration changes, and both protocol conversion guides when mappings change.
6. **Code Style:** Format all code with standard `gofmt` and adhere to idiomatic Go conventions.

---

## Workflow

1. Fork the repository and create a feature branch from `main`.
2. Implement your changes following the code style and testing guidelines above.
3. Ensure `go test -tags debug ./...`, `go test ./.github/scripts/...`, and `go vet -tags debug ./... ./.github/scripts/...` pass cleanly.
4. Open a pull request against `main` with a clear description of the change and any related issue references.
