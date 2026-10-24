# 协议转换的保留与损失

[English](protocol-conversion.md) · [返回 README](../README.zh-CN.md)

请求、普通响应和流式响应使用固定版本的 [apix v0.1.0](https://github.com/dillonzq/apix/tree/v0.1.0)。支持 Chat Completions、Messages、Responses 的六个跨协议方向及同协议处理。详细字段映射和限制见[库的 LLM 协议说明](https://github.com/dillonzq/apix/blob/v0.1.0/docs/protocols/llm.zh-CN.md)。本文补充插件的宿主和上游兼容行为。

## 插件接入

CPA 的 `openai`、`claude`、`openai-response` 分别映射为库的 `chat-completions`、`messages`、`responses`。输入使用宿主有效 `Payload` 和 `SourceFormat`；仅缺省/null Payload 回退到 `OriginalRequest`，显式空 payload 不回放原始请求。`Format` 决定输出协议，可以与输入不同。未知格式在请求上游前报错。

每次执行独立创建一个 apix exchange，请求和响应共用工具身份映射。默认 `LossPolicyAllow`，允许已知请求损耗并收集库诊断；没有新增用户配置。协议错误和无法表示的实际输出仍报错，不因 allow 而静默接受。

HTTP、鉴权、目录路由、session、超时、取消、最大响应字节和 CPA 错误 envelope 仍由插件管理。Messages 使用 `x-api-key` 和 `anthropic-version: 2023-06-01`，其他协议使用 Bearer；都带 `x-opencode-session`。session 在清洗和转换前从有效输入提取，保留 canonical ID、显式 header、初始用户内容的优先级。

## 原生请求的上游兼容规则

库的同协议请求只改写 model。插件在此之前保留 OpenCode Go 的兼容规则，使用 RawMessage 保留未知字段和精确 JSON 数值：

| 原生路径 | 插件行为 |
| --- | --- |
| Chat → Chat | developer 改为 system；删除缺少有效字符串 type 的顶层 thinking |
| Messages → Messages | 历史 system turn 折叠到顶层 system；其 thinking/redacted_thinking 省略；图片或未知 system 块明确报错；没有 system turn 时只改 model |
| Responses → GPT Responses | 仅改 model，保留原生工具、reasoning、compaction 和其他字段 |
| Responses → 非 GPT Responses | 保留 reasoning 原始 content 和 summary，不用 summary 冒充原文；删除外来 encrypted_content、没有非空白 reasoning_text 原文或 summary_text 摘要的 reasoning（包括缺省/null/空数组或无效数组） 和 compaction；过滤 custom apply_patch、tool_search、image_generation；web_search 声明仅保留 type；然后由 exchange（`ResponsesOptions.NormalizeTools`）合并 additional_tools、展平 namespace、custom 和本地 shell 转 function，并登记工具身份供响应恢复 |

GPT 分支沿用 upstream ID 不区分大小写的 `gpt` 前缀判断。通用 custom 工具的 input 包装为字符串参数；非字符串历史 custom input 会报错。同一身份重复声明保留首次声明，展平后的身份冲突报错。Hosted 工具并非全部可转换，上游是否接受原生工具仍由其自身验证。

请求转换后，对 upstream ID 以 `deepseek` 开头（不区分大小写）的模型额外执行 OpenCode Go 兼容处理：Chat `messages[].content[].image_url` 及 Responses 消息 content、工具 output 中的 `input_image` 图片块，将 `detail: "original"` 改为 `"high"`。覆盖全部历史，包括 `function_call_output` 和 `custom_tool_call_output` 图片；工具 schema、参数字符串、其他 detail 值和其他模型保持不变。[DeepSeek 官方文档说明 `high` 与 `original` 等价](https://api-docs.deepseek.com/zh-cn/guides/vision/)，此处理兼容网关校验，并非 DeepSeek 直连接口不支持。跨协议媒体转换仍沿用库的映射，可能省略图片 detail。

## 跨协议能力

| 内容 | 行为与限制 |
| --- | --- |
| 文本、图片请求和工具 | 保留可表示内容和函数调用；未知输入或无法表示内容报错。跨协议 custom apply_patch 可按普通 custom 工具转换；Responses 输出恢复 namespace/custom/shell 身份 |
| 可读推理 | 映射为 reasoning_content、thinking 或 Responses 原始 content 与展示 summary；保留 Unicode/空白，与最终回答分离；合成的普通与流式完成项保留可回传原文 |
| 签名与不透明数据 | 跨协议不传递 Anthropic signature/redacted_thinking.data、Responses encrypted_content。Chat/Responses → Messages 默认省略无签名 thinking 历史；原生 Messages 历史和响应展示仍保留 |
| 拒绝与 usage | 保留可表示的拒绝、基础 token、cache read/write 和推理 token；缓存细项区分显式零与缺省；Chat/Responses total 重新计算 |
| 媒体输入 | 在三种协议间映射可表示的文件；音频/视频输入支持 Chat ↔ Responses。Messages 仅接受支持的 PDF、UTF-8 文本和 URL 文档；不支持的 MIME、仅 file ID 的文档、音频和视频明确报错。保留混合内容顺序；插件不抓取或上传媒体 |
| 本地 shell | Responses 本地 shell 声明、选择、历史调用与结果在 Chat/Messages 及经清洗的非 GPT 原生 Responses 上适配为函数工具；普通和流式 Responses 输出使用同一 exchange 恢复 shell action 和稳定 item ID。GPT 原生请求透传；托管 shell 仅原生保留，跨协议不支持旧 local_shell。插件不执行命令 |
| 缓存提示 | Chat/Responses → Messages 保留可表示的消息、内容块、工具 cache_control、TTL 和扩展字段；已有块级提示优先，不自动添加缓存断点；无法表示的提示按损耗策略处理 |
| 结构化输出 | JSON Schema 在 response_format.json_schema、text.format、output_config.format 之间转换；保留函数工具 strict。Messages 无法表示无 schema 的 json_object 时报错 |
| 托管搜索和引用 | Responses ↔ Messages 支持可表示的搜索和 URL 引用；Chat 无托管搜索执行表示。模型是否支持目标类型由上游验证；加密搜索内容和不可表示偏移仍可能丢失 |
| 图文工具结果 | Responses → Messages 保留 tool_result 中图文顺序；Responses → Chat 保持连续工具回复，再用带 call ID 的 user 消息承载图文。图片需要 image_url，不支持仅 file ID 的跨协议引用 |
| 未知输出 | 检查原始 payload，包括 result/action 等未建模字段；无法表示的实际输出报错。音频、图像生成等输出尚无完整支持 |
| 多候选与边界 | 跨协议只处理首个 choice；Chat 聚合正文，消息/块的原始排列不保证保留；未知字段不保证跨协议复制 |
| 状态与终止 | Responses failed/cancelled/协议 error 返回分类错误；跨协议普通响应的非终止状态报错；incomplete 映射截断 |

历史推理保留原文及其与 assistant 正文、并行工具调用、多轮工具交互的关联；请求回放不用 summary 替代原文。仅 summary 的历史和缺失签名在默认 allow 策略下产生语义损耗诊断。插件保持 AllowUnsignedThinking 关闭，不提供覆盖配置。已经缺失的原文无法恢复；compaction 和供应商签名仍受上游能力限制。CPA 的 usage 推理档位标签仍来自宿主传入的有效 payload；仅后缀控制和后缀覆盖的标签限制需要宿主改动，token 统计不受影响。

## 思考后缀

模型尾部 `model(value)` 先剥离用于目录 lookup，失败时尝试带括号的字面 ID；字面命中不应用后缀。CPA 可能在到达插件前拒绝字面括号 ID。

识别到的后缀先移除被覆盖的请求控制，调用库转换后再应用后缀，因此优先于请求体。`none`/`0` 关闭，`auto`/`-1` 采用上游默认，档位为 minimal–max。Messages 使用 adaptive thinking 和 `output_config.effort`；Messages、Chat、Responses 的数字后缀都按固定阈值映射 effort。未知后缀只剥离，不改请求控制。budget 不按模型能力或 max_tokens 限幅。

## SSE 与错误处理

库处理任意网络分块、多 data 行和 LF/CRLF/CR，负责工具交错、晚到身份和 done/item/终止快照补尾去重。合成 Responses 事件带递增 sequence_number。

库返回完整 SSE 帧。插件对 Chat 输出使用 SSEDecoder 提取裸 JSON并消费库的 DONE，由 CPA HTTP handler 包装 data 并在正常关闭时发送一次 DONE；Messages/Responses 输出完整帧。SSE comment/retry/id/ping 不保证跨协议保留。

Feed/Finish 同时返回事件和错误时，先发送已完成事件，再以错误关闭，不生成成功终止。EOF 且尚未完成时调用 Finish 校验有效终止；取消、超时和网络错误直接失败关闭。Chat finish_reason/DONE、Messages stop_reason/message_stop 可建立结束状态；Responses 需要合法终止事件。错误后不恢复转换或重发请求。库错误消息为空或纯空白时，桥接补充默认消息，防止 CPA 将空错误关闭当成正常结束。

实现：`internal/protocol` 为公开库封装；`internal/plugin/upstream_policy.go` 为原生清洗；`reasoning_suffix.go` 使用库 thinking 子包；`executor.go` 管理传输。测试覆盖 27 个基础转换场景、原生策略、多轮推理回放、媒体/缓存映射、shell 状态恢复、请求状态隔离及真实 CPA HTTP 流输出；离线测试不替代真实上游验证。
