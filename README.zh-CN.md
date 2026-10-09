# cpa-opencode-go

[English](README.md) | [简体中文](README.zh-CN.md)

用于 [CLIProxyAPI](https://help.router-for.me/plugin/development) 的 Go 原生动态库插件，将 OpenCode Go 的模型统一接入 `opencode-go` 服务商。

插件统一处理模型发现、协议转换和上游请求，并使用 CLIProxyAPI 内置的认证、调度、密钥轮换和冷却机制。

本项目由 [dillonzq](https://github.com/dillonzq) 独立维护，基于 [massiveits/opencode-go-cliproxyapi](https://github.com/massiveits/opencode-go-cliproxyapi)，保留原项目的 MIT 许可证和版权声明。

插件 ID 为 `cpa-opencode-go`。服务商 ID、凭证类型和默认模型前缀仍为 `opencode-go`。

## 解决的问题

OpenCode Go 通过多种 API 协议提供模型，包括 OpenAI Chat Completions（`/v1/chat/completions`）、Anthropic Messages（`/v1/messages`）和 OpenAI Responses（`/v1/responses`）。

直接在 CLIProxyAPI 中接入这些接口，需要分别配置不同协议的服务商，带来以下问题：

- **重复配置密钥**：同一组 API 密钥需要填入多个服务商配置。
- **调度与轮换分散**：不同协议无法共享密钥轮换、限流和冷却状态，一个协议耗尽额度后，其他协议无法同步处理。
- **客户端需要了解上游协议**：调用前必须知道目标模型使用哪种协议和接口。
- **模型列表分散**：模型分布在不同的服务商命名空间中，无法统一发现。

## 实现方式

插件使用共享密钥池，将 OpenCode Go 统一接入 `opencode-go` 服务商：

- **统一认证池**：密钥只需配置一次，由 CLIProxyAPI 在不同协议间统一调度、轮换和冷却。
- **自动转换协议并路由**：客户端直接请求模型，例如 `opencode-go/glm-5.2` 或 `opencode-go/gpt-5.6-luna`，无需了解上游的协议格式。
- **统一模型目录**：所有模型通过 `/v1/models` 提供，归属于 `opencode-go` 服务商。

## 功能

- **统一服务商命名空间**：默认使用 `opencode-go` 模型前缀，例如 `opencode-go/glm-5.2`、`opencode-go/qwen3.7-max` 和 `opencode-go/gpt-5.6-luna`。
- **多协议转换**：转换以下客户端协议与上游协议之间的请求及流式响应：
  - OpenAI Chat Completions（`/v1/chat/completions`）
  - Anthropic Messages（`/v1/messages`）
  - OpenAI Responses（`/v1/responses`）
- **思考与推理支持**：同格式保留推理参数，跨格式仅做固定档位/预算转换；模型能力元数据不参与过滤或限幅，由上游校验。保留关闭和 Claude adaptive 档位；跨格式自动模式无对应字段时使用目标接口默认行为，无法转换的其他值明确报错。
- **动态模型发现**：获取远程模型目录，支持本地回退和自定义路由覆盖。
- **多密钥调度**：使用 CLIProxyAPI 原生调度器，在不同协议间共享密钥轮换、重试和错误冷却状态。
- **原生额度查询**：通过 CLIProxyAPI 通用额度接口查询当前选中凭证的滚动、每周和每月额度，移除独立插件额度页面。

## 环境要求

- **CLIProxyAPI**：`v8.0.0+`
- **Go 工具链**：Go 1.26.7+，构建 C 共享库时需启用 CGO。

## 请求行为

- 按 `SourceFormat` 执行 CLIProxyAPI 的有效 `Payload`，保留拦截器修改。只有缺省或 null Payload 的旧调用才回退到 `OriginalRequest`；显式空 Payload 按空输入校验；`Format` 决定输出协议。
- 每次上游 HTTP 调用独立持有宿主 operation 与请求 callback scope。客户端断开、scope 结束、`request-timeout` 和目录刷新停止会取消实际 HTTP；流式超时覆盖建立连接与消费流的总时长。
- `plugin.quiesce` 拒绝新工作、取消活动任务并等待回调退出。Shutdown 等待所有回调结束后才允许卸载动态库，因此永不返回的宿主回调会一直延迟关闭。
- 流在协议终止状态前 EOF 会报告错误，不会把部分文本或工具参数伪造成成功。已观察到 `finish_reason`/`stop_reason` 后仍兼容缺失 `[DONE]`/`message_stop`；EOF 时会处理没有末尾分隔符的最后一条 SSE 数据；终止事件缺少完整数据时报告错误。Messages 要求 `type: message_stop`；Responses 要求事件类型匹配，嵌套响应具备非空 `id`、`object: response`、匹配的终止 `status` 和 `output` 数组。
- 会话标识依次采用 `canonical_session_id`、显式会话 header、有效输入中的初始用户内容。已有文本、图片和工具结果的哈希保持兼容；文件、图像文件引用与未知原生内容采用稳定 JSON 哈希，不参与转换校验，也不记录内容。拦截器改变初始用户内容时，fallback 哈希随之改变；显式标识仍优先。

## 构建

本地开发使用 `debug` 标签构建动态库，发布构建由 CI 完成。

### Windows（AMD64）

```powershell
go build -tags debug -buildmode=c-shared -o plugins/windows/amd64/cpa-opencode-go.dll .
```

### Linux（AMD64）

```bash
go build -tags debug -buildmode=c-shared -o plugins/linux/amd64/cpa-opencode-go.so .
```

### macOS（ARM64）

```bash
go build -tags debug -buildmode=c-shared -o plugins/darwin/arm64/cpa-opencode-go.dylib .
```

将动态库放入 CLIProxyAPI 的插件目录，例如 `<cliproxyapi_root>/plugins/<os>/<arch>/`。

发布构建会将 Git 标签中的版本号注入插件元数据。本地构建默认显示 `0.0.0-dev`；如需指定版本，可添加 `-ldflags "-X github.com/dillonzq/cpa-opencode-go/internal/plugin.pluginVersion=<version>"`。

### 从 opencode-go-cliproxyapi 迁移

1. 停止 CLIProxyAPI。
2. 将配置键 `plugins.configs.opencode-go-cliproxyapi` 改为 `plugins.configs.cpa-opencode-go`，保留插件设置，并设置 `enabled: true`。
3. 删除所有旧的 `opencode-go-cliproxyapi` 动态库，包括 `opencode-go-cliproxyapi-v0.1.10.dylib` 这类带版本号的文件，然后安装对应平台的 `cpa-opencode-go` 动态库。两个插件会注册同一个服务商，请勿同时启用。
4. 重启 CLIProxyAPI，并强制刷新管理中心页面。额度查询改用[账户名称与额度](#账户名称与额度)中的原生接口，需要 CLIProxyAPI v8.0.0 或更高版本，以及支持通用额度接口的管理界面。

已有的 `opencode-go/<model>` 模型 ID、API 密钥和凭证记录可以继续使用。

如果旧插件通过插件商店安装，配置中可能包含记录旧仓库和锁定版本的 `store` 块。手动安装新版时，应从改名后的配置中删除这个块，否则 CPA 可能因动态库文件名不匹配旧版本而跳过加载。如果继续使用插件商店管理，应安装 ID 为 `cpa-opencode-go`、指向 `dillonzq/cpa-opencode-go` 的新条目，不要复制旧的 `store` 元数据。

## 配置

在 CLIProxyAPI 的 `config.yaml` 中，通过 `plugins.configs.cpa-opencode-go` 配置插件。全局插件开关和此插件的开关都需设置为 `enabled: true`。

```yaml
plugins:
  enabled: true
  configs:
    cpa-opencode-go:
      enabled: true
      # 上游基础 URL（默认："https://opencode.ai/zen/go/v1"）
      base-url: "https://opencode.ai/zen/go/v1"

      # 可选：覆盖模型目录地址（默认："{base-url}/models"）
      # catalog-url: "https://opencode.ai/zen/go/v1/models"

      # 客户端可见的模型 ID 前缀
      model-prefix:
        enabled: true           # true："opencode-go/<model>"；false："<model>"（默认：true）
        value: "opencode-go"    # 前缀名称（默认："opencode-go"）

      # OpenCode Go API 密钥，至少需要一个；支持 ${ENV_VAR} 环境变量展开
      api-keys:
        - value: "sk-opencode-key-1"
          name: "Personal"      # 可选账户名称
        - value: "sk-opencode-key-2"
        - value: "${OPENCODE_GO_API_KEY}"

      # 模型目录发现设置
      catalog:
        refresh-interval: "15m"          # 刷新间隔，最短 "1m"（默认："15m"）
        stale-while-unavailable: true    # 刷新失败时保留上次成功的目录（默认：true）

      # 协议开关，默认全部启用
      protocols:
        chat-completions: true   # 启用路由至 /v1/chat/completions 的模型
        messages: true           # 启用路由至 /v1/messages 的模型
        responses: true          # 启用路由至 /v1/responses 的模型

      # 按模型覆盖路由，优先于内置的前缀路由规则
      route-overrides:
        "custom-model":
          protocol: "messages"           # "chat-completions" | "messages" | "responses"
          endpoint: "/v1/messages"       # 必须以 / 开头

      # 请求执行设置
      request-timeout: "5m"              # 上游请求超时（默认："5m"）
      max-response-bytes: 67108864       # 非流式响应体的字节数上限（默认：64 MiB）
      allow-http: false                  # 允许本地模拟或测试使用 http://（默认：false）
```

### 配置项

| 配置项 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `api-keys` | `[]object` | 必填 | API 密钥列表，格式为 `- value: "..."`，可选 `name: "Personal"`。支持 `${ENV_VAR}` 环境变量展开，不允许重复或空值。 |
| `base-url` | `string` | `https://opencode.ai/zen/go/v1` | 上游基础 URL。必须是有效的 HTTPS 地址；仅在 `allow-http: true` 时允许 HTTP。不得包含查询参数、片段或用户认证信息。 |
| `catalog-url` | `string` | `{base-url}/models` | 模型目录的完整 URL，默认使用 `{base-url}/models`。 |
| `model-prefix.enabled` | `bool` | `true` | 为 `true` 时，客户端模型名称使用 `<prefix>/<model>`；为 `false` 时，仅使用模型 ID。 |
| `model-prefix.value` | `string` | `opencode-go` | 启用模型前缀时使用的前缀名称。 |
| `catalog.refresh-interval` | `duration` | `15m` | 模型目录刷新间隔，例如 `15m`、`1h`，最短为 `1m`。 |
| `catalog.stale-while-unavailable` | `bool` | `true` | 刷新失败时，继续使用上次成功获取的模型目录。 |
| `protocols.chat-completions` | `bool` | `true` | Chat Completions 协议开关。 |
| `protocols.messages` | `bool` | `true` | Messages 协议开关。 |
| `protocols.responses` | `bool` | `true` | Responses 协议开关。 |
| `route-overrides` | `map` | `{}` | 按模型 ID 覆盖 `{ protocol: "...", endpoint: "..." }`。协议可选 `chat-completions`、`messages` 或 `responses`。 |
| `request-timeout` | `duration` | `5m` | 上游连接建立及完整流的总超时；到期取消宿主 operation，必须大于零。 |
| `max-response-bytes` | `int64` | `67108864`（64 MiB） | 非流式响应体的字节数上限。 |
| `allow-http` | `bool` | `false` | 允许 `base-url` 和 `catalog-url` 使用 `http://`，用于本地测试。 |

## 测试

```powershell
# 运行全部测试
go test -tags debug ./...

# 验证发布压缩包包含动态库和许可证
go test ./.github/scripts

# 查看测试覆盖率
go test -tags debug ./... -cover

# 运行静态检查
go vet -tags debug ./... ./.github/scripts
```

本地开发的 debug 构建和验证要求见 [贡献指南](CONTRIBUTING.md)。

## 账户名称与额度

可为每个 `api-keys` 条目设置 `name`，例如 `Personal` 或 `Work`。未设置时，单密钥显示为 **OpenCode Go**，多密钥按配置顺序显示为 **OpenCode Go 1**、**OpenCode Go 2** 等。显式名称不受密钥顺序调整影响。上游额度响应不包含邮箱或账户身份，插件不会从密钥推断邮箱。

新凭证文件使用可读名称。已有凭证 ID 和文件名保持不变；解析时仅替换由旧前缀（`OpenCode Go credential ` 或 `opencode-go-key-`）加完整 64 位十六进制摘要组成的自动标签，其余自定义标签保留；配置明确指定的 `name` 仍优先。如需手动改名，先备份凭证文件并停止 CLIProxyAPI，再修改 `.json` 文件名，保持 JSON 中的 `id` 和其他字段不变。

新文件名按大小写不敏感规则检查冲突，并追加编号，例如已有 `personal.json` 时生成 `Personal-2.json`。Windows 保留设备名会加上 `OpenCode-Go-` 前缀，例如 `CON` 生成 `OpenCode-Go-CON.json`。这些规则在所有平台生效，仅改变新文件名，不改变账户显示名称。

通过 `GET /v0/management/quota/providers` 查询支持情况，再以 `{"auth_index":"<选中的索引>"}` 调用 `POST /v0/management/quota/fetch`。两者均需 CLIProxyAPI 管理密钥。标准响应包含 `subscription.plan`、`groups[].buckets[].window`、`remainingFraction` 和 `resetTime`。缺失的时间窗口会被省略，无效数据会返回错误，不会填充为零。

额度重置不受支持，旧版插件额度接口和独立页面已移除。管理界面需支持通用额度 API 才能显示插件刷新控件。
