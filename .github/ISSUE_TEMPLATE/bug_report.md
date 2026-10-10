---
name: Bug Report
about: Report a reproducible problem or unexpected error in the plugin
title: "[BUG] "
labels: bug
assignees: ''
---

### Describe the Bug
A clear and concise description of what the bug is.

### Reproduction Steps
1. Inbound endpoint: (e.g. `/v1/chat/completions`, `/v1/messages`, `/v1/responses`)
2. Target model ID: (e.g. `opencode-go/gpt-5.6-luna`)
3. Sanitized request payload:
```json
// Paste request payload here
```

### Expected Behavior
What should happen.

### Actual Behavior / Error Trace
What actually happened, including HTTP status code and upstream/plugin error output:
```text
// Paste error response or log trace here
```

### Environment
- **CLIProxyAPI Version:** (v8.0.0 or later)
- **Plugin Version / Commit:**
- **OS:** (e.g. Windows / Linux / macOS)
- **Go Version:** (if built from source)
