# OpenCode Go session ID 兼容说明

[English](session-id.md) · [返回 README](../README.zh-CN.md)

本文说明当前插件如何选择会话标识并发送给 OpenCode Go。参考 [OpenCode v2 Go 文档](https://dev.opencode.ai/v2/docs/console/go#where-can-i-use-it)，核对日期为 2026-10-11。

## 官方约定

OpenCode Go 要求每段对话在 `x-opencode-session` 中携带稳定的 session ID，用于优化路由和提示词缓存。文档也说明 Go 能识别 Claude Code、Codex 和 ZCode 的原生会话 header；代理转发时应保留会话信息。该页面没有规定 ID 必须使用 UUID 或 `ses_` 前缀。

这里讨论的是发送给 Go 推理接口的会话 header。插件不调用 OpenCode 实例的 session 创建、查询或持久化 API。

## 选择顺序

插件按下表顺序取首个非空值，最终统一写入上游 `x-opencode-session`：

| 优先级 | 来源 |
| --- | --- |
| 1 | 宿主 `Metadata["canonical_session_id"]`，必须为非空字符串 |
| 2 | `X-Session-Affinity` |
| 3 | `X-Opencode-Session` |
| 4 | `X-Session-Id` |
| 5 | `X-Claude-Code-Session-Id` |
| 6 | `Session-Id` |
| 7 | 有效输入中的初始用户内容 SHA-256 |

宿主 canonical ID 优先于所有客户端 header，因此最终值可能与客户端原始 ID 不同。插件本身不对显式 ID 去空白、加前缀、转换为 UUID 或重新哈希；空字符串才会被跳过。header 使用 Go `http.Header.Get` 读取。

请求体中的 `session_id`、`thread_id`、Claude `metadata.user_id` 等身份字段不由插件直接提取；宿主若已识别这些字段，可以通过 `canonical_session_id` 传入。

## 有效输入与旧调用兼容

哈希使用宿主处理后的 `Payload`，在插件原生清洗和协议转换之前提取，因此包含拦截器修改。只有 `Payload` 为 nil（缺省或 null）时才回退到 `OriginalRequest`；显式空 payload 不回放原请求。

输入格式依次采用 `SourceFormat`、`Format`，二者均缺省时使用 `openai`。正常调用中，`SourceFormat` 表示输入协议，`Format` 决定输出协议。

## 内容哈希规则

最终值为提取内容的 SHA-256，编码成 64 位小写十六进制字符串；不生成随机 ID，也不维护插件会话表。

| 输入格式 | 提取范围 |
| --- | --- |
| `openai`（Chat Completions） | 跳过开头的 system/developer 消息，收集随后第一组连续 user 消息；遇到其他角色即停止，包括 user 前的 assistant/tool |
| `claude`（Messages） | 跳过首个 user 前的非 user 消息，收集第一组连续 user 消息，遇到非 user 即停止 |
| `openai-response`（Responses） | 字符串 `input` 直接参与哈希；数组跳过首个用户消息前的其他项，收集连续用户消息（`role: user` 且 type 为 message 或缺省），遇到其他项即停止 |

系统指令、消息级 ID/status、后续对话、模型与鉴权信息不参与插件兜底哈希。相同纯文本在三种格式下得到相同 ID；文本空白和换行保持原样。

| 内容 | 兼容处理 |
| --- | --- |
| 字符串、text/input_text/output_text | 按顺序直接拼接文本，保留历史哈希 |
| image_url/input_image，带 URL | 拼接 URL，保留历史哈希 |
| Claude image | URL 直接拼接；base64 转为 data URL，缺省 MIME 为 application/octet-stream |
| Claude tool_result | 递归提取其 content，保留历史哈希 |
| Claude thinking/redacted_thinking/tool_use | 沿用历史规则，不参与哈希 |
| 文件、仅含 file_id 的图片、未知内容 | 使用稳定 JSON 编码，前后加 NUL 分隔符后参与哈希；对象键顺序稳定，数值使用 json.Number 保留精度，移除该内容对象顶层 cache_control |

session 提取不调用协议转换解码器，也不校验无关原生字段，避免文件或未来内容类型仅因会话提取而被拒绝。格式不支持或顶层 JSON 解析失败时返回错误；其他输入形状留给请求适配器或上游校验。显式 ID 命中时跳过内容哈希，但请求仍需通过后续处理。

未提取到内容时使用空字符串的固定 SHA-256：

```text
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

## 发送范围与限制

Chat Completions、Messages、Responses 的流式与非流式推理请求都设置 `x-opencode-session`；模型目录 `/models` 请求不携带该 header。提取逻辑不记录用于哈希的内容，debug 日志会记录最终 session ID。

内容哈希是确定性兜底，不能完整区分真实会话：相同初始内容会产生相同 ID；历史裁剪、上下文压缩或拦截器修改初始内容可能改变 ID；没有可提取内容的请求共享空内容哈希。旧版文本拼接还会使不同分段产生相同结果，例如 `ab` + `c` 与 `a` + `bc`。

要保持每段对话的稳定身份，应优先由客户端提供显式会话 ID，或由宿主提供 canonical ID。兜底哈希保证有可发送的标识，不等同于已验证真实上游接受所有 ID。

## 实现与验证

- [session.go](../internal/plugin/session.go)：选择顺序、有效输入与哈希规则。
- [executor.go](../internal/plugin/executor.go)：流式/非流式调用及上游 header。
- [session_test.go](../internal/plugin/session_test.go)：优先级、旧哈希、跨格式一致性、原生文件与未知内容。
- [executor_contract_test.go](../internal/plugin/executor_contract_test.go)：显式 ID、原生文件和空 payload 行为。
- [integration_test.go](../internal/plugin/integration_test.go)：模拟上游三种协议及目录请求的 header。

定向验证命令：`go test ./internal/plugin -run 'Session|DeriveOpenCode' -count=1`。这些离线测试不替代真实上游验证。
