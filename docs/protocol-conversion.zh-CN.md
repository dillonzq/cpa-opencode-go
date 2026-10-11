# 协议转换的保留与损失

[English](protocol-conversion.md) · [返回 README](../README.zh-CN.md)

请求、普通响应和流式响应使用固定版本的 [api-translator v0.2.0](https://github.com/dillonzq/api-translator/tree/v0.2.0)。支持 Chat Completions、Messages、Responses 的六个跨协议方向及同协议处理。详细字段映射和限制见[库的协议说明](https://github.com/dillonzq/api-translator/blob/v0.2.0/docs/protocol-conversion.zh-CN.md)。本文补充插件的宿主和上游兼容行为。

## 插件接入

CPA 的 `openai`、`claude`、`openai-response` 分别映射为库的 `chat-completions`、`messages`、`responses`。输入使用宿主有效 `Payload` 和 `SourceFormat`；仅缺省/null Payload 回退到 `OriginalRequest`，显式空 payload 不回放原始请求。`Format` 决定输出协议，可以与输入不同。未知格式在请求上游前报错。

每次执行独立创建 `RequestState`，请求和响应共用工具身份映射。默认 `LossPolicyAllow`，允许已知请求损耗并收集库诊断；没有新增用户配置。协议错误和无法表示的实际输出仍报错，不因 allow 而静默接受。

HTTP、鉴权、目录路由、session、超时、取消、最大响应字节和 CPA 错误 envelope 仍由插件管理。Messages 使用 `x-api-key` 和 `anthropic-version: 2023-06-01`，其他协议使用 Bearer；都带 `x-opencode-session`。session 在清洗和转换前从有效输入提取，保留 canonical ID、显式 header、初始用户内容的优先级。

## 原生请求的上游兼容规则

库的同协议请求只改写 model。插件在此之前保留 OpenCode Go 的兼容规则，使用 RawMessage 保留未知字段和精确 JSON 数值：

| 原生路径 | 插件行为 |
| --- | --- |
| Chat → Chat | developer 改为 system；删除缺少有效字符串 type 的顶层 thinking |
| Messages → Messages | 历史 system turn 折叠到顶层 system；其 thinking/redacted_thinking 省略；图片或未知 system 块明确报错；没有 system turn 时只改 model |
| Responses → GPT Responses | 仅改 model，保留原生工具、reasoning、compaction 和其他字段 |
| Responses → 非 GPT Responses | 删除历史 reasoning/compaction；过滤 custom apply_patch、tool_search、image_generation；web_search 声明仅保留 type；然后调用 NormalizeResponsesRequest 合并 additional_tools、展平 namespace、custom 转 function，并登记工具身份供响应恢复 |

GPT 分支沿用 upstream ID 不区分大小写的 `gpt` 前缀判断。通用 custom 工具的 input 包装为字符串参数；非字符串历史 custom input 会报错。同一身份重复声明保留首次声明，展平后的身份冲突报错。Hosted 工具并非全部可转换，上游是否接受原生工具仍由其自身验证。

## 跨协议能力

| 内容 | 行为与限制 |
| --- | --- |
| 文本、图片请求和工具 | 保留可表示内容和函数调用；未知输入或无法表示内容报错。跨协议 custom apply_patch 可按普通 custom 工具转换；Responses 输出恢复 namespace/custom 身份 |
| 可读推理 | 映射为 reasoning_content、thinking 或 reasoning summary；保留 Unicode/空白，与最终回答分离；不伪造签名 |
| 签名与不透明数据 | 跨协议不传递 Anthropic signature/redacted_thinking.data、Responses encrypted_content。无签名 thinking 不保证可回放到要求有效签名的模型 |
| 拒绝与 usage | 保留可表示的拒绝、基础 token、cache read/write 和推理 token；缓存细项区分显式零与缺省；Chat/Responses total 重新计算 |
| 结构化输出 | JSON Schema 在 response_format.json_schema、text.format、output_config.format 之间转换；保留函数工具 strict。Messages 无法表示无 schema 的 json_object 时报错 |
| 托管搜索和引用 | Responses ↔ Messages 支持可表示的搜索和 URL 引用；Chat 无托管搜索执行表示。模型是否支持目标类型由上游验证；加密搜索内容和不可表示偏移仍可能丢失 |
| 图文工具结果 | Responses → Messages 保留 tool_result 中图文顺序；Responses → Chat 保持连续工具回复，再用带 call ID 的 user 消息承载图文。图片需要 image_url，不支持仅 file ID 的跨协议引用 |
| 未知输出 | 检查原始 payload，包括 result/action 等未建模字段；无法表示的实际输出报错。音频、图像生成等输出尚无完整支持 |
| 多候选与边界 | 跨协议只处理首个 choice；Chat 聚合正文，消息/块的原始排列不保证保留；未知字段不保证跨协议复制 |
| 状态与终止 | Responses failed/cancelled/协议 error 返回分类错误；跨协议普通响应的非终止状态报错；incomplete 映射截断 |

历史推理回放、compaction、供应商签名仍受上游能力限制，不能为保留字段而强行发送不兼容内容。CPA 的 usage 推理档位标签仍来自宿主传入的有效 payload；仅后缀控制和后缀覆盖的标签限制需要宿主改动，token 统计不受影响。

## 思考后缀

模型尾部 `model(value)` 先剥离用于目录 lookup，失败时尝试带括号的字面 ID；字面命中不应用后缀。CPA 可能在到达插件前拒绝字面括号 ID。

识别到的后缀先移除被覆盖的请求控制，调用库转换后再应用后缀，因此优先于请求体。`none`/`0` 关闭，`auto`/`-1` 采用上游默认，档位为 minimal–max。Messages 数字后缀保留精确 budget；Chat/Responses 使用固定阈值映射 effort。未知后缀只剥离，不改请求控制。budget 不按模型能力或 max_tokens 限幅。

## SSE 与错误处理

库处理任意网络分块、多 data 行和 LF/CRLF/CR，负责工具交错、晚到身份和 done/item/终止快照补尾去重。合成 Responses 事件带递增 sequence_number。

库返回完整 SSE 帧。插件对 Chat 输出使用 SSEDecoder 提取裸 JSON并消费库的 DONE，由 CPA HTTP handler 包装 data 并在正常关闭时发送一次 DONE；Messages/Responses 输出完整帧。SSE comment/retry/id/ping 不保证跨协议保留。

Feed/Finish 同时返回事件和错误时，先发送已完成事件，再以错误关闭，不生成成功终止。EOF 且尚未完成时调用 Finish 校验有效终止；取消、超时和网络错误直接失败关闭。Chat finish_reason/DONE、Messages stop_reason/message_stop 可建立结束状态；Responses 需要合法终止事件。错误后不恢复转换或重发请求。库错误消息为空或纯空白时，桥接补充默认消息，防止 CPA 将空错误关闭当成正常结束。

实现：`internal/protocol` 为公开库封装；`internal/plugin/upstream_policy.go` 为原生清洗；`reasoning_suffix.go` 使用库 thinking 子包；`executor.go` 管理传输。测试覆盖 27 个基础转换场景、原生策略、请求状态隔离及真实 CPA HTTP 流输出；离线测试不替代真实上游验证。
