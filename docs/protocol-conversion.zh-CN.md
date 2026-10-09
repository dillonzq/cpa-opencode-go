# 协议转换的保留与损失

[English](protocol-conversion.md) · [返回 README](../README.zh-CN.md)

本文件记录插件当前行为。推理响应映射参照依赖锁定的 **CPA v8.0.0**，不是对官方全部转换能力的承诺。本次修改覆盖响应；请求侧的历史推理策略没有改变。响应转换继续由插件自行实现，不导入 CPA 官方转换器。

## 推理响应

普通响应与流式响应均覆盖以下六个方向：

| 上游 → 客户端 | 保留方式 |
| --- | --- |
| Chat Completions → Messages | 可读推理映射为 `thinking` 块 / `thinking_delta` |
| Chat Completions → Responses | 可读推理映射为 `reasoning.summary[]`（`type: summary_text`）和对应 summary 流式事件 |
| Messages → Chat Completions | `thinking` 文本合并到 `message.reasoning_content` / `delta.reasoning_content` |
| Messages → Responses | 每个 `thinking` 块映射为独立 reasoning summary item |
| Responses → Chat Completions | reasoning item 的 `summary_text` 与可读 `content`（`reasoning_text` / `text`）合并到 `reasoning_content` |
| Responses → Messages | 同样的可读推理映射为 `thinking` 块 / `thinking_delta` |

Chat Completions 按 `reasoning_content` → `reasoning` → `reasoning_details` 的顺序取第一个包含可读文本的字段，支持字符串、数组和带 `text` 的对象。多个别名不会重复拼接。文本保留空白和 Unicode；不会混进最终回答，也不会生成不存在的推理。Chat Completions 的多个推理片段在 Responses 终止结果中聚合为一个 reasoning item；Messages 的多个 thinking 块分别保留为 reasoning items。Chat Completions 输出本身无法保留正文、推理和多个块之间的原始排列关系。

Responses 的流式转换接收 `response.reasoning_summary_text.delta/done` 与 `response.reasoning_text.delta/done`。`output_item` 和终止响应中的 reasoning 快照补充未通过 delta 发送的文本；相同 part 已发送的前缀不会重复输出。合成 Responses 流包含 item/summary 的开始、增量和结束事件，终止 output 的顺序、索引与 item 公告一致。

跨协议仍不传递 Anthropic `signature` / `signature_delta` / `redacted_thinking.data`，以及 Responses `encrypted_content`。这些是供应商专属的签名或不透明数据，没有可靠的跨协议映射；不会伪造签名，也不会当成可读推理。转换出的无签名 thinking 适合展示，不保证可直接回放到要求有效签名的 Anthropic 请求。

相同协议的响应通常原样透传，保留这些字段；Responses 的自定义工具恢复和流式错误/终止处理是例外。

参考官方源码：[Chat Completions → Messages](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.0/internal/translator/openai/claude/openai_claude_response.go)、[Messages → Chat Completions](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.0/internal/translator/claude/openai/chat-completions/claude_openai_response.go)、[Chat Completions → Responses](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.0/internal/translator/openai/openai/responses/openai_openai-responses_response.go)、[Responses → Chat Completions](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.0/internal/translator/codex/openai/chat-completions/codex_openai_response.go)。

## 其他响应字段

以下是已有行为，本次未扩大支持范围。

| 内容 | 默认行为 / 损失 |
| --- | --- |
| 文本、函数工具调用 | 转换并保留文本和参数；缺省参数补 `{}`；转 Messages 时解析参数，非法 JSON 报错 |
| 多候选 `choices` | 跨协议只处理第一个候选；同协议透传保留全部 |
| 多个消息 / 文本块 | Chat Completions 聚合正文；合成 Responses 聚合为一个 assistant message，定位在首次文本位置 |
| Chat Completions `message.refusal`、Responses message 内的 `refusal` part | 普通/流式响应保留。Chat Completions ↔ Responses 保留独立 refusal 字段/part；转 Messages 映射为可显示的 text。正常完成的拒绝映射为 content_filter/refusal，工具和截断信号仍优先 |
| 引用、`annotations`、logprobs、audio、供应商扩展字段 | 跨协议不复制，相关增量事件默认忽略 |
| 图片和其他输出类型 | Chat Completions → Messages 可转换支持的图片块；Chat Completions → Responses 的非文本输出报错。Messages 未支持的输出块报错。Responses message 中非 `output_text` part 会被忽略；未知 output item 仅在解码后可识别的 `content/name/call_id/arguments` 非空时报错，只有 `result/action` 等未建模字段的 item 可能静默丢失 |
| Responses 的工具专属事件 | 跨协议转换 `function_call` 和参数 delta/done；output_item 和终止快照补齐未收到的参数尾部，同一工具的 call_id/item_id 共用状态避免重复调用。工具的真实 call_id 和 name 与参数状态分别跟踪；身份不完整时 delta/done 都先缓存，等待完整工具 item 后只公告一次；终止时身份仍缺失则报错。转 Messages 时未完成工具阻塞后续正文、推理和工具，参数可继续流式输出，完成或终止补齐后才关闭块；不会重开同一工具块。搜索、图像生成等专属事件仍默认忽略 |
| Responses 正文/拒绝快照 | output_text/refusal 的 done、output_item 和终止快照补齐遗漏的尾部；按 item_id、content_index 和 part 类型去重。Messages 无独立 refusal 字段，文本按到达顺序输出；合成 Responses 的正文/refusal part 索引与终止结果保持一致 |
| 结束原因 | `stop` ↔ `end_turn`、`length` ↔ `max_tokens`、`tool_calls` ↔ `tool_use`、`content_filter` ↔ `refusal`。未知原因回退到普通结束；`stop_sequence` 的具体值不复制 |
| 工具调用与截断并存 | 转 Chat Completions / Messages 时实际工具调用优先于截断原因；转 Responses 时仍以 `length/max_tokens` 映射 `incomplete`，工具信号放在 output items 中 |
| Responses 状态 | `incomplete` → `length/max_tokens`；其他非流式状态回退为普通结束。合成 Responses 仅表达 completed/incomplete，不复制 `incomplete_details` 或完整响应配置 |
| 基础 usage | 输入/输出 token 映射；Chat Completions/Responses 的 total 按输入+输出重算；缺省 usage 合成零值 |
| 缓存 usage | Messages 的 input 不含缓存，转 Chat Completions/Responses 时加上 cache read+creation；反向转 Messages 时扣掉可表示的缓存量并限制为非负。cache read 保留；cache creation 在三个协议间映射为 cache_creation_input_tokens / cache_write_tokens，缺省细项保持缺省，显式零值保留，不额外重复计入输入总量 |
| 推理 token usage | Chat Completions ↔ Responses 保留 `reasoning_tokens`；转 Messages 没有独立细项，仍计入输出总量 |
| 身份与时间 | 响应 id/model 保留；合成 message/reasoning item 的 id 由响应 id 派生。普通跨协议 Chat Completions 的 created 使用当前时间；Responses 流转 Chat Completions 可使用上游 created_at；其他未建模身份/时间字段不复制 |
| SSE 包装 | 转换时重建目标事件，comment/retry/id/ping 等信息不映射；Chat Completions 仍按单条 data 行解析，不支持一条 JSON 跨多条 data 行 |

## 请求侧的丢弃与映射

| 内容 / 路径 | 当前行为 |
| --- | --- |
| 历史推理 | Messages → Chat Completions/Responses 丢弃 thinking/redacted_thinking；Responses → Chat Completions 丢弃 reasoning items；Chat Completions → Messages/Responses 不映射历史 reasoning 字段。Responses → Messages 保留 summary 文本为无签名 thinking |
| 原生 Responses 发给非 GPT 模型 | 删除历史 reasoning 和 compaction；原生 GPT 路径仅重写 model，保留其他内容 |
| reasoning 控制 | `reasoning_effort` ↔ `reasoning.effort`；转 Messages 使用固定 budget 表，反向 budget 按阈值映射 effort，不能精确往返。`none` 为关闭；`auto` 留给上游默认。不会按模型能力过滤或截断 budget |
| system/developer/instructions | 转成目标协议 system/instructions；Messages 还会合并 user/tool_result 与连续 assistant 内容；历史消息边界不保证原样保留 |
| token 限制 | `max_tokens/max_completion_tokens/max_output_tokens` 映射；Messages 必须有正数限制，缺省或非正数默认 4096；Responses → Chat Completions 的非正数 max_output_tokens 被省略 |
| stop/stop_sequences | Chat Completions ↔ Messages 保留；转 Responses 丢弃 |
| temperature/top_p | 跨协议映射；其他只被某协议支持的字段经结构体选择丢弃，例如 `n`、`seed`、penalties、logprobs、`logit_bias`、`response_format`、`metadata`、`store`、`service_tier`、`stream_options`、`previous_response_id`、`include`、工具 strict/cache_control 等。原生路径通常保留 |
| 空内容、内容类型 | 空文本块/无有效内容的消息通常被移除；跨协议未知输入类型、system 图片、无法表示的工具结果会明确报错，不会统一静默删除 |
| tool choice / parallel | 转换 auto/none/required/指定工具；Messages 的并行开关映射为 disable_parallel_tool_use；无法表达的组合报错 |
| Responses 工具声明 | `additional_tools` 合并到 tools；namespace 展平（长名称截断+摘要），响应恢复原身份；普通 custom 工具改为 function，输入包装为 JSON，Responses 响应恢复 custom 调用 |
| Responses 不兼容工具 | 在需要工具规范化的路径删除 custom `apply_patch`、`tool_search`、`image_generation`；转 Chat Completions/Messages 还删除 hosted `web_search/web_search_preview`。原生 GPT Responses 透传不走此规范化；Chat Completions → Messages/Responses 的 namespace 声明直接跳过 |
| 非 GPT 原生 Responses 搜索工具 | `web_search` 重建为仅含 type 的声明，附加选项被删除；未知顶层字段仍保留 |
| 原生 Chat Completions 请求 | developer 角色改为 system；缺少有效字符串 type 的顶层 thinking 对象删除 |
| 原生 Messages 请求 | 历史 system turn 折叠到顶层 system；其中 thinking/redacted_thinking 被省略；没有 system turn 时只重写 model |

实现入口：`internal/adapter/*/request.go`、`convert.go`、`stream.go`，共享工具规范化在 `internal/adapter/shared/responses_tools.go`，推理控制表在 `internal/thinking/thinking.go`。


## 后续兼容升级方向

当前已补齐推理、拒绝正文、缓存写入 usage，以及 Responses 正文/工具参数的结束快照兜底。以下能力仍有缺口，尚未承诺支持：

| 优先项 | 当前缺口 / 后续方向 |
| --- | --- |
| Responses 原生 custom 工具完整生命周期 | 当前原生流恢复 item 和 input done，但 function arguments delta、终止 output 仍需统一恢复；跨协议直接收到 custom_tool_call/input 增量也需要独立支持 |
| 结构化输出和工具 strict | 检查 Chat Completions response_format ↔ Responses text.format 的可表达映射，保留工具 strict；转 Messages 时应显式检查支持范围 |
| 多模态与未知输出 | 支持图像生成结果、音频等可表达内容；对 result/action 等未建模实际 payload 明确报错，避免静默成功 |
| 引用与 annotations | 保留目标协议可表达的引用、来源和偏移；无法无损映射的字段需有明确策略 |
| SSE 与工具元数据 | Chat Completions 迁移到完整 SSE 帧解析以支持多 data 行；Responses → Messages 已串行处理交错工具块；其他路径的工具 id/name 晚到、分片和交错生命周期仍需补齐 |

历史推理回放、压缩记录和供应商签名应继续按上游能力处理，不能仅为保留字段而强行发送不兼容数据。
